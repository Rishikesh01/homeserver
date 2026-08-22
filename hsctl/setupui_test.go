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
	base := Config{ACMEEmail: "existing@example.com", UIPort: 8088}
	form := func(vals url.Values) (Config, error) {
		r := httptest.NewRequest("POST", "/setup", strings.NewReader(vals.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		return parseSetupForm(base, r)
	}
	good := url.Values{"server_ip": {"192.168.1.20"}, "tz": {"Asia/Kolkata"},
		"pihole_dns_bind": {"0.0.0.0"}, "vw_signups": {"on"}, "apps": {"vaultwarden", "nextcloud", "pihole", "imagetools"}}
	c, err := form(good)
	if err != nil {
		t.Fatalf("valid form rejected: %v", err)
	}
	if c.ServerIP != "192.168.1.20" || c.TZ != "Asia/Kolkata" || c.ACMEEmail != "existing@example.com" || !c.VWSignupsAllowed || c.UIPort != 8088 {
		t.Errorf("fields not applied: %+v", c)
	}
	// Unticked apps become the disabled list, in start order.
	if got := strings.Join(c.DisabledApps, ","); got != "stirling,it-tools" {
		t.Errorf("disabled apps = %q, want stirling,it-tools", got)
	}
	noApps := url.Values{"server_ip": {"10.0.0.2"}, "tz": {"UTC"}}
	if _, err := form(noApps); err == nil {
		t.Error("a submit with every app unticked must be rejected")
	}
	bad := map[string]url.Values{
		"ip":   {"server_ip": {"not-an-ip"}, "tz": {"UTC"}},
		"ipv6": {"server_ip": {"fe80::1"}, "tz": {"UTC"}},
		"tz":   {"server_ip": {"10.0.0.2"}, "tz": {"Europe Brussels"}},
		"dns":  {"server_ip": {"10.0.0.2"}, "tz": {"UTC"}, "pihole_dns_bind": {"x"}},
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
	if w.Code != 200 || !strings.Contains(w.Body.String(), `name="server_ip"`) || strings.Contains(w.Body.String(), `name="email"`) {
		t.Fatalf("GET /setup should render the form, got %d", w.Code)
	}

	vals := url.Values{"server_ip": {"10.1.2.3"}, "tz": {"Asia/Kolkata"}, "vw_signups": {"on"},
		"apps": {"vaultwarden", "nextcloud", "pihole", "stirling", "imagetools"}}
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
	if kv, _ := readKV(filepath.Join(repo, "caddy/.env")); kv["SERVER_IP"] != "10.1.2.3" || kv["ACME_EMAIL"] != "you@example.com" || kv["VAULT_HTTPS"] != "8443" {
		t.Errorf("caddy/.env not reconciled (or clobbered): %v", kv)
	}
	if kv, _ := readKV(filepath.Join(repo, "nextcloud/.env")); kv["NC_TRUSTED_DOMAINS"] != "10.1.2.3" {
		t.Errorf("confirmed IP not used for the apps: %v", kv)
	}
	if !s.setupDone() {
		t.Fatal("setup should be done after the POST")
	}
	// The switch is persisted and honoured by the start order + home tiles.
	saved := LoadConfig(repo)
	if strings.Join(saved.DisabledApps, ",") != "it-tools" {
		t.Errorf("DISABLED_APPS not saved: %v", saved.DisabledApps)
	}
	if got := strings.Join(enabledServices(saved), ","); got != "vaultwarden,nextcloud,pihole,stirling,imagetools,caddy" {
		t.Errorf("enabledServices = %s", got)
	}
	if err := os.WriteFile(filepath.Join(repo, "services.json"), []byte(`[{"key":"tools","name":"Utilities","icon":"x","desc":"d","https_port":8447,"dir":"it-tools"},{"key":"pdf","name":"PDF tools","icon":"x","desc":"d","https_port":8446,"dir":"stirling"}]`), 0644); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	s.handleHome(w, httptest.NewRequest("GET", "/", nil))
	if b := w.Body.String(); strings.Contains(b, "Utilities") || !strings.Contains(b, "PDF tools") {
		t.Error("home page must hide disabled apps' tiles and keep the enabled ones")
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
	w = httptest.NewRecorder()
	s.handleSetupUpStatus(w, httptest.NewRequest("POST", "/setup/up/status", nil))
	if w.Code != 405 {
		t.Errorf("POST /setup/up/status must be rejected, got %d", w.Code)
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

func TestAppSwitchConfig(t *testing.T) {
	var c Config
	c.setDisabled("it-tools", true)
	c.setDisabled("vaultwarden", true)
	c.setDisabled("caddy", true) // not an app: ignored
	if got := strings.Join(c.DisabledApps, ","); got != "vaultwarden,it-tools" {
		t.Errorf("setDisabled order = %q", got)
	}
	c.setDisabled("vaultwarden", false)
	if !c.IsDisabled("it-tools") || c.IsDisabled("vaultwarden") {
		t.Errorf("toggle back failed: %v", c.DisabledApps)
	}
	repo := setupTestRepo(t)
	c.ServerIP, c.UIPort = "10.0.0.1", 8088
	if err := c.Save(repo); err != nil {
		t.Fatal(err)
	}
	if got := LoadConfig(repo).DisabledApps; strings.Join(got, ",") != "it-tools" {
		t.Errorf("round-trip via setup.conf = %v", got)
	}
}

// The toggle endpoint must reject anything outside the registry BEFORE touching docker.
func TestHandleAppsToggleValidation(t *testing.T) {
	s := &uiServer{repo: setupTestRepo(t)}
	post := func(body string) int {
		r := httptest.NewRequest("POST", "/admin/apps/toggle", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		s.handleAppsToggle(w, r)
		return w.Code
	}
	if c := post("app=../../etc&action=disable"); c != 400 {
		t.Errorf("bad app accepted: %d", c)
	}
	if c := post("app=caddy&action=disable"); c != 400 {
		t.Errorf("caddy must not be switchable: %d", c)
	}
	if c := post("app=stirling&action=rm"); c != 400 {
		t.Errorf("bad action accepted: %d", c)
	}
	w := httptest.NewRecorder()
	s.handleAppsToggle(w, httptest.NewRequest("GET", "/admin/apps/toggle", nil))
	if w.Code != 303 {
		t.Errorf("GET must redirect, got %d", w.Code)
	}
}
