package main

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"
)

// HTTPS mode: the private CA, or Let's Encrypt.
//
// Caddy serves the apps in ONE of two ways, each a complete config of its own:
//
//   - Private CA (the default): the committed caddy/Caddyfile — every app at https://<ip>:<port>
//     with a certificate from Caddy's own CA, which each device installs once; port 80 serves
//     that CA for download. LAN-only, no domain needed, and nothing generated: an install that
//     never touches Let's Encrypt runs the file as shipped.
//   - Let's Encrypt: a config hsctl generates into caddy/generated/Caddyfile — every app at
//     https://<app>.<domain> on :443 with a publicly trusted certificate, so there's nothing to
//     install on any device and the Bitwarden / Nextcloud apps simply connect. hsctl points Caddy
//     at it with CADDY_CONFIG in caddy/.env (docker-compose.yml passes that to `caddy run`).
//     This REPLACES the private CA: the IP:port sites are gone, and port 80 only redirects the
//     names to https (plus a plain info page for the bare IP). Switching back unsets CADDY_CONFIG
//     and Caddy loads the committed Caddyfile again — so that file is always the fallback, and a
//     restart at any point (even mid-upgrade) has a complete config to load.
//
// Both configs import caddy/sites.d/*.caddy, where sites the user adds live in either mode.
//
// Let's Encrypt has to see that you control the domain. Two ways:
//   - dns (the default): Caddy creates a TXT record through your DNS provider's API. NOTHING is
//     port-forwarded, so the box stays LAN-only — this project's whole security posture. It
//     needs a Caddy build carrying the provider's plugin, which hsctl builds from
//     caddy/Dockerfile the first time (`homeserver-caddy:<provider>`).
//   - http: Let's Encrypt fetches http://<host>/.well-known/... from the internet, so port 80
//     must be forwarded to this machine and the names must resolve to your PUBLIC IP. Simpler,
//     but it exposes the box — docs/security.md explains the trade-off.
//
// Devices on the LAN must RESOLVE the names to the server's LAN IP — before the switch, or the
// dashboard is unreachable until they do. hsctl writes them into Pi-hole's custom.list (covered
// if Pi-hole is your DNS); otherwise add them in the router or as public A records pointing at
// the LAN IP — docs/setup.md walks through it. `hsctl letsencrypt disable` on the box itself
// always brings the IP sites back.

const (
	challengeDNS  = "dns"
	challengeHTTP = "http"

	// The generated Let's Encrypt config (repo-relative) and where the container sees it — the
	// value hsctl puts in CADDY_CONFIG. Absent in private-CA mode.
	leConfigFile      = "caddy/generated/Caddyfile"
	leConfigInCaddy   = "/etc/caddy/generated/Caddyfile"
	defaultConfigPath = "/etc/caddy/Caddyfile"
	// leImageRepo names the locally built caddy+DNS-plugin image (tagged with the provider).
	leImageRepo = "homeserver-caddy"
	// leStagingCA is Let's Encrypt's staging directory: untrusted certs, but generous rate limits —
	// for proving the DNS/challenge plumbing works before spending real issuances on it.
	leStagingCA = "https://acme-staging-v02.api.letsencrypt.org/directory"
	// lePort is where the domain sites listen. It's Caddy's default HTTPS port, published by
	// caddy/docker-compose.yml as HOME_HTTPS — which must therefore stay 443.
	lePort = 443
)

// dnsProviders are caddy-dns plugins configured by ONE api token, which is all the generated
// Caddyfile passes (`dns <provider> {env.ACME_DNS_TOKEN}`). Any other github.com/caddy-dns/<name>
// module can be typed in too, but one that needs several credentials won't work with this
// single-token wiring. Shown as suggestions in the dashboard.
var dnsProviders = []string{
	"cloudflare", "duckdns", "hetzner", "desec", "digitalocean", "gandi", "godaddy",
	"linode", "vultr", "netlify", "vercel", "dnsimple", "ionos", "infomaniak", "njalla",
}

// app is one built-in service as the Let's Encrypt config needs it: where to proxy, and any
// extra directives its site carries in the private-CA Caddyfile — the generated site must behave
// the same. KEEP IN STEP with caddy/Caddyfile. The upstream is the env placeholder resolved by
// caddy/docker-compose.yml, so the two configs can't drift. An app you add yourself gets its
// site from a *.caddy file in caddy/sites.d/ (imported by both configs) — docs/adding-an-app.md.
type app struct {
	Key      string // services.json key; "home" is the dashboard (the bare domain)
	Upstream string // Caddy env placeholder for the upstream
	Extra    string // extra directives, one tab in, newline-terminated
}

var apps = []app{
	{Key: "home", Upstream: "{$HOME_UPSTREAM}"},
	{Key: "vault", Upstream: "{$VAULT_UPSTREAM}"},
	{Key: "cloud", Upstream: "{$CLOUD_UPSTREAM}",
		Extra: "\tredir /.well-known/carddav /remote.php/dav 301\n" +
			"\tredir /.well-known/caldav /remote.php/dav 301\n" +
			"\tredir /.well-known/webfinger /index.php/.well-known/webfinger 301\n" +
			"\tredir /.well-known/nodeinfo /index.php/.well-known/nodeinfo 301\n" +
			"\theader Strict-Transport-Security \"max-age=15552000; includeSubDomains\"\n"},
	{Key: "pihole", Upstream: "{$PIHOLE_UPSTREAM}", Extra: "\tredir / /admin/ 302\n"},
	{Key: "pdf", Upstream: "{$STIRLING_UPSTREAM}"},
	{Key: "tools", Upstream: "{$ITTOOLS_UPSTREAM}"},
	{Key: "image", Upstream: "{$IMAGETOOLS_UPSTREAM}"},
}

// leHost is one generated domain site.
type leHost struct {
	Key      string
	Host     string // vault.home.example.com (the dashboard: the bare domain)
	Upstream string
	Extra    string
}

