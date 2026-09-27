package main

import (
	"bytes"
	"crypto/sha256"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
)

// Public access from outside the home network, with a Let's Encrypt certificate.
//
// Two kinds of certificate, side by side:
//   - LOCAL (always): at home every app is https://<LAN IP>:<port>, with a certificate from
//     Caddy's own CA (`tls internal`) — Let's Encrypt can't issue for a private IP. Devices
//     trust it once via root.crt. Nothing here changes that.
//   - PUBLIC (optional): the apps you pick are also served at https://<your domain>:<same port>
//     with a Let's Encrypt certificate for <domain> and *.<domain>, for reaching them from
//     outside (the domain points at your public IP; you forward those ports on the router).
//     The dashboard is never public — it's a root shell on the server.
//
// How:
//   - lego (the one-shot "lego" service in caddy/docker-compose.yml) solves the DNS-01
//     challenge through your DNS provider's API, with the credentials in .acme-env. DNS-01
//     needs no port open for the challenge, and it's the only challenge that can issue the
//     wildcard your other services can reuse.
//   - The certificate lands in <repo>/letsencrypt/certificates/<domain>.crt + .key. lego runs
//     as the folder's owner, so the files belong to you, not root, and other services can use
//     them too.
//   - Only once that certificate exists does hsctl write caddy/domain.caddy (which switches on
//     the chosen apps' public sites from caddy/letsencrypt.caddy) and reload Caddy. That
//     ordering is load-bearing: Caddy refuses to start at all if a certificate file it's told
//     to load is missing, which would take the local sites — and this dashboard — down with
//     it. So every `hsctl up` (and every renewal check) removes domain.caddy again if its
//     certificate is gone.
//   - A public Vaultwarden gets the domain as its DOMAIN (it has one canonical address, and
//     the Bitwarden apps that roam in and out of the house need the public one); a public
//     Nextcloud gains it as a trusted domain.
//   - The dashboard re-runs lego twice a day; lego only contacts Let's Encrypt when the
//     certificate is due, and hsctl reloads Caddy whenever the file actually changed.

const (
	acmeEnvFile = ".acme-env"          // DNS provider credentials, KEY=value lines (0600)
	certDirName = "letsencrypt"        // lego's --path: certificates/ + accounts/
	domainSites = "caddy/domain.caddy" // the switch: exists only while its certificate does

	// certRenewEvery is how often the dashboard asks lego to renew. lego itself decides when
	// a certificate is actually due (a third of its lifetime left, or earlier if Let's Encrypt
	// says so via ARI), so a frequent check costs nothing but a container start.
	certRenewEvery = 12 * time.Hour
)

// acmeResolvers are where lego checks that its challenge TXT record has propagated: public
// resolvers, deliberately not this LAN's — a local override (split-horizon DNS) for the domain
// would otherwise answer, or cache "no such record", in place of the real zone.
var acmeResolvers = "1.1.1.1:53,9.9.9.9:53"

// dnsCredentialHints are the credential variables most people need for a few popular
// providers, so `hsctl cert config` can ask for them directly. Every other provider lego
// supports works too — its variables are listed in the .acme-env template (lego dnshelp).
var dnsCredentialHints = map[string][]string{
	"cloudflare":   {"CF_DNS_API_TOKEN"},
	"duckdns":      {"DUCKDNS_TOKEN"},
	"porkbun":      {"PORKBUN_API_KEY", "PORKBUN_SECRET_API_KEY"},
	"desec":        {"DESEC_TOKEN"},
	"digitalocean": {"DO_AUTH_TOKEN"},
	"hetzner":      {"HETZNER_API_TOKEN"},
	"namecheap":    {"NAMECHEAP_API_USER", "NAMECHEAP_API_KEY"},
}

// ---- files -------------------------------------------------------------------

// certFiles are the certificate (with its chain) and key lego writes for domain.
func certFiles(repo, domain string) (crt, key string) {
	base := filepath.Join(repo, certDirName, "certificates", domain)
	return base + ".crt", base + ".key"
}

// certPresent reports whether both halves of domain's certificate are on disk.
func certPresent(repo, domain string) bool {
	crt, key := certFiles(repo, domain)
	return domain != "" && fileExists(crt) && fileExists(key)
}

// readCertificate parses the leaf (first) certificate of a PEM bundle.
func readCertificate(path string) (*x509.Certificate, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	for rest := b; ; {
		var blk *pem.Block
		if blk, rest = pem.Decode(rest); blk == nil {
			return nil, fmt.Errorf("%s: no PEM certificate in it", path)
		}
		if blk.Type == "CERTIFICATE" {
			return x509.ParseCertificate(blk.Bytes)
		}
	}
}

// fileHash identifies a file's content ("" if it can't be read) — how we tell that lego
// actually replaced the certificate, rather than parsing its log.
func fileHash(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%x", sha256.Sum256(b))
}

// domainSitesContent is caddy/domain.caddy: a public site for each app, on domain.
func domainSitesContent(domain string, apps []string) string {
	var b strings.Builder
	b.WriteString("# Written by `hsctl cert`: the apps reachable from outside at https://" + domain + ":<port>,\n" +
		"# with its Let's Encrypt certificate (see letsencrypt.caddy). Don't edit — `hsctl cert off` removes it.\n")
	for _, a := range apps {
		b.WriteString("import public_" + a + " " + domain + "\n")
	}
	return b.String()
}

