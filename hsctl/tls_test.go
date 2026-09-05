package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func leTestConfig() Config {
	return Config{
		ServerIP: "192.168.0.150", ACMEEmail: "admin@home-net.dev", UIPort: 8088,
		Domain: "home.example-lab.dev", LetsEncrypt: true, ACMEChallenge: challengeDNS,
		DNSProvider: "cloudflare", DNSToken: "tok$en",
	}
}

// TestValidateTLS covers the checks that stop a typo from reaching Caddy or Let's Encrypt.
func TestValidateTLS(t *testing.T) {
	ok := leTestConfig()
	if err := validateTLS(ok); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	off := ok
	off.LetsEncrypt, off.Domain = false, ""
	if err := validateTLS(off); err != nil {
		t.Fatalf("disabled config must not be validated: %v", err)
	}
	bad := map[string]func(c *Config){
		"no domain":         func(c *Config) { c.Domain = "" },
		"scheme in domain":  func(c *Config) { c.Domain = "https://home.example-lab.dev" },
		"ip as domain":      func(c *Config) { c.Domain = "192.168.0.150" },
		"single label":      func(c *Config) { c.Domain = "homeserver" },
		".local tld":        func(c *Config) { c.Domain = "server.local" },
		"example.com":       func(c *Config) { c.Domain = "home.example.com" },
		"placeholder email": func(c *Config) { c.ACMEEmail = "you@example.com" },
		"no email":          func(c *Config) { c.ACMEEmail = "" },
		"bad provider":      func(c *Config) { c.DNSProvider = "cloud flare; rm -rf" },
		"dns without token": func(c *Config) { c.DNSToken = "" },
		"unknown challenge": func(c *Config) { c.ACMEChallenge = "magic" },
	}
	for name, mutate := range bad {
		c := ok
		mutate(&c)
		if err := validateTLS(c); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
	// The http challenge needs neither provider nor token.
	h := ok
	h.ACMEChallenge, h.DNSProvider, h.DNSToken = challengeHTTP, "", ""
	if err := validateTLS(h); err != nil {
		t.Errorf("http challenge without dns settings should validate: %v", err)
	}
}

// TestRenderLEConfig: the Let's Encrypt config is a complete Caddyfile — global options (email,
// the domain as default SNI, no auto redirects), then the sites, then the user's sites.d import.
func TestRenderLEConfig(t *testing.T) {
	c := leTestConfig()
	out := renderLEConfig(c, leHostsFor(c, nil))
	for _, must := range []string{
		"{\n\temail {$ACME_EMAIL}\n", "\tdefault_sni home.example-lab.dev\n", "\tauto_https disable_redirects\n}",
		"import /etc/caddy/sites.d/*.caddy",
	} {
		if !strings.Contains(out, must) {
			t.Errorf("LE config missing %q:\n%s", must, out)
		}
	}
	if strings.Index(out, "{\n\temail") > strings.Index(out, "(letsencrypt)") {
		t.Error("the global options block must come first")
	}
}

// TestLEHostsAndSites checks the Let's Encrypt site file: one https site per app (dashboard at
// the bare domain, the rest on services.json subdomains), the dns/staging tls block, the
// Nextcloud/Pi-hole extras carried over, http->https redirects, the :80 info page — and no
// private-CA site anywhere.
func TestLEHostsAndSites(t *testing.T) {
	c := leTestConfig()
	svcs := []Service{
		{Key: "vault", HTTPSPort: 8443},
		{Key: "cloud", HTTPSPort: 8444, Host: "files"}, // a renamed tile
		{Key: "pihole", HTTPSPort: 8445, Path: "/admin"},
	}
	hosts := leHostsFor(c, svcs)
	want := map[string]string{
		"home": "home.example-lab.dev", "vault": "vault.home.example-lab.dev",
		"cloud": "files.home.example-lab.dev", "pihole": "pihole.home.example-lab.dev",
		"pdf": "pdf.home.example-lab.dev", // not in services.json -> falls back to the key
	}
	for k, h := range want {
		if got := hostFor(hosts, k); got != h {
			t.Errorf("host for %s = %q, want %q", k, got, h)
		}
	}
	if svcs[1].DomainURL(c.Domain) != "https://files.home.example-lab.dev" {
		t.Errorf("DomainURL = %q", svcs[1].DomainURL(c.Domain))
	}
	if svcs[2].DomainURL(c.Domain) != "https://pihole.home.example-lab.dev/admin" {
		t.Errorf("DomainURL with path = %q", svcs[2].DomainURL(c.Domain))
	}
	if c.appURL(svcs[2]) != "https://pihole.home.example-lab.dev/admin" || c.dashboardURL() != "https://home.example-lab.dev" {
		t.Errorf("appURL/dashboardURL in LE mode: %q %q", c.appURL(svcs[2]), c.dashboardURL())
	}
	off := c
	off.LetsEncrypt = false
	if off.appURL(svcs[2]) != "https://192.168.0.150:8445/admin" || off.dashboardURL() != "https://192.168.0.150" {
		t.Errorf("appURL/dashboardURL in private-CA mode: %q %q", off.appURL(svcs[2]), off.dashboardURL())
	}

	out := renderLEConfig(c, hosts)
	for _, must := range []string{
		"dns cloudflare {env.ACME_DNS_TOKEN}",
		"resolvers 1.1.1.1 9.9.9.9",
		":80 {\n\theader Content-Type \"text/html; charset=utf-8\"",
		"https://home.example-lab.dev\"><b>https://home.example-lab.dev</b></a>",
		"https://home.example-lab.dev {\n\timport letsencrypt\n\treverse_proxy {$HOME_UPSTREAM}\n}",
		"https://files.home.example-lab.dev {",
		"redir /.well-known/carddav /remote.php/dav 301",
		"Strict-Transport-Security",
		"https://pihole.home.example-lab.dev {\n\timport letsencrypt\n\treverse_proxy {$PIHOLE_UPSTREAM}\n\tredir / /admin/ 302\n}",
		"http://vault.home.example-lab.dev {\n\tredir https://{host}{uri} 308\n}",
	} {
		if !strings.Contains(out, must) {
			t.Errorf("generated sites missing %q:\n%s", must, out)
		}
	}
	for _, mustNot := range []string{"tok$en", "acme-staging", "tls internal", "{$SERVER_IP}:", "root.crt"} {
		if strings.Contains(out, mustNot) {
			t.Errorf("Let's Encrypt config must not contain %q:\n%s", mustNot, out)
		}
	}

	// staging switches the CA; the http challenge drops the dns line but keeps the snippet for `ca`.
	c.ACMEStaging, c.ACMEChallenge = true, challengeHTTP
	out = renderLEConfig(c, hosts)
	if !strings.Contains(out, "ca "+leStagingCA) || strings.Contains(out, "dns cloudflare") {
		t.Errorf("staging/http rendering wrong:\n%s", out)
	}
	// http challenge, production CA: no tls block at all -> plain sites, no dangling import.
	c.ACMEStaging = false
	out = renderLEConfig(c, hosts)
	if strings.Contains(out, "import letsencrypt") || strings.Contains(out, "(letsencrypt)") {
		t.Errorf("http+production must not emit an empty snippet:\n%s", out)
	}
}

// TestSetManagedBlock: the Pi-hole custom.list block is replaced in place, appended when new,
// removed when emptied — and the user's own lines survive every case.
func TestSetManagedBlock(t *testing.T) {
	user := "# my records\n10.0.0.5 nas.lan\n"
	b, e := piholeBlockBegin, piholeBlockEnd

	got := setManagedBlock(user, b, e, "1.2.3.4 a.example\n")
	if !strings.HasPrefix(got, user) || !strings.Contains(got, b+"\n1.2.3.4 a.example\n"+e+"\n") {
		t.Fatalf("append: %q", got)
	}
	got2 := setManagedBlock(got+"10.0.0.6 printer.lan\n", b, e, "1.2.3.4 b.example\n")
	if strings.Contains(got2, "a.example") || !strings.Contains(got2, "b.example") ||
		!strings.Contains(got2, "10.0.0.5 nas.lan") || !strings.Contains(got2, "10.0.0.6 printer.lan") {
		t.Fatalf("replace: %q", got2)
	}
	if strings.Count(got2, b) != 1 {
		t.Fatalf("block duplicated: %q", got2)
	}
	got3 := setManagedBlock(got2, b, e, "")
	if strings.Contains(got3, b) || strings.Contains(got3, "b.example") ||
		!strings.Contains(got3, "nas.lan") || !strings.Contains(got3, "printer.lan") {
		t.Fatalf("remove: %q", got3)
	}
	// Idempotent: applying the same body twice changes nothing.
	if again := setManagedBlock(got, b, e, "1.2.3.4 a.example\n"); again != got {
		t.Fatalf("not idempotent:\n%q\n%q", got, again)
	}
}

// TestRenderTLSRoundTrip exercises renderTLS against a temp repo: caddy/.env gains the keys (the
// token compose-escaped) and CADDY_CONFIG only in Let's Encrypt mode, the generated config exists
// only then, Pi-hole's records follow, and setup.conf never carries the token.
func TestRenderTLSRoundTrip(t *testing.T) {
	repo := t.TempDir()
	for _, d := range []string{"caddy", "pihole"} {
		if err := os.MkdirAll(filepath.Join(repo, d), 0700); err != nil {
			t.Fatal(err)
		}
	}
	caddyEnv := filepath.Join(repo, "caddy", ".env")
	if err := os.WriteFile(caddyEnv, []byte("SERVER_IP=192.168.0.150\nACME_EMAIL=you@example.com\nHOME_HTTPS=443\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gen := filepath.Join(repo, leConfigFile)

	// Private-CA mode first (what a fresh setup does): nothing generated, no overrides.
	c := leTestConfig()
	c.LetsEncrypt = false
	var log strings.Builder
	if err := renderTLS(repo, c, &log); err != nil {
		t.Fatal(err)
	}
	if fileExists(gen) {
		t.Fatal("private-CA mode must not generate a config")
	}
	if kv, _ := readKV(caddyEnv); kv["CADDY_CONFIG"] != "" || kv["CADDY_IMAGE"] != "" || kv["LETSENCRYPT"] != "false" {
		t.Errorf("private-CA mode caddy/.env: %v", kv)
	}

	// Switch to Let's Encrypt.
	c.LetsEncrypt = true
	log.Reset()
	if err := renderTLS(repo, c, &log); err != nil {
		t.Fatal(err)
	}
	if !fileExists(gen) {
		t.Fatal("LE mode must generate the config")
	}
	kv, _ := readKV(caddyEnv)
	for k, want := range map[string]string{
		"DOMAIN": c.Domain, "LETSENCRYPT": "true", "ACME_CHALLENGE": "dns", "ACME_DNS_PROVIDER": "cloudflare",
		"ACME_EMAIL": "admin@home-net.dev", "CADDY_IMAGE": "homeserver-caddy:cloudflare", "SERVER_IP": "192.168.0.150",
		"CADDY_CONFIG": leConfigInCaddy,
	} {
		if kv[k] != want {
			t.Errorf("caddy/.env %s = %q, want %q", k, kv[k], want)
		}
	}
	raw, _ := os.ReadFile(caddyEnv)
	if !strings.Contains(string(raw), "ACME_DNS_TOKEN=tok$$en") {
		t.Errorf("token should be compose-escaped in caddy/.env:\n%s", raw)
	}
	list, _ := os.ReadFile(filepath.Join(repo, "pihole", "custom.list"))
	if !strings.Contains(string(list), "192.168.0.150 vault.home.example-lab.dev") {
		t.Errorf("pihole records missing:\n%s", list)
	}
	// Second run: nothing to do.
	log.Reset()
	if err := renderTLS(repo, c, &log); err != nil || log.Len() != 0 {
		t.Errorf("second render should be silent, got err=%v log=%q", err, log.String())
	}
	// setup.conf: settings, never the token.
	if err := c.Save(repo); err != nil {
		t.Fatal(err)
	}
	conf, _ := os.ReadFile(filepath.Join(repo, confFile))
	if strings.Contains(string(conf), "tok") || !strings.Contains(string(conf), "DOMAIN=home.example-lab.dev") {
		t.Errorf("setup.conf wrong:\n%s", conf)
	}
	// LoadConfig sees the deployed values (token from caddy/.env, the rest from setup.conf).
	got := LoadConfig(repo)
	if got.DNSToken != "tok$en" || !got.LetsEncrypt || got.Domain != c.Domain || got.DNSProvider != "cloudflare" {
		t.Errorf("LoadConfig round-trip: %+v", got)
	}

	// Back to the private CA: the generated config, the records and both overrides go; the
	// token stays for next time.
	c.LetsEncrypt = false
	log.Reset()
	if err := renderTLS(repo, c, &log); err != nil {
		t.Fatal(err)
	}
	if fileExists(gen) || !strings.Contains(log.String(), "removed "+leConfigFile) {
		t.Errorf("after disable: generated exists=%v log=%q", fileExists(gen), log.String())
	}
	kv, _ = readKV(caddyEnv)
	if kv["LETSENCRYPT"] != "false" || kv["CADDY_IMAGE"] != "" || kv["CADDY_CONFIG"] != "" || kv["ACME_DNS_TOKEN"] != "tok$$en" {
		t.Errorf("caddy/.env after disable: %v", kv)
	}
	list, _ = os.ReadFile(filepath.Join(repo, "pihole", "custom.list"))
	if strings.Contains(string(list), "vault.home") {
		t.Errorf("pihole records should be removed:\n%s", list)
	}
}

// TestAppURLsFollowLE: Vaultwarden's DOMAIN and Nextcloud's trusted domains switch with the mode.
func TestAppURLsFollowLE(t *testing.T) {
	c := leTestConfig()
	hosts := leHostsFor(c, nil)
	if got := vwDomainFor(c, hosts, 8443); got != "https://vault.home.example-lab.dev" {
		t.Errorf("vw on: %q", got)
	}
	if got := ncTrustedDomainsFor(c, hosts); got != "192.168.0.150 cloud.home.example-lab.dev" {
		t.Errorf("nc on: %q", got)
	}
	c.LetsEncrypt = false
	if got := vwDomainFor(c, hosts, 8443); got != "https://192.168.0.150:8443" {
		t.Errorf("vw off: %q", got)
	}
	if got := ncTrustedDomainsFor(c, hosts); got != "192.168.0.150" {
		t.Errorf("nc off: %q", got)
	}
}

// TestOnboardingFor: the served guide drops the certificate passages, unwraps the Let's Encrypt
// ones, and rewrites the IP:port addresses to the names — and stays as written otherwise.
func TestOnboardingFor(t *testing.T) {
	src := "Intro.<!-- private-ca --> Install the cert first.<!-- /private-ca --> Go.\n" +
		"<!-- letsencrypt\nNothing to install.\n/letsencrypt -->\n" +
		"| 🔑 | `https://SERVER_IP:8443` |\n| 🏠 | `https://SERVER_IP` |\nGet it at http://SERVER_IP/ now.\n" +
		"<!-- private-ca -->\n## 1. Install the certificate\nsteps\n<!-- /private-ca -->\n## 2. Passwords\n## 3. Files\n"
	c := leTestConfig()
	hosts := leHostsFor(c, nil)

	le := onboardingFor(src, c, hosts)
	for _, must := range []string{
		"Intro. Go.", "Nothing to install.", "`https://vault.home.example-lab.dev`", "`https://home.example-lab.dev`",
		"Get it at https://home.example-lab.dev/ now.", "## 1. Passwords", "## 2. Files",
	} {
		if !strings.Contains(le, must) {
			t.Errorf("LE guide missing %q:\n%s", must, le)
		}
	}
	for _, mustNot := range []string{"Install the cert", "<!--", "SERVER_IP", "## 3.", "192.168.0.150:8443"} {
		if strings.Contains(le, mustNot) {
			t.Errorf("LE guide must not contain %q:\n%s", mustNot, le)
		}
	}

	c.LetsEncrypt = false
	ca := onboardingFor(src, c, hosts)
	for _, must := range []string{
		"Install the cert first.", "## 1. Install the certificate", "`https://192.168.0.150:8443`",
		"http://192.168.0.150/", "<!-- letsencrypt\nNothing to install.\n/letsencrypt -->", // still a comment: invisible
	} {
		if !strings.Contains(ca, must) {
			t.Errorf("private-CA guide missing %q:\n%s", must, ca)
		}
	}
}

func TestLEStateChanged(t *testing.T) {
	a := leTestConfig()
	if leStateChanged(a, a) {
		t.Error("identical configs must not count as changed")
	}
	b := a
	b.LetsEncrypt = false
	if !leStateChanged(a, b) {
		t.Error("toggling must count")
	}
	b = a
	b.DNSToken = "other"
	if !leStateChanged(a, b) {
		t.Error("a new token must count")
	}
	b = a
	b.TZ = "Europe/Brussels"
	if leStateChanged(a, b) {
		t.Error("unrelated settings must not count")
	}
}

// TestFileSnapshotRestore: a failed apply must put every file back — content, mode, and absence.
func TestFileSnapshotRestore(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, "caddy", "generated"), 0700); err != nil {
		t.Fatal(err)
	}
	env := filepath.Join(repo, "caddy", ".env")
	if err := os.WriteFile(env, []byte("A=1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	snap := snapshotFiles(repo, "caddy/.env", leConfigFile)
	// "apply" changes one and creates the other
	if err := os.WriteFile(env, []byte("A=2\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, leConfigFile), []byte("junk"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := snap.restore(); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(env)
	st, _ := os.Stat(env)
	if string(b) != "A=1\n" || st.Mode().Perm() != 0600 {
		t.Errorf("env not restored: %q mode %v", b, st.Mode().Perm())
	}
	if fileExists(filepath.Join(repo, leConfigFile)) {
		t.Error("a file that didn't exist before must be removed again")
	}
}