// leHostsFor lists the domain sites for a config: one per app, named after the matching
// services.json tile's subdomain (so renaming a tile's `host` renames its site) — or the key
// itself when the tile is absent.
func leHostsFor(c Config, svcs []Service) []leHost {
	sub := map[string]string{}
	for _, s := range svcs {
		sub[s.Key] = s.Subdomain()
	}
	var out []leHost
	for _, a := range apps {
		h := leHost{Key: a.Key, Upstream: a.Upstream, Extra: a.Extra, Host: c.Domain}
		if a.Key != "home" {
			label := sub[a.Key]
			if label == "" {
				label = a.Key
			}
			h.Host = label + "." + c.Domain
		}
		out = append(out, h)
	}
	return out
}

// hostFor returns the domain hostname of one app key (e.g. "vault"), or "" if not generated.
func hostFor(hosts []leHost, key string) string {
	for _, h := range hosts {
		if h.Key == key {
			return h.Host
		}
	}
	return ""
}

// leEnabled is the one place that decides which mode Caddy is in.
func (c Config) leEnabled() bool { return c.LetsEncrypt && c.Domain != "" }

// leUsesDNS reports whether the DNS challenge (and so the plugin image) is in use.
func (c Config) leUsesDNS() bool { return c.leEnabled() && c.ACMEChallenge == challengeDNS }

// leImage is the tag of the plugin build for this config's DNS provider.
func (c Config) leImage() string { return leImageRepo + ":" + c.DNSProvider }

// dashboardURL is where the dashboard lives in the current mode.
func (c Config) dashboardURL() string {
	if c.leEnabled() {
		return "https://" + c.Domain
	}
	return "https://" + c.ServerIP
}

// appURL is a tile's address in the current mode.
func (c Config) appURL(s Service) string {
	if c.leEnabled() {
		return s.DomainURL(c.Domain)
	}
	return s.URL(c.ServerIP)
}

// ---- validation ----------------------------------------------------------------

var (
	// A registrable DNS name: lowercase labels, at least one dot, alphabetic TLD.
	domainRe = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$`)
	// A caddy-dns module name (it's spliced into a `go get`-style path, so keep it strict).
	providerRe = regexp.MustCompile(`^[a-z0-9-]+$`)
	// TLDs that are never public: Let's Encrypt can't issue for them, so fail early.
	privateTLDs = map[string]bool{"local": true, "lan": true, "home": true, "internal": true,
		"localhost": true, "test": true, "invalid": true, "example": true, "arpa": true, "localdomain": true}
)

// validateTLS checks the Let's Encrypt settings are usable BEFORE anything is written, so a typo
// can't leave Caddy with a broken config or burn an issuance against the rate limits.
func validateTLS(c Config) error {
	if !c.LetsEncrypt {
		return nil
	}
	d := strings.ToLower(strings.TrimSpace(c.Domain))
	if d == "" {
		return fmt.Errorf("a domain is required to use Let's Encrypt (e.g. home.example.com)")
	}
	if !domainRe.MatchString(d) || net.ParseIP(d) != nil {
		return fmt.Errorf("%q isn't a valid domain name — use the bare name, lowercase, no https:// or port (e.g. home.example.com)", c.Domain)
	}
	labels := strings.Split(d, ".")
	if privateTLDs[labels[len(labels)-1]] || strings.HasSuffix(d, "example.com") ||
		strings.HasSuffix(d, "example.net") || strings.HasSuffix(d, "example.org") {
		return fmt.Errorf("%q isn't a public domain — Let's Encrypt only issues certificates for a real, registered domain you own", d)
	}
	e := strings.TrimSpace(c.ACMEEmail)
	at := strings.Index(e, "@")
	if at < 1 || !strings.Contains(e[at:], ".") || strings.ContainsAny(e, " \t") ||
		strings.HasSuffix(e, "@example.com") || strings.HasSuffix(e, "@example.org") {
		return fmt.Errorf("a real admin email is required (Let's Encrypt sends expiry warnings to it) — %q won't do", c.ACMEEmail)
	}
	switch c.ACMEChallenge {
	case challengeDNS:
		if !providerRe.MatchString(c.DNSProvider) {
			return fmt.Errorf("DNS provider %q isn't a plugin name — use the github.com/caddy-dns/<name> module name, e.g. cloudflare", c.DNSProvider)
		}
		if strings.TrimSpace(c.DNSToken) == "" {
			return fmt.Errorf("the dns challenge needs your %s API token (it creates the _acme-challenge TXT record for you)", c.DNSProvider)
		}
	case challengeHTTP:
	default:
		return fmt.Errorf("challenge must be %q (LAN-only, needs a DNS API token) or %q (port 80 reachable from the internet)", challengeDNS, challengeHTTP)
	}
	return nil
}

// ---- rendering -----------------------------------------------------------------

// renderLEConfig is the complete Let's Encrypt-mode Caddyfile: global options, a shared `tls`
// snippet (the challenge and CA choice), one https://<name> site per app, http->https redirects
// for the names, a plain info page on :80 for anyone who still types the IP, and the user's own
// sites.d. Upstreams come from the same env the private-CA Caddyfile uses.
func renderLEConfig(c Config, hosts []leHost) string {
	var b strings.Builder
	b.WriteString("# GENERATED by hsctl — do not edit, it is rewritten on every `hsctl up` / `hsctl letsencrypt …` /\n")
	b.WriteString("# dashboard apply. Caddy loads THIS instead of ../Caddyfile while Let's Encrypt is on (CADDY_CONFIG\n")
	b.WriteString("# in ../.env); sites of your own go in ../sites.d/*.caddy, imported at the bottom.\n#\n")
	fmt.Fprintf(&b, "# Let's Encrypt mode: each app at https://<app>.%s on :%d with a publicly trusted certificate.\n", c.Domain, lePort)
	b.WriteString("# The private CA is not used. Challenge: " + c.ACMEChallenge)
	if c.ACMEStaging {
		b.WriteString(" — STAGING CA (test certificates, NOT trusted by browsers)")
	}
	b.WriteString(".\n\n")
	fmt.Fprintf(&b, "{\n\temail {$ACME_EMAIL}\n"+
		"\t# A client that sends no SNI (someone typing the bare IP) gets the dashboard's certificate.\n"+
		"\tdefault_sni %s\n"+
		"\t# The redirects are explicit per name below; :80 otherwise serves the info page.\n"+
		"\tauto_https disable_redirects\n}\n\n", c.Domain)

	var tlsLines []string
	if c.ACMEChallenge == challengeDNS {
		tlsLines = append(tlsLines,
			"\t\tdns "+c.DNSProvider+" {env.ACME_DNS_TOKEN}",
			"\t\t# Check the TXT record via public resolvers: a LAN resolver (Pi-hole) that answers for this",
			"\t\t# domain locally would otherwise make the propagation check fail.",
			"\t\tresolvers 1.1.1.1 9.9.9.9")
	}
	if c.ACMEStaging {
		tlsLines = append(tlsLines,
			"\t\t# Staging CA: browsers won't trust these — for checking the setup without hitting rate limits.",
			"\t\tca "+leStagingCA)
	}
	useSnippet := len(tlsLines) > 0
	if useSnippet {
		b.WriteString("(letsencrypt) {\n\ttls {\n" + strings.Join(tlsLines, "\n") + "\n\t}\n}\n\n")
	}
	// Plain http on the bare IP: say where the server is. (Let's Encrypt's http challenge is
	// answered before any site route, so this doesn't get in its way.)
	fmt.Fprintf(&b, ":80 {\n\theader Content-Type \"text/html; charset=utf-8\"\n"+
		"\trespond `<!doctype html><meta name=viewport content=\"width=device-width,initial-scale=1\">"+
		"<title>Home server</title><h1>Home server</h1><p>This server is at <a href=\"https://%[1]s\"><b>https://%[1]s</b></a>. "+
		"If that doesn't open on this device, the name has to point at %[2]s on your network — ask your admin.</p>` 200\n}\n\n",
		c.Domain, c.ServerIP)
	for _, h := range hosts {
		fmt.Fprintf(&b, "https://%s {\n", h.Host)
		if useSnippet {
			b.WriteString("\timport letsencrypt\n")
		}
		fmt.Fprintf(&b, "\treverse_proxy %s\n%s}\n\n", h.Upstream, h.Extra)
		fmt.Fprintf(&b, "http://%s {\n\tredir https://{host}{uri} 308\n}\n\n", h.Host)
	}
	b.WriteString("# Sites of your own (the private-CA Caddyfile imports this folder too).\n")
	b.WriteString("import /etc/caddy/sites.d/*.caddy\n")
	return b.String()
}