// activePublic reads caddy/domain.caddy: the domain Caddy serves publicly and the apps it
// serves there. "" and nil when the file doesn't exist (everything is local only).
func activePublic(repo string) (domain string, apps []string) {
	b, err := os.ReadFile(filepath.Join(repo, domainSites))
	if err != nil {
		return "", nil
	}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) != 3 || f[0] != "import" || !strings.HasPrefix(f[1], "public_") {
			continue
		}
		domain = f[2]
		apps = append(apps, strings.TrimPrefix(f[1], "public_"))
	}
	return domain, apps
}

// publicURL is where app (a compose dir) is reached on host — its dashboard tile's port and
// path. ok is false for an app without a tile.
func publicURL(repo, app, host string) (url string, ok bool) {
	for _, t := range LoadServices(repo) {
		if t.Dir == app {
			return t.URL(host), true
		}
	}
	return "", false
}

// validPublicApps checks a list of apps to make public against the registry. The dashboard
// can't be one: it isn't an app, and it's a root shell.
func validPublicApps(apps []string) error {
	if len(apps) == 0 {
		return fmt.Errorf("pick at least one app to reach from outside, e.g. vaultwarden")
	}
	for _, a := range apps {
		if !validApp(a) {
			return fmt.Errorf("unknown app %q — one of: %s", a, strings.Join(appDirs(), ", "))
		}
	}
	return nil
}

// ---- validation ----------------------------------------------------------------

var (
	domainLabelRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	providerRE    = regexp.MustCompile(`^[a-z0-9]+$`)
)

// normalizeDomain lower-cases d and checks it can get a public certificate: a real DNS name
// (not an IP, not a wildcard — the wildcard is added for you) under a public suffix, so not
// .local/.lan/.internal and friends, which no public CA will issue for.
func normalizeDomain(d string) (string, error) {
	d = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(d)), ".")
	switch {
	case d == "":
		return "", fmt.Errorf("no domain given")
	case strings.HasPrefix(d, "*."):
		return "", fmt.Errorf("give the domain without \"*.\" — the certificate covers %s and *.%s", d[2:], d[2:])
	case net.ParseIP(d) != nil:
		return "", fmt.Errorf("%q is an IP address — Let's Encrypt needs a domain name you own", d)
	case len(d) > 253:
		return "", fmt.Errorf("%q is too long for a domain name", d)
	}
	labels := strings.Split(d, ".")
	if len(labels) < 2 {
		return "", fmt.Errorf("%q isn't a full domain name — e.g. home.example.com", d)
	}
	for _, l := range labels {
		if !domainLabelRE.MatchString(l) {
			return "", fmt.Errorf("%q isn't a valid domain name", d)
		}
	}
	for _, tld := range []string{"local", "lan", "home", "internal", "localhost", "arpa", "test", "invalid", "example"} {
		if labels[len(labels)-1] == tld {
			return "", fmt.Errorf("%q is a private name — Let's Encrypt only issues for public domains you own", d)
		}
	}
	return d, nil
}

// acmeEmail is the contact to register with, or "" for none. The shipped placeholder
// (you@example.com) is dropped: Let's Encrypt rejects example.com addresses outright, and
// an account needs no email at all.
func acmeEmail(e string) string {
	e = strings.TrimSpace(e)
	if e == "" || strings.HasSuffix(strings.ToLower(e), "@example.com") {
		return ""
	}
	return e
}

// acmeCredentials returns the non-empty KEY=value pairs in .acme-env.
func acmeCredentials(repo string) (map[string]string, error) {
	kv, err := readKV(filepath.Join(repo, acmeEnvFile))
	if err != nil {
		return nil, err
	}
	for k, v := range kv {
		if v == "" {
			delete(kv, k)
		}
	}
	return kv, nil
}

// isStagingCert reports whether cert came from a test CA (Let's Encrypt staging names its
// issuers "(STAGING) …") — never trusted by browsers, so it must be replaced when moving to
// the real CA even though it isn't due.
func isStagingCert(cert *x509.Certificate) bool {
	return strings.Contains(strings.ToUpper(cert.Issuer.CommonName), "STAGING")
}

func isStagingServer(server string) bool { return strings.Contains(strings.ToLower(server), "staging") }

// ---- running lego ----------------------------------------------------------------

// ownerOf returns a path's owning uid/gid.
func ownerOf(path string) (uid, gid int, err error) {
	fi, err := os.Stat(path)
	if err != nil {
		return 0, 0, err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, fmt.Errorf("%s: can't read its owner", path)
	}
	return int(st.Uid), int(st.Gid), nil
}

// ensureCertDir makes sure <repo>/letsencrypt exists and belongs to the repo's owner, and
// returns who lego should run as: that owner, so the certificate is the user's to use
// elsewhere — not root's, even when hsctl itself runs as root (sudo, or the dashboard
// service). As root it also hands back anything an earlier root-run left behind; unprivileged,
// it runs lego as whoever owns the folder, so lego can always write there.
func ensureCertDir(repo string) (uid, gid int, err error) {
	dir := filepath.Join(repo, certDirName)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return 0, 0, err
	}
	if os.Geteuid() == 0 {
		ru, rg, err := ownerOf(repo)
		if err != nil {
			return 0, 0, err
		}
		if du, dg, err := ownerOf(dir); err == nil && (du != ru || dg != rg) {
			_ = filepath.WalkDir(dir, func(p string, _ fs.DirEntry, err error) error {
				if err == nil {
					_ = os.Lchown(p, ru, rg)
				}
				return nil
			})
		}
	}
	return ownerOf(dir)
}

