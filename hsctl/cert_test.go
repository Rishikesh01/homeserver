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

// TestDomainSitesRoundTrip: what applyDomain writes is what activeDomain (and so Config's
// ActiveDomain) reads back; no file = no domain.
func TestDomainSitesRoundTrip(t *testing.T) {
	repo := t.TempDir()
	if got := activeDomain(repo); got != "" {
		t.Fatalf("activeDomain with no file = %q", got)
	}
	os.MkdirAll(filepath.Join(repo, "caddy"), 0755)
	if err := os.WriteFile(filepath.Join(repo, domainSites), []byte(domainSitesContent("home.example.org")), 0644); err != nil {
		t.Fatal(err)
	}
	if got := activeDomain(repo); got != "home.example.org" {
		t.Fatalf("activeDomain = %q", got)
	}
}

// TestLetsencryptSitesMirrorCaddyfile guards against drift between the two Caddy files: every
// app site the Caddyfile serves at the IP must have a twin in letsencrypt.caddy on the same
// port with the same handling, using the Let's Encrypt certificate — and nothing more.
func TestLetsencryptSitesMirrorCaddyfile(t *testing.T) {
	sites := func(path, addr string) map[string]string {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		// "<addr>:{$PORT} {" … "import <snippet>" … "}" — port var -> snippet.
		re := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(addr) + `:\{\$(\w+)\} \{\n((?:\t.*\n)+)\}`)
		out := map[string]string{}
		for _, m := range re.FindAllStringSubmatch(string(b), -1) {
			body := m[2]
			var snippet string
			for _, l := range strings.Split(body, "\n") {
				if f := strings.Fields(l); len(f) == 2 && f[0] == "import" {
					snippet = f[1]
				}
			}
			if addr == "{args[0]}" && !strings.Contains(body, "\ttls /letsencrypt/certificates/{args[0]}.crt /letsencrypt/certificates/{args[0]}.key\n") {
				t.Errorf("letsencrypt.caddy: the %s site doesn't load the Let's Encrypt certificate", m[1])
			}
			if addr == "{$SERVER_IP}" && !strings.Contains(body, "\ttls internal\n") {
				t.Errorf("Caddyfile: the %s site doesn't use tls internal", m[1])
			}
			out[m[1]] = snippet
		}
		return out
	}
	ip := sites(filepath.Join("..", "caddy", "Caddyfile"), "{$SERVER_IP}")
	le := sites(filepath.Join("..", "caddy", "letsencrypt.caddy"), "{args[0]}")
	if len(ip) < 7 {
		t.Fatalf("found only %d IP sites in the Caddyfile — has its layout changed? %v", len(ip), ip)
	}
	for port, snippet := range ip {
		if snippet == "" {
			t.Errorf("Caddyfile: the %s site doesn't import a per-app snippet", port)
		}
		if le[port] != snippet {
			t.Errorf("letsencrypt.caddy: the %s site imports %q, want %q (its twin in the Caddyfile)", port, le[port], snippet)
		}
	}
	for port := range le {
		if _, ok := ip[port]; !ok {
			t.Errorf("letsencrypt.caddy: %s has no IP site in the Caddyfile", port)
		}
	}
	caddyfile, _ := os.ReadFile(filepath.Join("..", "caddy", "Caddyfile"))
	if !strings.Contains(string(caddyfile), "\nimport domain*.caddy\n") {
		t.Error("the Caddyfile no longer imports domain*.caddy — the domain switch would do nothing")
	}
	if !strings.HasPrefix(domainSitesContent("x.example.org"), "#") || !strings.Contains(domainSitesContent("x.example.org"), "\nimport letsencrypt.caddy x.example.org\n") {
		t.Error("domainSitesContent doesn't import letsencrypt.caddy with the domain")
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
		DNSProvider: "cloudflare", ACMEServer: "letsencrypt-staging"}
	if err := c.Save(repo); err != nil {
		t.Fatal(err)
	}
	got := LoadConfig(repo)
	if got.Domain != c.Domain || got.DNSProvider != c.DNSProvider || got.ACMEServer != c.ACMEServer {
		t.Errorf("round trip: %+v", got)
	}
	if got.ActiveDomain != "" || got.Host() != "192.168.1.10" {
		t.Errorf("no domain.caddy yet, but ActiveDomain=%q Host=%q", got.ActiveDomain, got.Host())
	}
	os.MkdirAll(filepath.Join(repo, "caddy"), 0755)
	os.WriteFile(filepath.Join(repo, domainSites), []byte(domainSitesContent("home.example.org")), 0644)
	if got = LoadConfig(repo); got.Host() != "home.example.org" {
		t.Errorf("Host() = %q with the domain switched on", got.Host())
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
	c := Config{ServerIP: "192.168.1.10", Domain: "home.example.org", DNSProvider: "cloudflare"}
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
	if err := applyDomain(repo, c, false, io.Discard); err != nil {
		t.Fatal(err)
	}
	if got := activeDomain(repo); got != "home.example.org" {
		t.Errorf("active domain = %q", got)
	}
	vw, _ := readKV(filepath.Join(repo, "vaultwarden/.env"))
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
}

// TestGenerateUsesActiveDomain: a (re)generated vaultwarden/nextcloud .env follows the domain
// Caddy serves, so `setup --force` can't point Vaultwarden back at the IP behind its back.
func TestGenerateUsesActiveDomain(t *testing.T) {
	repo := t.TempDir()
	for _, d := range []string{"caddy", "vaultwarden", "nextcloud", "pihole"} {
		os.MkdirAll(filepath.Join(repo, d), 0755)
	}
	c := Config{ServerIP: "192.168.1.10", TZ: "Etc/UTC", UIPort: 8088, PiholeDNSBind: "0.0.0.0", ActiveDomain: "home.example.org"}
	if _, err := c.Generate(repo, false); err != nil {
		t.Fatal(err)
	}
	vw, _ := readKV(filepath.Join(repo, "vaultwarden/.env"))
	nc, _ := readKV(filepath.Join(repo, "nextcloud/.env"))
	if vw["VW_DOMAIN"] != "https://home.example.org:8443" || nc["NC_TRUSTED_DOMAINS"] != "192.168.1.10 home.example.org" {
		t.Errorf("VW_DOMAIN=%q NC_TRUSTED_DOMAINS=%q", vw["VW_DOMAIN"], nc["NC_TRUSTED_DOMAINS"])
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