// Pi-hole custom.list gets a managed block of "<ip> <host>" lines, so LAN devices using Pi-hole
// as their DNS resolve the domain names to the server without any router or public-DNS change.
// The markers let hsctl rewrite its own block on every apply while leaving anything the user
// added by hand alone.
const (
	piholeBlockBegin = "# >>> hsctl: Let's Encrypt domain names (managed — rewritten on every apply) >>>"
	piholeBlockEnd   = "# <<< hsctl: Let's Encrypt domain names <<<"
)

// setManagedBlock replaces (or appends, or removes when body is empty) the marked block in text.
func setManagedBlock(text, begin, end, body string) string {
	var head, tail string
	if i := strings.Index(text, begin); i >= 0 {
		head = text[:i]
		tail = text[i:]
		if j := strings.Index(tail, end); j >= 0 {
			tail = tail[j+len(end):]
			tail = strings.TrimPrefix(tail, "\n")
		} else {
			tail = "" // begin without end: treat the rest as ours
		}
	} else {
		head = text
	}
	if body == "" {
		return strings.TrimRight(head, "\n") + "\n" + tail
	}
	head = strings.TrimRight(head, "\n")
	if head != "" {
		head += "\n"
	}
	return head + begin + "\n" + body + end + "\n" + tail
}

// piholeRecords renders the hosts-file lines for the domain sites.
func piholeRecords(ip string, hosts []leHost) string {
	var b strings.Builder
	for _, h := range hosts {
		fmt.Fprintf(&b, "%s %s\n", ip, h.Host)
	}
	return b.String()
}

// vwDomainFor is Vaultwarden's DOMAIN (the URL clients use — it shapes emailed links and
// passkeys): the domain site in Let's Encrypt mode, else the IP:port form.
func vwDomainFor(c Config, hosts []leHost, vaultPort int) string {
	if c.leEnabled() {
		if h := hostFor(hosts, "vault"); h != "" {
			return "https://" + h
		}
	}
	return fmt.Sprintf("https://%s:%d", c.ServerIP, vaultPort)
}

// ncTrustedDomainsFor is Nextcloud's trusted-domains list (space separated). Nextcloud refuses
// requests for any other Host. The IP stays in the list in Let's Encrypt mode too: harmless, and
// it saves a config churn when switching back.
func ncTrustedDomainsFor(c Config, hosts []leHost) string {
	out := c.ServerIP
	if c.leEnabled() {
		if h := hostFor(hosts, "cloud"); h != "" {
			out += " " + h
		}
	}
	return out
}

// onboardingFor adapts ONBOARDING.md (served at /help) to the current mode. Private-CA mode is
// the file as written, with SERVER_IP filled in. In Let's Encrypt mode the private-CA passages
// (between <!-- private-ca --> … <!-- /private-ca -->) are dropped, the Let's Encrypt passages
// (written as an HTML comment, <!-- letsencrypt … /letsencrypt -->, so they're invisible
// otherwise) are unwrapped, the IP:port addresses become the domain names, and the numbered
// steps close up.
func onboardingFor(src string, c Config, hosts []leHost) string {
	if !c.leEnabled() {
		return strings.ReplaceAll(src, "SERVER_IP", c.ServerIP)
	}
	src = stripMarked(src, "<!-- private-ca -->", "<!-- /private-ca -->")
	r := strings.NewReplacer(
		"<!-- letsencrypt\n", "", "\n/letsencrypt -->", "",
		"https://SERVER_IP:8443", "https://"+hostFor(hosts, "vault"),
		"https://SERVER_IP:8444", "https://"+hostFor(hosts, "cloud"),
		"https://SERVER_IP:8445", "https://"+hostFor(hosts, "pihole"),
		"http://SERVER_IP/", "https://"+c.Domain+"/",
		"https://SERVER_IP", "https://"+c.Domain,
		"SERVER_IP", c.ServerIP,
		"## 2. ", "## 1. ", "## 3. ", "## 2. ",
	)
	return r.Replace(src)
}