// legoImage is the lego image the compose file pins (after any caddy/.env override) — for
// the one-off `dnshelp` lookup, which runs outside compose because the service's .acme-env
// mount doesn't exist yet at that point.
func legoImage(repo string) string {
	data, err := os.ReadFile(filepath.Join(repo, "caddy", "docker-compose.yml"))
	if err != nil {
		return "goacme/lego"
	}
	env, _ := readKV(filepath.Join(repo, "caddy", ".env"))
	for _, line := range strings.Split(string(data), "\n") {
		if v, def, ok := parseComposeImage(line); ok && v == "LEGO_IMAGE" {
			return effectiveRef(v, def, env)
		}
	}
	return "goacme/lego"
}

// dnsHelp is lego's description of a DNS provider (`lego dnshelp -c <code>`) — which
// credential variables it takes. Errors for a provider lego doesn't know.
func dnsHelp(repo, provider string) (string, error) {
	out, err := dockerCombined(repo, "run", "--rm", legoImage(repo), "dnshelp", "-c", provider)
	switch {
	case strings.Contains(out, "not yet supported"):
		return "", fmt.Errorf("lego doesn't know a DNS provider called %q — see https://go-acme.github.io/lego/dns/", provider)
	case err != nil || !strings.Contains(out, "Code:"):
		return "", fmt.Errorf("couldn't look up DNS provider %q with lego (is Docker running?): %v\n%s", provider, err, strings.TrimSpace(out))
	}
	return strings.TrimSpace(out), nil
}

// clearUnregisteredAccounts removes Let's Encrypt accounts lego saved locally but never
// registered — it writes the account before registering it, so a first run that failed in
// between (network down, bad credentials file) leaves one behind, and lego then refuses every
// later run ("accountDoesNotExist") instead of registering. Such an account has nothing to
// lose; deleting it lets the next run register afresh. Registered accounts are left alone.
func clearUnregisteredAccounts(repo string, out io.Writer) {
	files, _ := filepath.Glob(filepath.Join(repo, certDirName, "accounts", "*", "*", "account.json"))
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var acct struct {
			Registration json.RawMessage `json:"registration"`
		}
		if json.Unmarshal(b, &acct) != nil {
			continue
		}
		if r := strings.TrimSpace(string(acct.Registration)); r == "" || r == "null" {
			if os.RemoveAll(filepath.Dir(f)) == nil {
				fmt.Fprintf(out, "(removed an unfinished Let's Encrypt account from an earlier failed run: %s)\n", filepath.Dir(f))
			}
		}
	}
}

// legoRunArgs is the `docker compose` argv (run from caddy/) that gets or renews c.Domain's
// certificate. `lego run` is idempotent: with a valid certificate that isn't due it just says
// so and exits 0 without contacting the CA for a new one.
func legoRunArgs(c Config, uid, gid int, background, force bool) []string {
	args := []string{"compose", "run", "--rm", "-T", "--user", fmt.Sprintf("%d:%d", uid, gid), "lego",
		"run", "--accept-tos", "--path", "/letsencrypt", "--env-file", "/acme.env",
		"--dns", c.DNSProvider, "--dns.resolvers", acmeResolvers,
		"--domains", c.Domain, "--domains", "*." + c.Domain,
		// A certificate for a different set of names (you changed DOMAIN) is replaced, not kept.
		"--force-cert-domains",
		"--log.format", "text"}
	if e := acmeEmail(c.ACMEEmail); e != "" {
		args = append(args, "--email", e)
	}
	if c.ACMEServer != "" {
		args = append(args, "--server", c.ACMEServer)
	}
	// lego spreads unattended renewals out with a random delay; someone waiting on the
	// command (CLI or a dashboard button) shouldn't sit through that.
	if !background {
		args = append(args, "--no-random-sleep")
	}
	if force {
		args = append(args, "--renew-force")
	}
	return args
}

// issueCert runs lego for c.Domain — getting the certificate the first time, renewing it when
// due after that — with its output going to out. renewed reports that the certificate file
// changed (a new one was written). It does not touch Caddy; see applyDomain.
func issueCert(repo string, c Config, background, force bool, out io.Writer) (renewed bool, err error) {
	if c.Domain == "" || c.DNSProvider == "" {
		return false, fmt.Errorf("no domain configured — run: hsctl cert config --domain <domain> --dns <provider>")
	}
	envPath := filepath.Join(repo, acmeEnvFile)
	if fi, err := os.Stat(envPath); err == nil && fi.IsDir() {
		return false, fmt.Errorf("%s is a directory — remove it and run hsctl cert config", envPath)
	}
	if creds, err := acmeCredentials(repo); err != nil || len(creds) == 0 {
		return false, fmt.Errorf("no DNS API credentials in %s — fill it in (hsctl cert config writes a template)", envPath)
	}
	uid, gid, err := ensureCertDir(repo)
	if err != nil {
		return false, fmt.Errorf("preparing %s: %w", filepath.Join(repo, certDirName), err)
	}
	// lego reads the credentials as that same user, and .acme-env is 0600: one written by
	// root (the dashboard's form — the service runs as root) would be unreadable to it.
	if os.Geteuid() == 0 {
		if err := os.Chown(envPath, uid, gid); err != nil {
			return false, fmt.Errorf("handing %s to the certificate folder's owner: %w", envPath, err)
		}
	}
	// One lego at a time: the dashboard's renewal loop and a button or terminal run could
	// otherwise overlap and write the same files. The second simply waits, then finds the
	// certificate fresh.
	if lock, err := os.Open(filepath.Join(repo, certDirName)); err == nil {
		defer lock.Close()
		if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err == nil {
			defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
		}
	}
	clearUnregisteredAccounts(repo, out)
	crt, _ := certFiles(repo, c.Domain)
	// Moving from the staging CA to the real one: the staging certificate isn't due, but it
	// isn't trusted either — replace it now.
	if cert, err := readCertificate(crt); err == nil && isStagingCert(cert) && !isStagingServer(c.ACMEServer) {
		force = true
	}
	before := fileHash(crt)
	cmd := dockerCmd(filepath.Join(repo, "caddy"), legoRunArgs(c, uid, gid, background, force)...)
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Run(); err != nil {
		return false, fmt.Errorf("lego couldn't get the certificate (its log above says why): %w", err)
	}
	if !certPresent(repo, c.Domain) {
		return false, fmt.Errorf("lego finished, but there's no certificate at %s", crt)
	}
	return fileHash(crt) != before, nil
}

