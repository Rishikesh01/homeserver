package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestMain keeps these tests off Docker: whatever stack runs on this machine (a contributor's
// live server, say) must never be reloaded or restarted by `go test`.
func TestMain(m *testing.M) {
	containerRunning = func(string, string) bool { return false }
	os.Exit(m.Run())
}

func TestNormalizeDomain(t *testing.T) {
	for in, want := range map[string]string{
		"home.example.org":   "home.example.org",
		" Home.Example.ORG.": "home.example.org",
		"example.co.uk":      "example.co.uk",
		"x-1.duckdns.org":    "x-1.duckdns.org",
	} {
		if got, err := normalizeDomain(in); err != nil || got != want {
			t.Errorf("normalizeDomain(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "localhost", "192.168.1.10", "*.example.org", "nas.local", "home.lan",
		"box.internal", "-bad.example.org", "bad_.example.org", "a..b", "home.arpa"} {
		if got, err := normalizeDomain(bad); err == nil {
			t.Errorf("normalizeDomain(%q) = %q, want an error", bad, got)
		}
	}
}

func TestAcmeEmail(t *testing.T) {
	for in, want := range map[string]string{
		"":                    "",
		"you@example.com":     "", // the shipped placeholder — Let's Encrypt rejects it
		" Me@Example.COM ":    "",
		"me@home.example.org": "me@home.example.org",
	} {
		if got := acmeEmail(in); got != want {
			t.Errorf("acmeEmail(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestDomainSitesRoundTrip: what applyDomain writes is what activePublic (and so Config's
// ActiveDomain/ActivePublic) reads back; no file = nothing public.
func TestDomainSitesRoundTrip(t *testing.T) {
	repo := t.TempDir()
	if d, apps := activePublic(repo); d != "" || apps != nil {
		t.Fatalf("activePublic with no file = %q %v", d, apps)
	}
	os.MkdirAll(filepath.Join(repo, "caddy"), 0755)
	content := domainSitesContent("home.example.org", []string{"vaultwarden", "it-tools"})
	if !strings.HasPrefix(content, "#") || !strings.Contains(content, "\nimport public_vaultwarden home.example.org\nimport public_it-tools home.example.org\n") {
		t.Fatalf("domain.caddy content:\n%s", content)
	}
	os.WriteFile(filepath.Join(repo, domainSites), []byte(content), 0644)
	if d, apps := activePublic(repo); d != "home.example.org" || !slices.Equal(apps, []string{"vaultwarden", "it-tools"}) {
		t.Fatalf("activePublic = %q %v", d, apps)
	}
}

// TestPublicSnippetsMirrorCaddyfile guards against drift between the two Caddy files: every
// app's local site in the Caddyfile must have a public_<app dir> snippet in letsencrypt.caddy
// serving the same port with the same handling on the Let's Encrypt certificate, and the
// dashboard (the "home" site) must have none — it's a root shell.
func TestPublicSnippetsMirrorCaddyfile(t *testing.T) {
	read := func(name string) string {
		b, err := os.ReadFile(filepath.Join("..", "caddy", name))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	caddyfile, le := read("Caddyfile"), read("letsencrypt.caddy")
	importOf := func(body string) string {
		for _, l := range strings.Split(body, "\n") {
			if f := strings.Fields(l); len(f) == 2 && f[0] == "import" {
				return f[1]
			}
		}
		return ""
	}
	// local sites: port var -> snippet
	local := map[string]string{}
	for _, m := range regexp.MustCompile(`(?m)^\{\$SERVER_IP\}:\{\$(\w+)\} \{\n((?:\t.*\n)+)\}`).FindAllStringSubmatch(caddyfile, -1) {
		if !strings.Contains(m[2], "\ttls internal\n") {
			t.Errorf("Caddyfile: the %s site doesn't use tls internal", m[1])
		}
		local[m[1]] = importOf(m[2])
	}
	if len(local) < 7 {
		t.Fatalf("found only %d local sites in the Caddyfile — has its layout changed? %v", len(local), local)
	}
	// public snippets: app dir -> (port var, snippet)
	type site struct{ port, snippet string }
	public := map[string]site{}
	for _, m := range regexp.MustCompile(`(?m)^\(public_([\w-]+)\) \{\n\t\{args\[0\]\}:\{\$(\w+)\} \{\n((?:\t\t.*\n)+)\t\}\n\}`).FindAllStringSubmatch(le, -1) {
		if !strings.Contains(m[3], "\t\ttls /letsencrypt/certificates/{args[0]}.crt /letsencrypt/certificates/{args[0]}.key\n") {
			t.Errorf("letsencrypt.caddy: public_%s doesn't load the Let's Encrypt certificate", m[1])
		}
		public[m[1]] = site{m[2], importOf(m[3])}
	}
	for _, dir := range appDirs() {
		p, ok := public[dir]
		if !ok {
			t.Errorf("letsencrypt.caddy: no public_%s snippet — that app couldn't be made public", dir)
			continue
		}
		if local[p.port] != p.snippet {
			t.Errorf("letsencrypt.caddy: public_%s serves {$%s} with %q, but the Caddyfile's local site there uses %q", dir, p.port, p.snippet, local[p.port])
		}
	}
	for dir, p := range public {
		if !validApp(dir) {
			t.Errorf("letsencrypt.caddy: public_%s isn't an app hsctl knows", dir)
		}
		if p.port == "HOME_HTTPS" {
			t.Errorf("letsencrypt.caddy: public_%s would put the dashboard (a root shell) on the internet", dir)
		}
	}
	for _, want := range []string{"\nimport letsencrypt.caddy\n", "\nimport domain*.caddy\n"} {
		if !strings.Contains(caddyfile, want) {
			t.Errorf("the Caddyfile no longer has %q — public access would do nothing", strings.TrimSpace(want))
		}
	}
}

func TestValidPublicApps(t *testing.T) {
	if err := validPublicApps([]string{"vaultwarden", "nextcloud"}); err != nil {
		t.Error(err)
	}
	for _, bad := range [][]string{nil, {"caddy"}, {"home"}, {"vaultwarden", "nope"}} {
		if validPublicApps(bad) == nil {
			t.Errorf("validPublicApps(%v) accepted it", bad)
		}
	}
}

func TestPublicNotes(t *testing.T) {
	c := Config{Domain: "home.example.org", PublicApps: []string{"vaultwarden"}, VWSignupsAllowed: true}
	notes := strings.Join(publicNotes(c, nil), "\n")
	if !strings.Contains(notes, "doesn't resolve") || !strings.Contains(notes, "open signups") {
		t.Errorf("unresolved + open signups:\n%s", notes)
	}
	if n := strings.Join(publicNotes(c, []net.IP{net.ParseIP("192.168.1.10")}), "\n"); !strings.Contains(n, "private address") {
		t.Errorf("a LAN address should be flagged:\n%s", n)
	}
	c.VWSignupsAllowed = false
	if n := publicNotes(c, []net.IP{net.ParseIP("203.0.113.7")}); len(n) != 0 {
		t.Errorf("public IP, signups off — nothing to say, got %v", n)
	}
	c.PublicApps, c.VWSignupsAllowed = []string{"nextcloud"}, true
	if n := publicNotes(c, []net.IP{net.ParseIP("203.0.113.7")}); len(n) != 0 {
		t.Errorf("signups only matter when Vaultwarden is public, got %v", n)
	}
}

func TestLegoRunArgs(t *testing.T) {
	c := Config{Domain: "home.example.org", DNSProvider: "cloudflare", ACMEEmail: "you@example.com"}
	args := legoRunArgs(c, 1000, 1000, false, false)
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"compose run --rm -T --user 1000:1000 lego run --accept-tos",
		"--path /letsencrypt", "--env-file /acme.env", "--dns cloudflare",
		"--domains home.example.org --domains *.home.example.org", "--force-cert-domains", "--no-random-sleep",
		"--dns.resolvers " + acmeResolvers,
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("args missing %q:\n%s", want, joined)
		}
	}
	for _, not := range []string{"--email", "--server", "--renew-force"} {
		if slices.Contains(args, not) {
			t.Errorf("args shouldn't contain %s here:\n%s", not, joined)
		}
	}

	c.ACMEEmail, c.ACMEServer = "me@home.example.org", "letsencrypt-staging"
	joined = strings.Join(legoRunArgs(c, 0, 0, true, true), " ")
	for _, want := range []string{"--email me@home.example.org", "--server letsencrypt-staging", "--renew-force"} {
		if !strings.Contains(joined, want) {
			t.Errorf("args missing %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "--no-random-sleep") {
		t.Error("an unattended (background) renewal should keep lego's random delay")
	}
}

func TestParseTrustedDomains(t *testing.T) {
	d, next := parseTrustedDomains(`["192.168.1.10","home.example.org"]`)
	if !slices.Equal(d, []string{"192.168.1.10", "home.example.org"}) || next != 2 {
		t.Errorf("list: %v, %d", d, next)
	}
	d, next = parseTrustedDomains(`{"0":"192.168.1.10","3":"old.example.org"}`)
	if !slices.Equal(d, []string{"192.168.1.10", "old.example.org"}) || next != 4 {
		t.Errorf("sparse object: %v, %d", d, next)
	}
	if d, next = parseTrustedDomains(""); len(d) != 0 || next != 0 {
		t.Errorf("empty: %v, %d", d, next)
	}
}

func TestParseCredentialLines(t *testing.T) {
	got, err := parseCredentialLines("\n# comment\nCF_DNS_API_TOKEN = abc=123 \r\nDUCKDNS_TOKEN=x\n")
	if err != nil {
		t.Fatal(err)
	}
	if got["CF_DNS_API_TOKEN"] != "abc=123" || got["DUCKDNS_TOKEN"] != "x" || len(got) != 2 {
		t.Errorf("got %v", got)
	}
	for _, bad := range []string{"sEcReTtOkEn", "lower_case=sEcReTtOkEn", "EMPTY=", "1ABC=sEcReTtOkEn"} {
		if _, err := parseCredentialLines(bad); err == nil {
			t.Errorf("parseCredentialLines(%q) accepted it", bad)
		} else if strings.Contains(err.Error(), "sEcReTtOkEn") {
			t.Errorf("the error echoes what was typed (it ends up in a URL): %v", err)
		}
	}
}

func TestConfigDomainRoundTrip(t *testing.T) {
	repo := t.TempDir()
	c := Config{ServerIP: "192.168.1.10", TZ: "Etc/UTC", UIPort: 8088, Domain: "home.example.org",
		DNSProvider: "cloudflare", ACMEServer: "letsencrypt-staging", PublicApps: []string{"vaultwarden", "nextcloud"}}
	if err := c.Save(repo); err != nil {
		t.Fatal(err)
	}
	got := LoadConfig(repo)
	if got.Domain != c.Domain || got.DNSProvider != c.DNSProvider || got.ACMEServer != c.ACMEServer || !slices.Equal(got.PublicApps, c.PublicApps) {
		t.Errorf("round trip: %+v", got)
	}
	if got.ActiveDomain != "" || got.IsPublic("vaultwarden") {
		t.Errorf("no domain.caddy yet, but ActiveDomain=%q, vaultwarden public=%v", got.ActiveDomain, got.IsPublic("vaultwarden"))
	}
	os.MkdirAll(filepath.Join(repo, "caddy"), 0755)
	os.WriteFile(filepath.Join(repo, domainSites), []byte(domainSitesContent("home.example.org", []string{"vaultwarden"})), 0644)
	if got = LoadConfig(repo); !got.IsPublic("vaultwarden") || got.IsPublic("nextcloud") {
		t.Errorf("with only Vaultwarden switched on: vaultwarden=%v nextcloud=%v", got.IsPublic("vaultwarden"), got.IsPublic("nextcloud"))
	}
}

// writeTestCert writes a self-signed certificate + key where lego would put domain's.
func writeTestCert(t *testing.T, repo, domain, issuerCN string, notAfter time.Time) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: issuerCN},
		DNSNames: []string{domain, "*." + domain}, NotBefore: notAfter.Add(-90 * 24 * time.Hour), NotAfter: notAfter}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	crt, keyPath := certFiles(repo, domain)
	os.MkdirAll(filepath.Dir(crt), 0700)
	if err := os.WriteFile(crt, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}
	kb, _ := x509.MarshalECPrivateKey(key)
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestCertStatus(t *testing.T) {
	repo := t.TempDir()
	c := Config{ServerIP: "192.168.1.10", Domain: "home.example.org", DNSProvider: "cloudflare"}
	if st := loadCertStatus(repo, c); st.Present || st.HasCreds || st.Expiring() {
		t.Errorf("empty repo: %+v", st)
	}
	os.WriteFile(filepath.Join(repo, acmeEnvFile), []byte("# template\nCF_DNS_API_TOKEN=\n"), 0600)
	if st := loadCertStatus(repo, c); st.HasCreds {
		t.Error("an empty KEY= line counted as credentials")
	}
	os.WriteFile(filepath.Join(repo, acmeEnvFile), []byte("CF_DNS_API_TOKEN=abc\n"), 0600)
	writeTestCert(t, repo, "home.example.org", "(STAGING) Pseudo Plum E5", time.Now().Add(10*24*time.Hour+time.Hour))
	st := loadCertStatus(repo, c)
	if !st.HasCreds || !st.Present || st.Err != "" || !st.Staging || st.DaysLeft() != 10 || !st.Expiring() {
		t.Errorf("status: %+v days=%d", st, st.DaysLeft())
	}
	if !slices.Equal(st.Names, []string{"home.example.org", "*.home.example.org"}) {
		t.Errorf("names = %v", st.Names)
	}
	var b strings.Builder
	printCertStatus(&b, st, c.ServerIP)
	if !strings.Contains(b.String(), "STAGING") || !strings.Contains(b.String(), "renewals are failing") {
		t.Errorf("printed status:\n%s", b.String())
	}
}

// TestApplyAndReconcileDomain runs the file side of switching the domain on and off (Docker
// isn't needed: with no caddy/vaultwarden containers, the reloads and restarts are skipped).
func TestApplyAndReconcileDomain(t *testing.T) {
	repo := t.TempDir()
	for _, d := range []string{"caddy", "vaultwarden", "nextcloud"} {
		os.MkdirAll(filepath.Join(repo, d), 0755)
		os.WriteFile(filepath.Join(repo, d, "docker-compose.yml"), nil, 0644)
	}
	os.WriteFile(filepath.Join(repo, "caddy/.env"), []byte("SERVER_IP=192.168.1.10\nVAULT_HTTPS=9443\n"), 0600)
	os.WriteFile(filepath.Join(repo, "vaultwarden/.env"), []byte("VW_DOMAIN=https://192.168.1.10:9443\nVW_ADMIN_TOKEN=x\n"), 0600)
	os.WriteFile(filepath.Join(repo, "nextcloud/.env"), []byte("NC_TRUSTED_DOMAINS=192.168.1.10\n"), 0600)
	c := Config{ServerIP: "192.168.1.10", Domain: "home.example.org", DNSProvider: "cloudflare", PublicApps: []string{"nextcloud"}}
	if err := c.Save(repo); err != nil {
		t.Fatal(err)
	}

	if err := applyDomain(repo, c, false, io.Discard); err == nil {
		t.Fatal("applyDomain without a certificate should refuse")
	}
	if fileExists(filepath.Join(repo, domainSites)) {
		t.Fatal("domain.caddy written without a certificate — Caddy would fail to start")
	}

	writeTestCert(t, repo, "home.example.org", "E7", time.Now().Add(90*24*time.Hour))
	// Only Nextcloud public: Vaultwarden keeps its local address.
	if err := applyDomain(repo, c, false, io.Discard); err != nil {
		t.Fatal(err)
	}
	if d, apps := activePublic(repo); d != "home.example.org" || !slices.Equal(apps, []string{"nextcloud"}) {
		t.Errorf("public = %q %v", d, apps)
	}
	vw, _ := readKV(filepath.Join(repo, "vaultwarden/.env"))
	if vw["VW_DOMAIN"] != "https://192.168.1.10:9443" {
		t.Errorf("Vaultwarden isn't public, yet VW_DOMAIN = %q", vw["VW_DOMAIN"])
	}
	// Vaultwarden made public too: its one canonical address becomes the public one.
	c.PublicApps = []string{"vaultwarden", "nextcloud"}
	if err := applyDomain(repo, c, false, io.Discard); err != nil {
		t.Fatal(err)
	}
	vw, _ = readKV(filepath.Join(repo, "vaultwarden/.env"))
	if vw["VW_DOMAIN"] != "https://home.example.org:9443" || vw["VW_ADMIN_TOKEN"] != "x" {
		t.Errorf("vaultwarden/.env = %v", vw)
	}
	nc, _ := readKV(filepath.Join(repo, "nextcloud/.env"))
	if nc["NC_TRUSTED_DOMAINS"] != "192.168.1.10 home.example.org" {
		t.Errorf("NC_TRUSTED_DOMAINS = %q", nc["NC_TRUSTED_DOMAINS"])
	}
	// Idempotent: a second run (every renewal check does one) changes nothing.
	if err := applyDomain(repo, c, false, io.Discard); err != nil {
		t.Fatal(err)
	}
	if nc, _ = readKV(filepath.Join(repo, "nextcloud/.env")); nc["NC_TRUSTED_DOMAINS"] != "192.168.1.10 home.example.org" {
		t.Errorf("second apply: NC_TRUSTED_DOMAINS = %q", nc["NC_TRUSTED_DOMAINS"])
	}

	// The certificate vanishes (letsencrypt/ deleted): the next `up` must not start Caddy on it.
	os.RemoveAll(filepath.Join(repo, certDirName))
	reconcileDomain(repo, io.Discard)
	if fileExists(filepath.Join(repo, domainSites)) {
		t.Error("reconcileDomain left domain.caddy pointing at a missing certificate")
	}
	if vw, _ = readKV(filepath.Join(repo, "vaultwarden/.env")); vw["VW_DOMAIN"] != "https://192.168.1.10:9443" {
		t.Errorf("VW_DOMAIN not moved back to the IP: %q", vw["VW_DOMAIN"])
	}

	// A domain.caddy hsctl can't read (hand-edited, another version's format) goes too.
	os.WriteFile(filepath.Join(repo, domainSites), []byte("import letsencrypt.caddy home.example.org\n"), 0644)
	reconcileDomain(repo, io.Discard)
	if fileExists(filepath.Join(repo, domainSites)) {
		t.Error("reconcileDomain kept a domain.caddy it couldn't parse")
	}
}

// TestGenerateFollowsPublicApps: a (re)generated vaultwarden/nextcloud .env uses the public
// domain for exactly the apps Caddy serves publicly, so `setup --force` can't point a public
// Vaultwarden back at the IP behind its back — nor a local-only one at the domain.
func TestGenerateFollowsPublicApps(t *testing.T) {
	for _, tc := range []struct {
		public     []string
		vw, ncHost string
	}{
		{nil, "https://192.168.1.10:8443", "192.168.1.10"},
		{[]string{"vaultwarden"}, "https://home.example.org:8443", "192.168.1.10"},
		{[]string{"nextcloud"}, "https://192.168.1.10:8443", "192.168.1.10 home.example.org"},
	} {
		repo := t.TempDir()
		for _, d := range []string{"caddy", "vaultwarden", "nextcloud", "pihole"} {
			os.MkdirAll(filepath.Join(repo, d), 0755)
		}
		c := Config{ServerIP: "192.168.1.10", TZ: "Etc/UTC", UIPort: 8088, PiholeDNSBind: "0.0.0.0"}
		if tc.public != nil {
			c.ActiveDomain, c.ActivePublic = "home.example.org", tc.public
		}
		if _, err := c.Generate(repo, false); err != nil {
			t.Fatal(err)
		}
		vw, _ := readKV(filepath.Join(repo, "vaultwarden/.env"))
		nc, _ := readKV(filepath.Join(repo, "nextcloud/.env"))
		if vw["VW_DOMAIN"] != tc.vw || nc["NC_TRUSTED_DOMAINS"] != tc.ncHost {
			t.Errorf("public %v: VW_DOMAIN=%q NC_TRUSTED_DOMAINS=%q", tc.public, vw["VW_DOMAIN"], nc["NC_TRUSTED_DOMAINS"])
		}
	}
}

func TestIsStaging(t *testing.T) {
	if !isStagingCert(&x509.Certificate{Issuer: pkix.Name{CommonName: "(STAGING) Ersatz Edamame E1"}}) ||
		isStagingCert(&x509.Certificate{Issuer: pkix.Name{CommonName: "E7"}}) {
		t.Error("isStagingCert")
	}
	if !isStagingServer("letsencrypt-staging") || !isStagingServer("https://acme-staging-v02.api.letsencrypt.org/directory") || isStagingServer("") {
		t.Error("isStagingServer")
	}
}

func TestClearUnregisteredAccounts(t *testing.T) {
	repo := t.TempDir()
	mk := func(server, id, body string) string {
		dir := filepath.Join(repo, certDirName, "accounts", server, id)
		os.MkdirAll(dir, 0700)
		os.WriteFile(filepath.Join(dir, "account.json"), []byte(body), 0600)
		os.WriteFile(filepath.Join(dir, id+".key"), []byte("key"), 0600)
		return dir
	}
	stub := mk("acme-v02.api.letsencrypt.org", "noemail@example.com", `{"id":"noemail@example.com","registration": null}`)
	real := mk("acme-staging-v02.api.letsencrypt.org", "me@example.org",
		`{"id":"me@example.org","registration":{"status":"valid","accountURL":"https://x/acct/1"}}`)
	clearUnregisteredAccounts(repo, io.Discard)
	if fileExists(stub) {
		t.Error("the never-registered account is still there — lego would keep failing on it")
	}
	if !fileExists(filepath.Join(real, "me@example.org.key")) {
		t.Error("a registered account was deleted")
	}
}