// stripMarked removes every begin…end passage (markers included) from text.
func stripMarked(text, begin, end string) string {
	for {
		i := strings.Index(text, begin)
		if i < 0 {
			return text
		}
		j := strings.Index(text[i:], end)
		if j < 0 {
			return text[:i]
		}
		text = text[:i] + text[i+j+len(end):]
	}
}

// writeIfChanged writes content to path (atomically) only when it differs; reports whether it did.
func writeIfChanged(path, content string, perm os.FileMode) (bool, error) {
	if b, err := os.ReadFile(path); err == nil && string(b) == content {
		return false, nil
	}
	return true, writeFileAtomic(path, content, perm)
}

// setEnvKeys rewrites several KEY=VALUE lines of an env file in one pass (preserving comments,
// order and other keys; appending what's missing — under header, once, when given).
func setEnvKeys(path string, pairs [][2]string, header string) (bool, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	orig := string(b)
	lines := strings.Split(strings.TrimRight(orig, "\n"), "\n")
	var missing [][2]string
	for _, kv := range pairs {
		found := false
		for i, l := range lines {
			if strings.HasPrefix(l, kv[0]+"=") {
				lines[i], found = kv[0]+"="+kv[1], true
				break
			}
		}
		if !found {
			missing = append(missing, kv)
		}
	}
	if len(missing) > 0 {
		if header != "" && !strings.Contains(orig, header) {
			lines = append(lines, "", header)
		}
		for _, kv := range missing {
			lines = append(lines, kv[0]+"="+kv[1])
		}
	}
	out := strings.Join(lines, "\n") + "\n"
	if out == orig {
		return false, nil
	}
	return true, writeFile0600(path, out)
}

const caddyEnvLEHeader = "# Optional public domain + Let's Encrypt — managed by `hsctl letsencrypt` / the dashboard."

// renderTLS writes everything on disk that follows from the HTTPS mode: the keys in caddy/.env
// (CADDY_CONFIG / CADDY_IMAGE only in Let's Encrypt mode), the generated Let's Encrypt config
// (removed in private-CA mode), and Pi-hole's local records. Pure file writes, idempotent, no
// docker — `hsctl setup` calls it before the stack exists, `hsctl up` calls it so the config
// always matches setup.conf (and a restored box regenerates it), and applyTLS calls it before
// reloading Caddy. It deliberately does NOT touch Vaultwarden's or Nextcloud's own .env: those
// change only on an explicit enable/disable (syncAppEnvs), never as a side effect of `up`.
func renderTLS(repo string, c Config, w io.Writer) error {
	caddyEnv := filepath.Join(repo, "caddy", ".env")
	if !fileExists(caddyEnv) {
		return fmt.Errorf("%s not found — run: hsctl setup", caddyEnv)
	}
	on := c.leEnabled()
	pairs := [][2]string{
		{"DOMAIN", c.Domain},
		{"LETSENCRYPT", boolStr(on, "true", "false")},
		{"ACME_CHALLENGE", c.ACMEChallenge},
		{"ACME_DNS_PROVIDER", c.DNSProvider},
		{"ACME_STAGING", boolStr(c.ACMEStaging, "true", "false")},
		{"ACME_EMAIL", c.ACMEEmail},
	}
	if c.DNSToken != "" {
		// '$' doubled so compose interpolation passes the literal token into the container.
		pairs = append(pairs, [2]string{"ACME_DNS_TOKEN", escapeDollarsForCompose(c.DNSToken)})
	}
	// Private-CA mode is the committed Caddyfile with the stock image — no overrides at all.
	var drop []string
	if on {
		pairs = append(pairs, [2]string{"CADDY_CONFIG", leConfigInCaddy})
	} else {
		drop = append(drop, "CADDY_CONFIG")
	}
	if c.leUsesDNS() {
		pairs = append(pairs, [2]string{"CADDY_IMAGE", c.leImage()})
	} else {
		drop = append(drop, "CADDY_IMAGE")
	}
	if len(drop) > 0 {
		if _, err := removeEnvKeys(caddyEnv, drop...); err != nil {
			return err
		}
	}
	if changed, err := setEnvKeys(caddyEnv, pairs, caddyEnvLEHeader); err != nil {
		return err
	} else if changed {
		fmt.Fprintln(w, "updated caddy/.env")
	}

	hosts := leHostsFor(c, LoadServices(repo))
	gen := filepath.Join(repo, leConfigFile)
	if on {
		if err := os.MkdirAll(filepath.Dir(gen), 0755); err != nil {
			return err
		}
		if changed, err := writeIfChanged(gen, renderLEConfig(c, hosts), 0644); err != nil {
			return err
		} else if changed {
			fmt.Fprintf(w, "wrote %s\n", leConfigFile)
		}
	} else if fileExists(gen) {
		if err := os.Remove(gen); err != nil {
			return err
		}
		fmt.Fprintf(w, "removed %s (back to caddy/Caddyfile)\n", leConfigFile)
	}

	// Pi-hole records. custom.list is bind-mounted into the container as a single FILE, so it
	// must be rewritten IN PLACE (same inode): an atomic rename would leave the container
	// looking at the old, now-orphaned file until it's recreated.
	list := filepath.Join(repo, "pihole", "custom.list")
	var body string
	if on {
		body = piholeRecords(c.ServerIP, hosts)
	}
	old, _ := os.ReadFile(list)
	if cur := string(old); cur != "" || on {
		if cur == "" {
			cur = "# Pi-hole local DNS records — hosts-file format: \"<IP> <hostname>\".\n"
		}
		if next := setManagedBlock(cur, piholeBlockBegin, piholeBlockEnd, body); next != cur {
			if err := os.WriteFile(list, []byte(next), 0644); err != nil {
				return err
			}
			fmt.Fprintln(w, "updated pihole/custom.list (local DNS records for the domain names)")
		}
	}
	return nil
}