// ---- wiring the domain into the stack ------------------------------------------------

// containerRunning reports whether a stack container is up, quietly (a switched-off app's
// container simply doesn't exist). A variable so tests never reach the real containers of a
// stack that happens to run on the same machine.
var containerRunning = func(repo, name string) bool {
	out, err := dockerCmd(repo, "inspect", "-f", "{{.State.Status}}", name).Output()
	return err == nil && strings.TrimSpace(string(out)) == "running"
}

// reloadCaddy makes a running Caddy re-read its config AND its certificate files: --force,
// because a renewed certificate leaves the config itself unchanged, and a plain reload skips
// an unchanged config (still serving the old certificate). Graceful — no dropped connections.
// A stopped Caddy is fine: it reads everything when it next starts.
func reloadCaddy(repo string) error {
	if !containerRunning(repo, "caddy") {
		return nil
	}
	if out, err := dockerCombined(repo, "exec", "caddy", "caddy", "reload",
		"--config", "/etc/caddy/Caddyfile", "--adapter", "caddyfile", "--force"); err != nil {
		return fmt.Errorf("caddy reload: %v\n%s", err, strings.TrimSpace(out))
	}
	return nil
}

// vaultURL is Vaultwarden's public address on host — its DOMAIN setting.
func vaultURL(repo, host string) string {
	port := 8443
	if kv, err := readKV(filepath.Join(repo, "caddy", ".env")); err == nil {
		port = atoiDef(kv["VAULT_HTTPS"], port)
	}
	return fmt.Sprintf("https://%s:%d", host, port)
}

// setVaultwardenDomain points Vaultwarden's DOMAIN at url and recreates it if it's running
// (a new env needs a new container). Vaultwarden has ONE canonical address — its links,
// passkey/WebAuthn origin and CORS all follow DOMAIN — so when it's public it takes the public
// one: that's what the Bitwarden apps, which roam in and out of the house, are set to.
func setVaultwardenDomain(repo, url string, out io.Writer) error {
	env := filepath.Join(repo, "vaultwarden", ".env")
	kv, err := readKV(env)
	if err != nil || kv["VW_DOMAIN"] == url {
		return nil // not set up yet (Generate picks the right one), or already right
	}
	if err := setEnvKey(env, "VW_DOMAIN", url); err != nil {
		return err
	}
	fmt.Fprintf(out, "Vaultwarden: DOMAIN is now %s\n", url)
	if containerRunning(repo, "vaultwarden") {
		if o, err := dockerCombined(filepath.Join(repo, "vaultwarden"), "compose", "up", "-d"); err != nil {
			return fmt.Errorf("restarting vaultwarden: %v\n%s", err, strings.TrimSpace(o))
		}
		fmt.Fprintln(out, "Vaultwarden: restarted with it")
	}
	return nil
}

// addNextcloudTrustedDomain adds domain to a running Nextcloud's trusted_domains (it rejects
// requests for any host not listed) and to NC_TRUSTED_DOMAINS, which a fresh install reads.
// The env var alone isn't enough: the image only applies it on first install. Best-effort —
// Nextcloud may be switched off or not installed yet; the next renewal check retries.
func addNextcloudTrustedDomain(repo, domain string, out io.Writer) error {
	env := filepath.Join(repo, "nextcloud", ".env")
	if kv, err := readKV(env); err == nil {
		if cur := strings.Fields(kv["NC_TRUSTED_DOMAINS"]); !slices.Contains(cur, domain) {
			if err := setEnvKey(env, "NC_TRUSTED_DOMAINS", strings.Join(append(cur, domain), " ")); err != nil {
				return err
			}
		}
	}
	if !containerRunning(repo, "nextcloud-app") {
		return nil
	}
	occ := func(args ...string) (string, error) {
		return dockerOut(repo, append([]string{"exec", "-u", "www-data", "nextcloud-app", "php", "occ"}, args...)...)
	}
	raw, err := occ("config:system:get", "trusted_domains", "--output=json")
	if err != nil {
		return fmt.Errorf("reading Nextcloud's trusted domains: %w", err)
	}
	have, next := parseTrustedDomains(raw)
	if slices.Contains(have, domain) {
		return nil
	}
	if _, err := occ("config:system:set", "trusted_domains", strconv.Itoa(next), "--value="+domain); err != nil {
		return fmt.Errorf("adding %s to Nextcloud's trusted domains: %w", domain, err)
	}
	fmt.Fprintf(out, "Nextcloud: added %s to its trusted domains\n", domain)
	return nil
}

