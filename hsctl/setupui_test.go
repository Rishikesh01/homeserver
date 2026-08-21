package main

import (
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setupTestRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	for _, d := range []string{"vaultwarden", "nextcloud", "pihole", "caddy"} {
		if err := os.MkdirAll(filepath.Join(repo, d), 0755); err != nil {
			t.Fatal(err)
		}
	}
	return repo
}

func TestParseSetupFormValidation(t *testing.T) {
	base := Config{UIPort: 8088}
	form := func(vals url.Values) (Config, error) {
		r := httptest.NewRequest("POST", "/setup", strings.NewReader(vals.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		return parseSetupForm(base, r)
	}
	good := url.Values{"server_ip": {"192.168.1.20"}, "tz": {"Asia/Kolkata"}, "email": {"me@example.com"},
		"pihole_dns_bind": {"0.0.0.0"}, "vw_signups": {"on"}}
	c, err := form(good)
	if err != nil {
		t.Fatalf("valid form rejected: %v", err)
	}
	if c.ServerIP != "192.168.1.20" || c.TZ != "Asia/Kolkata" || !c.VWSignupsAllowed || c.UIPort != 8088 {
		t.Errorf("fields not applied: %+v", c)
	}
	bad := map[string]url.Values{
		"ip":   {"server_ip": {"not-an-ip"}, "tz": {"UTC"}, "email": {"a@b"}},
		"ipv6": {"server_ip": {"fe80::1"}, "tz": {"UTC"}, "email": {"a@b"}},
		"tz":   {"server_ip": {"10.0.0.2"}, "tz": {"Europe Brussels"}, "email": {"a@b"}},
		"mail": {"server_ip": {"10.0.0.2"}, "tz": {"UTC"}, "email": {"nope"}},
		"dns":  {"server_ip": {"10.0.0.2"}, "tz": {"UTC"}, "email": {"a@b"}, "pihole_dns_bind": {"x"}},
	}
	for name, v := range bad {
		if _, err := form(v); err == nil {
			t.Errorf("%s: invalid form accepted", name)
		}
	}
	// Unchecked checkbox = signups off (browsers omit the field entirely).
	delete(good, "vw_signups")
	if c, _ := form(good); c.VWSignupsAllowed {
		t.Error("missing vw_signups must mean false")
	}
}

// The wizard's lifecycle: a fresh repo redirects home → /setup, the POST generates every
// core .env + caddy/.env from the confirmed values and reports the new logins, and once
// that's done the wizard is closed (home renders, /setup bounces to admin).
func TestSetupWizardFlow(t *testing.T) {
	repo := setupTestRepo(t)
	s := &uiServer{repo: repo}
	// The bootstrap wrote caddy/.env from a guessed IP; the wizard must correct it.
	if err := os.WriteFile(filepath.Join(repo, "caddy/.env"), []byte("SERVER_IP=192.168.1.10\nACME_EMAIL=you@example.com\nHOME_UPSTREAM=host.docker.internal:8088\nVAULT_HTTPS=8443\n"), 0600); err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	s.handleHome(w, httptest.NewRequest("GET", "/", nil))
	if w.Code != 303 || w.Header().Get("Location") != "/setup" {
		t.Fatalf("unconfigured home must redirect to /setup, got %d %s", w.Code, w.Header().Get("Location"))
	}

	w = httptest.NewRecorder()
	s.handleSetup(w, httptest.NewRequest("GET", "/setup", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `name="server_ip"`) {
		t.Fatalf("GET /setup should render the form, got %d", w.Code)
	}

	vals := url.Values{"server_ip": {"10.1.2.3"}, "tz": {"Asia/Kolkata"}, "email": {"me@example.com"}, "vw_signups": {"on"}}
	r := httptest.NewRequest("POST", "/setup", strings.NewReader(vals.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w = httptest.NewRecorder()
	s.handleSetup(w, r)
	body := w.Body.String()
	if w.Code != 200 {
		t.Fatalf("POST /setup: %d %s", w.Code, body)
	}
	for _, want := range []string{"Nextcloud (user &#39;admin&#39;)", "Pi-hole admin", "Vaultwarden /admin token", `id="start"`} {
		if !strings.Contains(body, want) {
			t.Errorf("step 2 page missing %q", want)
		}
	}
	for _, f := range []string{"vaultwarden/.env", "nextcloud/.env", "pihole/.env", "setup.conf", "WELCOME.txt"} {
		if !fileExists(filepath.Join(repo, f)) {
			t.Errorf("%s not written", f)
		}
	}
	if kv, _ := readKV(filepath.Join(repo, "caddy/.env")); kv["SERVER_IP"] != "10.1.2.3" || kv["ACME_EMAIL"] != "me@example.com" || kv["VAULT_HTTPS"] != "8443" {
		t.Errorf("caddy/.env not reconciled (or clobbered): %v", kv)
	}
	if kv, _ := readKV(filepath.Join(repo, "nextcloud/.env")); kv["NC_TRUSTED_DOMAINS"] != "10.1.2.3" {
		t.Errorf("confirmed IP not used for the apps: %v", kv)
	}
	if !s.setupDone() {
		t.Fatal("setup should be done after the POST")
	}

	// Closed: a second POST must NOT regenerate anything (would rotate live secrets).
	before, _ := os.ReadFile(filepath.Join(repo, "vaultwarden/.env"))
	r = httptest.NewRequest("POST", "/setup", strings.NewReader(vals.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w = httptest.NewRecorder()
	s.handleSetup(w, r)
	if w.Code != 303 || !strings.HasPrefix(w.Header().Get("Location"), "/admin") {
		t.Errorf("finished wizard must bounce to admin, got %d", w.Code)
	}
	after, _ := os.ReadFile(filepath.Join(repo, "vaultwarden/.env"))
	if string(before) != string(after) {
		t.Error("re-POST rotated secrets")
	}
	w = httptest.NewRecorder()
	s.handleSetupUp(w, httptest.NewRequest("GET", "/setup/up", nil))
	if w.Code != 405 {
		t.Errorf("GET /setup/up must be rejected, got %d", w.Code)
	}
}

func TestReconcileCaddyEnvCreatesWhenMissing(t *testing.T) {
	repo := setupTestRepo(t)
	c := Config{ServerIP: "10.0.0.5", ACMEEmail: "a@b", UIPort: 9000}
	if err := reconcileCaddyEnv(repo, c); err != nil {
		t.Fatal(err)
	}
	kv, _ := readKV(filepath.Join(repo, "caddy/.env"))
	if kv["SERVER_IP"] != "10.0.0.5" || kv["HOME_UPSTREAM"] != "host.docker.internal:9000" || kv["HOME_HTTPS"] != "443" {
		t.Errorf("fresh caddy/.env wrong: %v", kv)
	}
}