// ---- apply (files + the running stack) -------------------------------------------

type applyOpts struct {
	Rebuild bool // rebuild the DNS-plugin image even if one exists (new Caddy/plugin version)
}

// ensureCaddyImage builds the caddy+DNS-plugin image if it isn't present (or if rebuild).
func ensureCaddyImage(repo string, c Config, rebuild bool, w io.Writer) error {
	img := c.leImage()
	if !rebuild && dockerCmd(repo, "image", "inspect", img).Run() == nil {
		return nil
	}
	fmt.Fprintf(w, "== building Caddy with the %s DNS plugin (%s) — the first time takes a few minutes ==\n", c.DNSProvider, img)
	cmd := dockerCmd(filepath.Join(repo, "caddy"), "build", "--pull",
		"--build-arg", "CADDY_DNS_PROVIDER="+c.DNSProvider, "-t", img, ".")
	cmd.Stdout, cmd.Stderr = w, w
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("building %s failed: %w\n(is %q a real github.com/caddy-dns/ module? does the box reach Docker Hub?)", img, err, c.DNSProvider)
	}
	return nil
}

// leNeedsImageBuild reports whether starting the stack would first have to build the plugin image.
func leNeedsImageBuild(repo string, c Config) bool {
	return c.leUsesDNS() && dockerCmd(repo, "image", "inspect", c.leImage()).Run() != nil
}

// activeCaddyConfig is the config path Caddy runs with (CADDY_CONFIG in caddy/.env, else the
// committed Caddyfile) — the same value docker-compose.yml passes to `caddy run`.
func activeCaddyConfig(repo string) string {
	if kv, err := readKV(filepath.Join(repo, "caddy", ".env")); err == nil && kv["CADDY_CONFIG"] != "" {
		return kv["CADDY_CONFIG"]
	}
	return defaultConfigPath
}

// reloadCaddy asks the running Caddy to gracefully load its (bind-mounted) config — no
// downtime. Errors are returned for the caller to show.
func reloadCaddy(repo string) error {
	out, err := dockerCombined(repo, "exec", "caddy", "caddy", "reload", "--config", activeCaddyConfig(repo))
	if err != nil {
		return fmt.Errorf("caddy reload: %v\n%s", err, strings.TrimSpace(out))
	}
	return nil
}

// applyTLS is the explicit switch (enable / disable / re-apply): validate, save setup.conf,
// render the files, build the plugin image if the dns challenge needs it, then — if the stack is
// up — prove the new Caddy config parses with the image Caddy will run BEFORE touching the live
// container, and reload. A config that fails validation is rolled back completely, so a typo
// can't take the proxy (and this dashboard) down. Progress streams to w (the CLI's stdout, or
// the dashboard's log).
func applyTLS(repo string, c Config, o applyOpts, w io.Writer) error {
	c.Normalize()
	if err := validateTLS(c); err != nil {
		return err
	}
	caddyDir := filepath.Join(repo, "caddy")
	caddyEnv, err := readKV(filepath.Join(caddyDir, ".env"))
	if err != nil {
		return fmt.Errorf("caddy/.env not found — run: hsctl setup")
	}
	if c.leEnabled() {
		if p := atoiDef(caddyEnv["HOME_HTTPS"], lePort); p != lePort {
			return fmt.Errorf("the domain sites listen on :%d, but caddy/.env has HOME_HTTPS=%d so Caddy doesn't publish %d — set HOME_HTTPS=%d first", lePort, p, lePort, lePort)
		}
	}

	// Snapshot every file this apply may touch, so a config Caddy rejects can be undone
	// completely — on disk as well as in the proxy. Leaving a rejected token in caddy/.env would
	// otherwise crash-loop Caddy on the next `hsctl up`, long after the failed click.
	snap := snapshotFiles(repo, confFile, "caddy/.env", leConfigFile, "pihole/custom.list",
		"vaultwarden/.env", "nextcloud/.env")
	rollback := func() {
		if err := snap.restore(); err != nil {
			fmt.Fprintln(w, "warning: rolling the files back failed:", err)
			return
		}
		fmt.Fprintln(w, "rolled the config files back to their previous state")
	}

	if err := c.Save(repo); err != nil {
		return err
	}
	fmt.Fprintln(w, "saved", confFile)
	if err := renderTLS(repo, c, w); err != nil {
		return err
	}
	hosts := leHostsFor(c, LoadServices(repo))
	vwChanged, err := syncAppEnvs(repo, c, hosts, w)
	if err != nil {
		return err
	}

	if c.leUsesDNS() {
		if err := ensureCaddyImage(repo, c, o.Rebuild, w); err != nil {
			rollback()
			return err
		}
	}

	if containerState(repo, "caddy") == "" {
		fmt.Fprintln(w, "\nCaddy isn't running yet — everything is written; it takes effect on the next `hsctl up`.")
		printLENext(w, c, hosts)
		return nil
	}

	// Validate with the exact image + environment the service gets from compose (`run` reuses
	// them, without publishing ports), so a bad plugin name or Caddyfile is caught here rather
	// than crash-looping the live proxy.
	fmt.Fprintln(w, "== checking the new Caddy config ==")
	if err := ensureEdgeNetwork(); err != nil {
		return err
	}
	if out, err := dockerCombined(caddyDir, "compose", "run", "--rm", "--no-deps", "-T", "caddy",
		"caddy", "validate", "--config", activeCaddyConfig(repo)); err != nil {
		rollback()
		return fmt.Errorf("the new Caddy config is INVALID — nothing was applied to the running proxy:\n%s", strings.TrimSpace(out))
	}
	fmt.Fprintln(w, "config OK")

	fmt.Fprintln(w, "== applying to Caddy ==")
	// `up -d` recreates the container when its image, command or environment changed (a mode
	// switch, the plugin build, a new token) and is a no-op otherwise; the reload then picks up
	// an edited generated config either way.
	if out, err := dockerCombined(caddyDir, "compose", "up", "-d"); err != nil {
		return fmt.Errorf("compose up caddy: %v\n%s", err, strings.TrimSpace(out))
	}
	if err := reloadCaddy(repo); err != nil {
		return err
	}
	fmt.Fprintln(w, "Caddy reloaded")

	if vwChanged && containerState(repo, "vaultwarden") == "running" {
		fmt.Fprintln(w, "== restarting Vaultwarden with its new DOMAIN (a few seconds) ==")
		if out, err := dockerCombined(filepath.Join(repo, "vaultwarden"), "compose", "up", "-d"); err != nil {
			fmt.Fprintf(w, "warning: vaultwarden didn't recreate: %v\n%s\n", err, strings.TrimSpace(out))
		}
	}
	if c.leEnabled() && containerState(repo, "nextcloud-app") == "running" {
		if h := hostFor(hosts, "cloud"); h != "" {
			if err := nextcloudTrustDomain(repo, h); err != nil {
				fmt.Fprintf(w, "warning: couldn't add %s to Nextcloud's trusted domains (add it by hand: Settings → Overview): %v\n", h, err)
			} else {
				fmt.Fprintf(w, "Nextcloud trusts %s\n", h)
			}
		}
	}
	if containerState(repo, "pihole") == "running" {
		if _, err := dockerCombined(repo, "exec", "pihole", "pihole", "reloaddns"); err != nil {
			fmt.Fprintln(w, "warning: pihole reloaddns failed — its local records refresh on the next restart")
		} else {
			fmt.Fprintln(w, "Pi-hole reloaded its local DNS records")
		}
	}
	printLENext(w, c, hosts)
	return nil
}