// parseTrustedDomains reads `occ config:system:get trusted_domains --output=json`: a JSON
// list, or an object when the indexes have gaps. next is the first free index.
func parseTrustedDomains(raw string) (domains []string, next int) {
	var list []string
	if json.Unmarshal([]byte(raw), &list) == nil {
		return list, len(list)
	}
	var obj map[string]string
	if json.Unmarshal([]byte(raw), &obj) == nil {
		for k, v := range obj {
			domains = append(domains, v)
			if i, err := strconv.Atoi(k); err == nil && i+1 > next {
				next = i + 1
			}
		}
		sort.Strings(domains)
	}
	return domains, next
}

// applyDomain makes c.PublicApps reachable from outside at c.Domain (whose certificate must
// exist): writes caddy/domain.caddy and reloads Caddy when it changed or the certificate was
// renewed, then points Vaultwarden at the public address if it's public (else back at the IP)
// and adds the domain to a public Nextcloud's trusted domains. Idempotent — the renewal loop
// calls it every time, which also heals a `setup --force` that reset VW_DOMAIN. If Caddy
// rejects the new config, domain.caddy is put back as it was, so the next Caddy start can't
// fail on it.
func applyDomain(repo string, c Config, renewed bool, out io.Writer) error {
	if err := validPublicApps(c.PublicApps); err != nil {
		return err
	}
	if !certPresent(repo, c.Domain) {
		return fmt.Errorf("no certificate for %s yet — run: hsctl cert issue", c.Domain)
	}
	sites := filepath.Join(repo, domainSites)
	want := domainSitesContent(c.Domain, c.PublicApps)
	cur, _ := os.ReadFile(sites)
	changed := string(cur) != want
	if changed {
		if err := writeFile0644(sites, want); err != nil {
			return err
		}
	}
	if changed || renewed {
		if err := reloadCaddy(repo); err != nil {
			if changed {
				if len(cur) > 0 {
					_ = writeFile0644(sites, string(cur))
				} else {
					_ = os.Remove(sites)
				}
			}
			return err
		}
		if changed {
			fmt.Fprintf(out, "Caddy: serving %s publicly at %s (the local addresses are unchanged)\n", strings.Join(c.PublicApps, ", "), c.Domain)
		} else {
			fmt.Fprintln(out, "Caddy: reloaded with the renewed certificate")
		}
	}
	var errs []error
	vwHost := c.ServerIP
	if slices.Contains(c.PublicApps, "vaultwarden") {
		vwHost = c.Domain
	}
	if err := setVaultwardenDomain(repo, vaultURL(repo, vwHost), out); err != nil {
		errs = append(errs, err)
	}
	if slices.Contains(c.PublicApps, "nextcloud") {
		if err := addNextcloudTrustedDomain(repo, c.Domain, out); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// dropDomain ends public access: removes caddy/domain.caddy, reloads Caddy and points
// Vaultwarden back at the IP. The certificate files stay (a later `hsctl cert issue` reuses
// them), and so does the domain in Nextcloud's trusted list, where it's harmless.
func dropDomain(repo string, c Config, out io.Writer) error {
	sites := filepath.Join(repo, domainSites)
	if fileExists(sites) {
		if err := os.Remove(sites); err != nil {
			return err
		}
		if err := reloadCaddy(repo); err != nil {
			return err
		}
		fmt.Fprintln(out, "Caddy: nothing is served publicly any more (local addresses unchanged)")
	}
	return setVaultwardenDomain(repo, vaultURL(repo, c.ServerIP), out)
}

// reconcileDomain runs before the stack starts: if caddy/domain.caddy points at a
// certificate that isn't there (letsencrypt/ was deleted, or a restore brought back the
// config without it), end public access rather than let Caddy — and so the whole front door,
// local sites included — fail to start.
func reconcileDomain(repo string, out io.Writer) {
	d, _ := activePublic(repo)
	switch {
	case !fileExists(filepath.Join(repo, domainSites)) || (d != "" && certPresent(repo, d)):
		return
	case d == "":
		// hsctl owns this file; one it can't read (hand-edited, or from another version) is
		// as likely to break Caddy's start as one pointing at a missing certificate.
		fmt.Fprintf(out, "warning: %s isn't in the form hsctl writes — removed it, so public access is off\n"+
			"  (local access is unaffected). Turn it back on with: hsctl cert issue\n", domainSites)
	default:
		crt, _ := certFiles(repo, d)
		fmt.Fprintf(out, "warning: the certificate for %s is missing (%s) — public access is off, local access is unaffected.\n"+
			"  Get it again with: hsctl cert issue\n", d, crt)
	}
	c := LoadConfig(repo)
	c.Normalize()
	if err := dropDomain(repo, c, out); err != nil {
		fmt.Fprintf(out, "warning: %v\n", err)
	}
}

// renewCert is one unattended renewal check (the dashboard's loop): get or renew the
// configured domain's certificate and keep the stack pointed at it. Output goes to out.
func renewCert(repo string, out io.Writer) (renewed bool, err error) {
	c := LoadConfig(repo)
	c.Normalize()
	reconcileDomain(repo, out)
	if c.Domain == "" {
		// DOMAIN was cleared from setup.conf by hand: stop serving a certificate nobody renews.
		if c.ActiveDomain != "" {
			return false, dropDomain(repo, c, out)
		}
		return false, nil
	}
	if renewed, err = issueCert(repo, c, true, false, out); err != nil {
		return false, err
	}
	return renewed, applyDomain(repo, c, renewed, out)
}

// ---- status ------------------------------------------------------------------------

// publicApp is one app reachable from outside: its tile name, public URL and the port to
// forward on the router.
type publicApp struct {
	Dir, Name, URL string
	Port           int
}

// publicApps describes dirs as served on domain, using their dashboard tiles.
func publicApps(repo, domain string, dirs []string) []publicApp {
	tiles := map[string]Service{}
	for _, t := range LoadServices(repo) {
		tiles[t.Dir] = t
	}
	var out []publicApp
	for _, d := range dirs {
		p := publicApp{Dir: d, Name: d}
		if t, ok := tiles[d]; ok {
			p.Name, p.URL, p.Port = t.Name, t.URL(domain), t.HTTPSPort
		}
		out = append(out, p)
	}
	return out
}

// certStatus is what the CLI and the dashboard show about public access + its certificate.
type certStatus struct {
	Domain, Provider, Server, Email string
	PublicApps                      []string    // configured to be public
	Active                          string      // domain Caddy serves publicly now ("" = local only)
	Live                            []publicApp // what Caddy serves publicly now
	HasCreds                        bool        // .acme-env has at least one value
	CertPath                        string
	Present                         bool
	Names                           []string
	Issuer                          string
	NotBefore, NotAfter             time.Time
	Staging                         bool
	Err                             string // certificate present but unreadable
}

func loadCertStatus(repo string, c Config) certStatus {
	st := certStatus{Domain: c.Domain, Provider: c.DNSProvider, Server: c.ACMEServer, Email: acmeEmail(c.ACMEEmail),
		PublicApps: c.PublicApps, Active: c.ActiveDomain}
	if c.ActiveDomain != "" {
		st.Live = publicApps(repo, c.ActiveDomain, c.ActivePublic)
	}
	creds, _ := acmeCredentials(repo)
	st.HasCreds = len(creds) > 0
	d := c.Domain
	if d == "" {
		d = c.ActiveDomain
	}
	if d == "" {
		return st
	}
	st.CertPath, _ = certFiles(repo, d)
	st.Present = certPresent(repo, d)
	if !st.Present {
		return st
	}
	cert, err := readCertificate(st.CertPath)
	if err != nil {
		st.Err = err.Error()
		return st
	}
	st.Names, st.Issuer, st.Staging = cert.DNSNames, cert.Issuer.CommonName, isStagingCert(cert)
	st.NotBefore, st.NotAfter = cert.NotBefore, cert.NotAfter
	return st
}

// DaysLeft is how long the certificate stays valid, in whole days.
func (s certStatus) DaysLeft() int { return int(time.Until(s.NotAfter).Hours() / 24) }

// Expiring is true when renewals have evidently been failing: lego renews with a third of
// the lifetime left (half, for short-lived certificates), so under a sixth left — 15 days of
// a 90-day certificate — means many checks in a row didn't manage it.
func (s certStatus) Expiring() bool {
	return s.Present && s.Err == "" && time.Until(s.NotAfter) < s.NotAfter.Sub(s.NotBefore)/6
}

func (s certStatus) Expiry() string { return s.NotAfter.Local().Format("2 Jan 2006") }

func printCertStatus(w io.Writer, st certStatus, serverIP string) {
	fmt.Fprintf(w, "local         https://%s:<port> for every app, with Caddy's own CA (root.crt) — always on\n", serverIP)
	if st.Domain == "" && st.Active == "" {
		fmt.Fprintln(w, "public        off — to reach apps from outside with a Let's Encrypt certificate: hsctl cert config")
		fmt.Fprintln(w, "              (see docs/letsencrypt.md)")
		return
	}
	if st.Domain == "" {
		fmt.Fprintf(w, "public        no domain configured, but Caddy still serves %s publicly — run: hsctl cert off\n", st.Active)
		return
	}
	fmt.Fprintf(w, "domain        %s (the certificate also covers *.%s)\n", st.Domain, st.Domain)
	fmt.Fprintf(w, "public apps   %s\n", strings.Join(st.PublicApps, ", "))
	fmt.Fprintf(w, "DNS provider  %s — credentials %s\n", st.Provider, boolStr(st.HasCreds, "set in "+acmeEnvFile, "MISSING from "+acmeEnvFile))
	if st.Server != "" {
		fmt.Fprintf(w, "ACME server   %s\n", st.Server)
	}
	switch {
	case !st.Present:
		fmt.Fprintln(w, "certificate   none yet — get it with: hsctl cert issue")
	case st.Err != "":
		fmt.Fprintf(w, "certificate   UNREADABLE: %s\n", st.Err)
	default:
		fmt.Fprintf(w, "certificate   %s\n", st.CertPath)
		fmt.Fprintf(w, "  covers      %s\n", strings.Join(st.Names, ", "))
		fmt.Fprintf(w, "  issuer      %s%s\n", st.Issuer, boolStr(st.Staging, "  (STAGING — not trusted by browsers)", ""))
		fmt.Fprintf(w, "  valid until %s (%d days left)%s\n", st.Expiry(), st.DaysLeft(), boolStr(st.Expiring(), "  ⚠ renewals are failing — run: hsctl cert issue", ""))
	}
	switch {
	case st.Active == "":
		fmt.Fprintln(w, "public now    nothing yet (switches on once the certificate exists)")
	case st.Active != st.Domain:
		fmt.Fprintf(w, "public now    still the OLD domain %s, until hsctl cert issue gets %s's certificate\n", st.Active, st.Domain)
	default:
		fmt.Fprintf(w, "public now    (forward each port on your router to %s)\n", serverIP)
		for _, a := range st.Live {
			fmt.Fprintf(w, "  %-11s %s   port %d\n", a.Name, a.URL, a.Port)
		}
	}
}

// ---- CLI: hsctl cert ------------------------------------------------------------------

func certCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "cert", Short: "Public access from outside, with a Let's Encrypt certificate (see docs/letsencrypt.md)"}

	cfg := &cobra.Command{Use: "config", Short: "Set the public domain, the apps to make public, and the DNS provider (+ API credentials)", Args: cobra.NoArgs, RunE: runCertConfig}
	cfg.Flags().String("domain", "", "your public domain, e.g. home.example.com (the certificate also covers *.<domain>)")
	cfg.Flags().String("public", "", "apps reachable from outside, comma-separated (default vaultwarden) — the dashboard never is")
	cfg.Flags().String("dns", "", "DNS provider code, e.g. cloudflare, duckdns, porkbun (lego's list: https://go-acme.github.io/lego/dns/)")
	cfg.Flags().String("email", "", "contact email for your Let's Encrypt account (optional)")
	cfg.Flags().String("server", "", `ACME server: "letsencrypt-staging" to test, "" for Let's Encrypt`)

	issue := &cobra.Command{Use: "issue", Aliases: []string{"renew"}, Args: cobra.NoArgs,
		Short: "Get the certificate (or renew it if due) and serve the public apps — safe to repeat",
		RunE: func(c *cobra.Command, _ []string) error {
			force, _ := c.Flags().GetBool("force")
			return runCertIssue(force)
		}}
	issue.Flags().Bool("force", false, "renew even if the certificate isn't due (counts against Let's Encrypt's rate limits)")

	status := &cobra.Command{Use: "status", Short: "Show what's public, the certificate and when it expires", Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			repo, err := requireRepoDir()
			if err != nil {
				return err
			}
			c := LoadConfig(repo)
			c.Normalize()
			printCertStatus(os.Stdout, loadCertStatus(repo, c), c.ServerIP)
			return nil
		}}

	off := &cobra.Command{Use: "off", Short: "End public access (local access is unaffected); keeps the certificate files", Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			repo, err := requireRepoDir()
			if err != nil {
				return err
			}
			c := LoadConfig(repo)
			c.Normalize()
			if err := dropDomain(repo, c, os.Stdout); err != nil {
				return err
			}
			c.Domain = ""
			if err := c.Save(repo); err != nil {
				return err
			}
			fmt.Printf("Public access off and renewals stopped. At home everything stays at https://%s:<port>.\n", c.ServerIP)
			fmt.Println("Remove the port forwards from your router too.")
			return nil
		}}

	cmd.AddCommand(cfg, issue, status, off)
	return cmd
}

func runCertConfig(cmd *cobra.Command, _ []string) error {
	repo, err := requireRepoDir()
	if err != nil {
		return err
	}
	f := cmd.Flags()
	c := LoadConfig(repo)
	c.Normalize()
	for name, dst := range map[string]*string{"domain": &c.Domain, "dns": &c.DNSProvider, "email": &c.ACMEEmail, "server": &c.ACMEServer} {
		if f.Changed(name) {
			*dst, _ = f.GetString(name)
		}
	}
	if f.Changed("public") {
		v, _ := f.GetString("public")
		c.PublicApps = splitCSV(v)
	}
	if len(c.PublicApps) == 0 {
		c.PublicApps = []string{"vaultwarden"}
	}
	// On a terminal, ask for whatever wasn't given as a flag.
	tty := isTTY()
	if tty && !(f.Changed("domain") && f.Changed("dns")) {
		fmt.Println("== Public access from outside (Enter accepts each [default]) ==")
		if !f.Changed("domain") {
			c.Domain = ask("Public domain (the certificate covers it and *.<domain>)", c.Domain)
		}
		if !f.Changed("public") {
			fmt.Printf("  Apps: %s (the dashboard is never public)\n", strings.Join(appDirs(), ", "))
			c.PublicApps = splitCSV(ask("Apps to reach from outside (comma-separated)", strings.Join(c.PublicApps, ",")))
		}
		if !f.Changed("dns") {
			if c.DNSProvider == "" {
				c.DNSProvider = "cloudflare"
			}
			c.DNSProvider = ask("DNS provider (lego code: cloudflare, duckdns, porkbun, …)", c.DNSProvider)
		}
		if !f.Changed("email") {
			if c.ACMEEmail = ask("Email for your Let's Encrypt account (optional, 'none' to skip)", acmeEmail(c.ACMEEmail)); c.ACMEEmail == "none" {
				c.ACMEEmail = ""
			}
		}
	}
	if c.Domain, err = normalizeDomain(c.Domain); err != nil {
		return err
	}
	if err := validPublicApps(c.PublicApps); err != nil {
		return fmt.Errorf("--public: %w", err)
	}
	c.DNSProvider = strings.ToLower(strings.TrimSpace(c.DNSProvider))
	if !providerRE.MatchString(c.DNSProvider) {
		return fmt.Errorf("--dns: give lego's provider code, e.g. cloudflare (list: https://go-acme.github.io/lego/dns/)")
	}
	help, err := dnsHelp(repo, c.DNSProvider)
	if err != nil {
		return err
	}
	if err := c.Save(repo); err != nil {
		return err
	}
	fmt.Printf("Saved: %s reachable from outside at %s (DNS provider %s), in %s\n",
		strings.Join(c.PublicApps, ", "), c.Domain, c.DNSProvider, confFile)

	envPath := filepath.Join(repo, acmeEnvFile)
	if !fileExists(envPath) {
		if err := writeFile0600(envPath, acmeEnvTemplate(c, help)); err != nil {
			return err
		}
		fmt.Printf("Wrote %s — the place for your DNS provider's API credentials.\n", envPath)
	}
	creds, _ := acmeCredentials(repo)
	if tty {
		for _, k := range dnsCredentialHints[c.DNSProvider] {
			if creds[k] != "" {
				continue
			}
			if v := askSecret(k + " (input hidden; Enter to skip)"); v != "" {
				if err := upsertEnvKey(envPath, k, v); err != nil {
					return err
				}
			}
		}
		creds, _ = acmeCredentials(repo)
	}
	if len(creds) == 0 {
		fmt.Printf("\nNext: put your %s API credentials in %s", c.DNSProvider, envPath)
		if hint := dnsCredentialHints[c.DNSProvider]; len(hint) > 0 {
			fmt.Printf(" (%s=…)", strings.Join(hint, "=…, "))
		}
		fmt.Println(",\nthen: hsctl cert issue")
		return nil
	}
	fmt.Println("\nNext: hsctl cert issue")
	return nil
}