// syncAppEnvs updates the apps' own idea of their public URL — Vaultwarden's DOMAIN and
// Nextcloud's trusted domains — in their .env files. Only on an explicit change of the domain
// settings (enable/disable/apply, or a setup run that changed them), never as a side effect of
// `up`: those keys are also legitimately hand-edited (the sandbox does). Reports whether
// Vaultwarden's changed, since that one needs a container recreate to take effect.
func syncAppEnvs(repo string, c Config, hosts []leHost, w io.Writer) (vwChanged bool, err error) {
	vaultPort := 8443
	if kv, err := readKV(filepath.Join(repo, "caddy", ".env")); err == nil {
		vaultPort = atoiDef(kv["VAULT_HTTPS"], vaultPort)
	}
	vwEnv := filepath.Join(repo, "vaultwarden", ".env")
	if fileExists(vwEnv) {
		if vwChanged, err = setEnvKeys(vwEnv, [][2]string{{"VW_DOMAIN", vwDomainFor(c, hosts, vaultPort)}}, ""); err != nil {
			return false, err
		} else if vwChanged {
			fmt.Fprintln(w, "updated vaultwarden/.env (VW_DOMAIN)")
		}
	}
	ncEnv := filepath.Join(repo, "nextcloud", ".env")
	if fileExists(ncEnv) {
		if changed, err := setEnvKeys(ncEnv, [][2]string{{"NC_TRUSTED_DOMAINS", ncTrustedDomainsFor(c, hosts)}}, ""); err != nil {
			return false, err
		} else if changed {
			fmt.Fprintln(w, "updated nextcloud/.env (NC_TRUSTED_DOMAINS)")
		}
	}
	return vwChanged, nil
}

// leStateChanged reports whether two configs differ in anything the HTTPS mode depends on.
func leStateChanged(a, b Config) bool {
	return a.leEnabled() != b.leEnabled() || a.Domain != b.Domain || a.ACMEChallenge != b.ACMEChallenge ||
		a.DNSProvider != b.DNSProvider || a.DNSToken != b.DNSToken || a.ACMEStaging != b.ACMEStaging ||
		a.ACMEEmail != b.ACMEEmail
}

// printLENext tells the operator what still depends on them (DNS records, waiting for issuance).
func printLENext(w io.Writer, c Config, hosts []leHost) {
	if !c.leEnabled() {
		fmt.Fprintf(w, "\nPrivate-CA mode — the apps are at https://%s:<port>; dashboard: https://%s (install the certificate once per device).\n", c.ServerIP, c.ServerIP)
		return
	}
	fmt.Fprintf(w, "\nLet's Encrypt is ON for %s (%s challenge", c.Domain, c.ACMEChallenge)
	if c.ACMEStaging {
		fmt.Fprint(w, ", STAGING — test certificates browsers won't trust")
	}
	fmt.Fprintln(w, "). The private CA is no longer used: the IP:port addresses are gone, and there is nothing to install on devices.")
	fmt.Fprintln(w, "Caddy requests the certificates in the background; the first ones take about a minute.")
	fmt.Fprintf(w, "\nDashboard: https://%s/admin\n", c.Domain)
	fmt.Fprintln(w, "\nThese names must resolve to the server on your network:")
	for _, h := range hosts {
		fmt.Fprintf(w, "  %-40s -> %s\n", h.Host, c.ServerIP)
	}
	fmt.Fprintln(w, "  • Pi-hole users: done — hsctl put them in pihole/custom.list.")
	fmt.Fprintln(w, "  • Otherwise: add them in your router's local DNS, or as public A records pointing at that LAN IP")
	fmt.Fprintf(w, "    (a wildcard *.%s works for the subdomains).\n", c.Domain)
	if c.ACMEChallenge == challengeHTTP {
		fmt.Fprintf(w, "  • http challenge: the names must ALSO resolve to your PUBLIC IP from the internet, with port 80\n"+
			"    forwarded to %s. (The dns challenge avoids all of that.)\n", c.ServerIP)
	}
	fmt.Fprintln(w, "\nIf a name doesn't open, you can always go back from the server itself:  hsctl letsencrypt disable")
	fmt.Fprintln(w, "Check progress:  hsctl letsencrypt status     (or the dashboard's Domain & HTTPS page)")
	fmt.Fprintln(w, "Caddy's log:     hsctl letsencrypt logs")
}