// acmeEnvTemplate is a fresh .acme-env: what the file is for, lego's list of the provider's
// variables, and the common ones ready to fill in.
func acmeEnvTemplate(c Config, help string) string {
	var b bytes.Buffer
	fmt.Fprintf(&b, "# DNS API credentials for your Let's Encrypt certificate (hsctl cert). lego uses them to\n")
	fmt.Fprintf(&b, "# add a short-lived TXT record, _acme-challenge.%s, proving you control the domain.\n", c.Domain)
	fmt.Fprintf(&b, "# One KEY=value per line. Keep this private: chmod 600, git-ignored, never shown by hsctl.\n#\n")
	fmt.Fprintf(&b, "# What lego accepts for %q (from `lego dnshelp -c %s`):\n#\n", c.DNSProvider, c.DNSProvider)
	for _, line := range strings.Split(help, "\n") {
		fmt.Fprintln(&b, strings.TrimRight("#   "+line, " \t"))
	}
	fmt.Fprintln(&b, "#")
	for _, k := range dnsCredentialHints[c.DNSProvider] {
		fmt.Fprintf(&b, "%s=\n", k)
	}
	return b.String()
}

func runCertIssue(force bool) error {
	repo, err := requireRepoDir()
	if err != nil {
		return err
	}
	c := LoadConfig(repo)
	c.Normalize()
	if c.Domain == "" {
		return fmt.Errorf("no domain configured — run: hsctl cert config --domain <domain> --dns <provider>")
	}
	fmt.Printf("== Let's Encrypt certificate for %s and *.%s (DNS-01 via %s) ==\n", c.Domain, c.Domain, c.DNSProvider)
	renewed, err := issueCert(repo, c, false, force, os.Stdout)
	if err != nil {
		return err
	}
	fmt.Println()
	fmt.Println("== public access ==")
	if err := applyDomain(repo, c, renewed, os.Stdout); err != nil {
		return err
	}
	c = LoadConfig(repo)
	c.Normalize()
	st := loadCertStatus(repo, c)
	fmt.Printf("\nDone: certificate for %s — valid until %s (%d days). The dashboard renews it automatically.\n",
		strings.Join(st.Names, ", "), st.Expiry(), st.DaysLeft())
	if st.Staging {
		fmt.Println("This is a STAGING certificate (not trusted by browsers) — for the real one:\n  hsctl cert config --server \"\" && hsctl cert issue")
	}
	fmt.Printf("\nFrom outside, once your router forwards each port below (TCP) to %s:\n", c.ServerIP)
	for _, a := range st.Live {
		fmt.Printf("  %-11s %s   port %d\n", a.Name, a.URL, a.Port)
	}
	fmt.Printf("At home nothing changes: every app stays at https://%s:<port>.\n", c.ServerIP)
	for _, n := range publicNotes(c, resolveHost(c.Domain)) {
		fmt.Println("\nnote: " + n)
	}
	return nil
}