// nextcloudTrustDomain adds host to a RUNNING Nextcloud's trusted_domains via occ. The env var
// only seeds the list on first install; afterwards config.php is the truth, so this is the
// only way a post-setup enable actually works. Idempotent.
func nextcloudTrustDomain(repo, host string) error {
	occ := func(args ...string) (string, error) {
		return dockerOut(repo, append([]string{"exec", "-u", "www-data", "nextcloud-app", "php", "occ"}, args...)...)
	}
	out, err := occ("config:system:get", "trusted_domains")
	if err != nil {
		return err
	}
	n := 0
	for _, l := range strings.Split(out, "\n") {
		if strings.TrimSpace(l) == host {
			return nil
		}
		if strings.TrimSpace(l) != "" {
			n++
		}
	}
	_, err = occ("config:system:set", "trusted_domains", strconv.Itoa(n), "--value="+host)
	return err
}

// ---- status ----------------------------------------------------------------------

// certStatus is what one domain site is currently serving, seen from the box itself.
type certStatus struct {
	Host    string
	URL     string
	OK      bool   // a valid, publicly trusted certificate for this name
	Issuer  string // who signed it
	Expires string // "in 61 days" ("" if nothing served)
	Note    string // what's wrong, in plain words, when !OK
}

// probeCert connects to Caddy with the domain name as SNI and reports the certificate it
// serves — the same thing a browser would get, minus the DNS lookup (so it works before the
// names resolve anywhere). Caddy publishes on every interface, so loopback is tried first and
// the configured server IP second. Trust is checked against the box's own root store, which is
// how "Let's Encrypt (trusted)" is told apart from staging or the private CA.
func probeCert(serverIP string, port int, host string, timeout time.Duration) certStatus {
	st := certStatus{Host: host, URL: "https://" + host}
	d := net.Dialer{Timeout: timeout}
	var raw net.Conn
	var err error
	for _, ip := range []string{"127.0.0.1", serverIP} {
		if raw, err = d.Dial("tcp", net.JoinHostPort(ip, strconv.Itoa(port))); err == nil {
			break
		}
	}
	if err != nil {
		st.Note = "can't reach Caddy on :" + strconv.Itoa(port) + " — is the stack up?"
		return st
	}
	defer raw.Close()
	tc := tls.Client(raw, &tls.Config{ServerName: host, InsecureSkipVerify: true}) //nolint:gosec // we verify below, and want the cert even when untrusted
	_ = tc.SetDeadline(time.Now().Add(timeout))
	if err := tc.Handshake(); err != nil {
		st.Note = "no certificate for this name yet — Caddy is still requesting it, or the request failed (see the Caddy log)"
		return st
	}
	certs := tc.ConnectionState().PeerCertificates
	if len(certs) == 0 {
		st.Note = "no certificate served"
		return st
	}
	leaf := certs[0]
	st.Issuer = leaf.Issuer.CommonName
	if len(leaf.Issuer.Organization) > 0 {
		st.Issuer = leaf.Issuer.Organization[0] + " (" + leaf.Issuer.CommonName + ")"
	}
	days := int(time.Until(leaf.NotAfter).Hours() / 24)
	switch {
	case days < 0:
		st.Expires = "EXPIRED"
	case days == 0:
		st.Expires = "today"
	default:
		st.Expires = fmt.Sprintf("in %d days", days)
	}
	opts := x509.VerifyOptions{DNSName: host, Intermediates: x509.NewCertPool()}
	for _, ic := range certs[1:] {
		opts.Intermediates.AddCert(ic)
	}
	if _, err := leaf.Verify(opts); err != nil {
		switch {
		case strings.Contains(st.Issuer, "STAGING"):
			st.Note = "staging certificate — the plumbing works; turn staging off to get a trusted one"
		case strings.Contains(st.Issuer, "Caddy Local Authority"):
			st.Note = "private-CA certificate is being served for this name — the domain site isn't active (is the config applied?)"
		case leaf.VerifyHostname(host) != nil:
			st.Note = "certificate is for " + strings.Join(leaf.DNSNames, ", ") + ", not this name"
		default:
			st.Note = "not trusted: " + err.Error()
		}
		return st
	}
	st.OK = true
	return st
}

// leStatuses probes every domain site, in parallel so the page costs one handshake's time.
func leStatuses(c Config, hosts []leHost) []certStatus {
	out := make([]certStatus, len(hosts))
	var wg sync.WaitGroup
	for i, h := range hosts {
		wg.Add(1)
		go func(i int, host string) {
			defer wg.Done()
			out[i] = probeCert(c.ServerIP, lePort, host, 3*time.Second)
		}(i, h.Host)
	}
	wg.Wait()
	return out
}

// fileSnapshot remembers a set of files (content + mode, or their absence) so a failed apply can
// put them back exactly. custom.list is restored in place (same inode — it's bind-mounted as a
// single file into the Pi-hole container); everything else via an atomic rename.
type fileSnapshot struct {
	files map[string]*snapEntry // absolute path -> entry (nil = didn't exist)
}

type snapEntry struct {
	data []byte
	mode os.FileMode
}

func snapshotFiles(repo string, rels ...string) fileSnapshot {
	snap := fileSnapshot{files: map[string]*snapEntry{}}
	for _, rel := range rels {
		p := filepath.Join(repo, rel)
		st, err := os.Stat(p)
		if err != nil {
			snap.files[p] = nil
			continue
		}
		b, err := os.ReadFile(p)
		if err != nil {
			snap.files[p] = nil
			continue
		}
		snap.files[p] = &snapEntry{data: b, mode: st.Mode().Perm()}
	}
	return snap
}

func (s fileSnapshot) restore() error {
	var firstErr error
	for p, e := range s.files {
		var err error
		switch {
		case e == nil:
			if fileExists(p) {
				err = os.Remove(p)
			}
		case filepath.Base(p) == "custom.list":
			err = os.WriteFile(p, e.data, e.mode)
		default:
			err = writeFileAtomic(p, string(e.data), e.mode)
		}
		if err != nil && firstErr == nil {
			firstErr = fmt.Errorf("%s: %w", p, err)
		}
	}
	return firstErr
}

// ---- CLI -------------------------------------------------------------------------

func letsencryptCmd() *cobra.Command {
	root := &cobra.Command{
		Use:     "letsencrypt",
		Aliases: []string{"le"},
		Short:   "Optional: replace the private CA with Let's Encrypt certificates on a public domain",
		Long: "Switches Caddy from the private CA (https://<ip>:<port>, certificate installed per device) to\n" +
			"https://<app>.<domain> with Let's Encrypt certificates that every device already trusts. The default\n" +
			"'dns' challenge keeps the box LAN-only (nothing port-forwarded); it needs your DNS provider's API\n" +
			"token. The names must resolve to this server on your network. See docs/setup.md.",
	}

	enable := &cobra.Command{
		Use:   "enable",
		Short: "Switch to Let's Encrypt (or change its settings) and apply to the running stack",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			repo, err := requireRepoDir()
			if err != nil {
				return err
			}
			c := LoadConfig(repo)
			f := cmd.Flags()
			if f.Changed("domain") {
				c.Domain, _ = f.GetString("domain")
			}
			if f.Changed("email") {
				c.ACMEEmail, _ = f.GetString("email")
			}
			if f.Changed("challenge") {
				c.ACMEChallenge, _ = f.GetString("challenge")
			}
			if f.Changed("dns-provider") {
				c.DNSProvider, _ = f.GetString("dns-provider")
			}
			if f.Changed("dns-token") {
				c.DNSToken, _ = f.GetString("dns-token")
			} else if v := os.Getenv("ACME_DNS_TOKEN"); v != "" {
				c.DNSToken = v
			}
			if f.Changed("staging") {
				c.ACMEStaging, _ = f.GetBool("staging")
			}
			c.LetsEncrypt = true
			c.Normalize()
			// The token is a secret: prompt for it rather than insisting it goes on the command line.
			if c.ACMEChallenge == challengeDNS && c.DNSToken == "" && isTTY() {
				c.DNSToken = ask(c.DNSProvider+" API token (input is echoed)", "")
			}
			rebuild, _ := f.GetBool("rebuild")
			return applyTLS(repo, c, applyOpts{Rebuild: rebuild}, os.Stdout)
		},
	}
	enable.Flags().String("domain", "", "base domain, e.g. home.example.com (apps become vault.<domain>, …)")
	enable.Flags().String("email", "", "contact email for the Let's Encrypt account")
	enable.Flags().String("challenge", "", "dns (LAN-only, needs a DNS API token) or http (port 80 forwarded)")
	enable.Flags().String("dns-provider", "", "caddy-dns plugin for the dns challenge: cloudflare, duckdns, hetzner, …")
	enable.Flags().String("dns-token", "", "that provider's API token (or set $ACME_DNS_TOKEN, or get prompted)")
	enable.Flags().Bool("staging", false, "use Let's Encrypt's staging CA (untrusted test certs, no rate limits)")
	enable.Flags().Bool("rebuild", false, "rebuild the caddy+DNS-plugin image even if it exists")

	disable := &cobra.Command{
		Use:   "disable",
		Short: "Switch back to the private CA at https://<ip>:<port> (the Let's Encrypt settings are kept)",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			repo, err := requireRepoDir()
			if err != nil {
				return err
			}
			c := LoadConfig(repo)
			c.LetsEncrypt = false
			return applyTLS(repo, c, applyOpts{}, os.Stdout)
		},
	}

	apply := &cobra.Command{
		Use:   "apply",
		Short: "Re-render from the saved settings and reload Caddy (e.g. after editing setup.conf)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			repo, err := requireRepoDir()
			if err != nil {
				return err
			}
			rebuild, _ := cmd.Flags().GetBool("rebuild")
			return applyTLS(repo, LoadConfig(repo), applyOpts{Rebuild: rebuild}, os.Stdout)
		},
	}
	apply.Flags().Bool("rebuild", false, "rebuild the caddy+DNS-plugin image (picks up a newer Caddy/plugin)")

	status := &cobra.Command{
		Use:   "status",
		Short: "Show the HTTPS mode and what certificate each domain site is serving",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			repo, err := requireRepoDir()
			if err != nil {
				return err
			}
			c := LoadConfig(repo)
			c.Normalize()
			return leStatusPrint(repo, c, os.Stdout)
		},
	}

	logs := &cobra.Command{
		Use:   "logs",
		Short: "Show Caddy's recent log (certificate requests and their errors show up here)",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			return dockerRun(repoDir(), "logs", "--tail", "120", "caddy")
		},
	}

	root.AddCommand(enable, disable, apply, status, logs)
	return root
}

func leStatusPrint(repo string, c Config, w io.Writer) error {
	if !c.leEnabled() {
		fmt.Fprintln(w, "HTTPS: private CA — apps at https://"+c.ServerIP+":<port>; install the certificate once per device (http://"+c.ServerIP+"/).")
		if c.Domain != "" {
			fmt.Fprintf(w, "(Let's Encrypt settings for %s are saved; switch with: hsctl letsencrypt enable)\n", c.Domain)
		} else {
			fmt.Fprintln(w, "Switch to Let's Encrypt with: hsctl letsencrypt enable --domain home.example.com --email you@… --dns-provider cloudflare")
		}
		return nil
	}
	fmt.Fprintf(w, "HTTPS: Let's Encrypt   domain %s   challenge %s", c.Domain, c.ACMEChallenge)
	if c.ACMEChallenge == challengeDNS {
		fmt.Fprintf(w, " (%s)", c.DNSProvider)
	}
	if c.ACMEStaging {
		fmt.Fprint(w, "   STAGING (untrusted test certs)")
	}
	fmt.Fprintln(w)
	hosts := leHostsFor(c, LoadServices(repo))
	if !fileExists(filepath.Join(repo, leConfigFile)) {
		fmt.Fprintln(w, "note: the generated config is missing — run `hsctl letsencrypt apply` (or `hsctl up`)")
	}
	fmt.Fprintln(w)
	for _, st := range leStatuses(c, hosts) {
		mark := "✔"
		detail := st.Issuer + ", expires " + st.Expires
		if !st.OK {
			mark = "✖"
			detail = st.Note
			if st.Issuer != "" {
				detail = st.Issuer + " — " + st.Note
			}
		}
		fmt.Fprintf(w, "  %s %-40s %s\n", mark, st.URL, detail)
	}
	return nil
}