// resolveHost looks name up (nil on failure) — a variable so tests needn't touch DNS.
var resolveHost = func(name string) []net.IP {
	ips, _ := net.LookupIP(name)
	return ips
}

// publicNotes are the checks worth pointing out once apps are public: the domain has to
// resolve to your public address (a LAN address can't be reached from outside), and a public
// Vaultwarden with open signups lets anyone on the internet create an account.
func publicNotes(c Config, ips []net.IP) []string {
	var notes []string
	switch {
	case len(ips) == 0:
		notes = append(notes, fmt.Sprintf("%s doesn't resolve yet — add a DNS record pointing it at your public IP address.", c.Domain))
	case slices.ContainsFunc(ips, func(ip net.IP) bool { return ip.IsPrivate() || ip.IsLoopback() }):
		notes = append(notes, fmt.Sprintf("%s resolves to a private address (%v) — outside your network it must point at your public IP.", c.Domain, ips))
	}
	if slices.Contains(c.PublicApps, "vaultwarden") && c.VWSignupsAllowed {
		notes = append(notes, "Vaultwarden allows open signups, so anyone on the internet could now create an account.\n"+
			"  Once your family has signed up, switch it off: set VW_SIGNUPS_ALLOWED=false in vaultwarden/.env and\n"+
			"  in setup.conf, then: cd vaultwarden && docker compose up -d")
	}
	return notes
}
