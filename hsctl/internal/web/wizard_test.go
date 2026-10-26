//go:build integration

package web_test

import (
	"encoding/json"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Rishikesh01/homeserver/hsctl/internal/config"
	"github.com/Rishikesh01/homeserver/hsctl/internal/docker"
	"github.com/Rishikesh01/homeserver/hsctl/internal/envfile"
	"github.com/Rishikesh01/homeserver/hsctl/internal/itest"
)

// TestWizard walks a fresh install through the setup wizard: the home page sends the admin to it,
// bad values are refused before anything is written, the confirmed settings generate every .env and
// show the new logins once, "Start everything" runs the real `hsctl up` as a job the page can follow
// across reconnects — and afterwards the wizard is closed, so nothing can rotate live secrets.
func TestWizard(t *testing.T) {
	itest.Hsctl(t)
	itest.NeedRoot(t)
	itest.NeedDocker(t)
	repo := itest.Checkout(t)
	// The installer wrote caddy/.env from a guessed IP; the wizard corrects it.
	itest.WriteFile(t, repo.Root, "caddy/.env", "SERVER_IP=192.168.1.10\nACME_EMAIL=you@example.com\nHOME_UPSTREAM=host.docker.internal:8088\nVAULT_HTTPS=8443\n")
	d := newDash(t, repo, nil)
	d.login()
	form := url.Values{"server_ip": {"10.1.2.3"}, "tz": {"Asia/Kolkata"}, "pihole_dns_bind": {"0.0.0.0"},
		"apps": {"vaultwarden", "imagetools"}}
	steps := []struct {
		name     string
		do       func() (int, string)
		wantCode int
		want     []string
		check    func() string // "" = fine
	}{
		{"home sends a fresh install to the wizard", func() (int, string) {
			resp, body := d.get("/")
			if resp.Header.Get("Location") != "/setup" {
				return 0, body
			}
			return resp.StatusCode, body
		}, 303, nil, nil},
		{"the form", func() (int, string) { resp, body := d.get("/setup"); return resp.StatusCode, body },
			200, []string{`name="server_ip"`, `name="apps"`, "Etc/UTC"}, nil},
		{"a bad IP is refused", func() (int, string) {
			bad := url.Values{"server_ip": {"fe80::1"}, "tz": {"UTC"}, "apps": {"vaultwarden"}}
			resp, body := d.post("/setup", bad)
			return resp.StatusCode, body
		}, 400, []string{"isn&#39;t a valid IPv4 address"}, func() string {
			if itest.ReadFile(repo.Path("vaultwarden/.env")) != "" {
				return "a refused form wrote config"
			}
			return ""
		}},
		{"every app switched off is refused", func() (int, string) {
			resp, body := d.post("/setup", url.Values{"server_ip": {"10.1.2.3"}, "tz": {"UTC"}})
			return resp.StatusCode, body
		}, 400, nil, nil},
		{"confirm", func() (int, string) { resp, body := d.post("/setup", form); return resp.StatusCode, body },
			200, []string{"Vaultwarden /admin token", "Nextcloud (user &#39;admin&#39;)", "Pi-hole admin", `id="start"`}, func() string {
				kv, _ := envfile.Read(repo.Path("caddy/.env"))
				if kv["SERVER_IP"] != "10.1.2.3" || kv["VAULT_HTTPS"] != "8443" {
					return "caddy/.env not reconciled (or clobbered)"
				}
				c := config.NewStore(repo, config.LocalHost{}).Load()
				if !slices.Equal(c.DisabledApps, []string{"nextcloud", "pihole", "stirling", "it-tools"}) || c.TZ != "Asia/Kolkata" {
					return "settings not saved"
				}
				return ""
			}},
		{"start everything needs a POST", func() (int, string) { resp, body := d.get("/setup/up"); return resp.StatusCode, body }, 405, nil, nil},
		{"start everything", func() (int, string) { resp, body := d.post("/setup/up", nil); return resp.StatusCode, body }, 202, nil, nil},
		{"a second click while it runs is harmless", func() (int, string) { resp, body := d.post("/setup/up", nil); return resp.StatusCode, body }, 202, nil, nil},
		{"the progress, until it's done", func() (int, string) {
			var st struct {
				Active, Done, OK bool
				Output           string
			}
			deadline := time.Now().Add(5 * time.Minute)
			for time.Now().Before(deadline) {
				resp, body := d.get("/setup/up/status")
				if resp.StatusCode != 200 || json.Unmarshal([]byte(body), &st) != nil {
					return resp.StatusCode, body
				}
				if st.Done {
					break
				}
				time.Sleep(time.Second)
			}
			if !st.OK {
				return 500, st.Output
			}
			return 200, st.Output
		}, 200, []string{"$ hsctl up", "== up: vaultwarden ==", "== up: caddy ==", "[done]"}, func() string {
			for _, c := range []string{"vaultwarden", "imagetools", "caddy"} {
				if docker.New("").ContainerState(c) != "running" {
					return c + " isn't running"
				}
			}
			return ""
		}},
		{"the home page shows the chosen apps", func() (int, string) { resp, body := d.get("/"); return resp.StatusCode, body },
			200, []string{"https://10.1.2.3:8443", "https://10.1.2.3:8448"}, func() string {
				_, body := d.get("/")
				if strings.Contains(body, ":8447") {
					return "a switched-off app has a tile"
				}
				return ""
			}},
		{"the wizard is closed", func() (int, string) {
			before := itest.ReadFile(repo.Path("vaultwarden/.env"))
			resp, body := d.post("/setup", form)
			if itest.ReadFile(repo.Path("vaultwarden/.env")) != before {
				return 500, "a second POST rotated the secrets"
			}
			if !strings.HasPrefix(resp.Header.Get("Location"), "/admin") {
				return 0, body
			}
			return resp.StatusCode, body
		}, 303, nil, nil},
	}
	for _, st := range steps {
		code, body := st.do()
		if code != st.wantCode {
			t.Fatalf("%s: %d\n%s", st.name, code, body)
		}
		for _, w := range st.want {
			if !strings.Contains(body, w) {
				t.Fatalf("%s: lacks %q\n%s", st.name, w, body)
			}
		}
		if st.check != nil {
			if msg := st.check(); msg != "" {
				t.Fatalf("%s: %s", st.name, msg)
			}
		}
	}
}

// TestWizardForm: the Pi-hole bind is pre-filled from the detected IP; correcting the IP but leaving
// the bind alone must not keep Pi-hole on an address the server no longer has.
func TestWizardForm(t *testing.T) {
	cases := []struct {
		name, ip, bind, want string
	}{
		{"ip corrected, bind untouched", "10.0.0.9", "10.0.0.5", "0.0.0.0"},
		{"ip corrected, bind chosen", "10.0.0.9", "10.0.0.9", "10.0.0.9"},
		{"ip kept", "10.0.0.5", "10.0.0.5", "10.0.0.5"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := itest.Checkout(t)
			store := config.NewStore(repo, config.LocalHost{})
			c := store.Loaded()
			c.ServerIP, c.PiholeDNSBind = "10.0.0.5", "10.0.0.5"
			if err := store.Save(c); err != nil {
				t.Fatal(err)
			}
			d := newDash(t, repo, nil)
			d.login()
			resp, body := d.post("/setup", url.Values{"server_ip": {tc.ip}, "tz": {"Etc/UTC"}, "pihole_dns_bind": {tc.bind}, "apps": {"vaultwarden"}})
			if resp.StatusCode != 200 {
				t.Fatalf("%d %s", resp.StatusCode, body)
			}
			if got := config.NewStore(repo, config.LocalHost{}).Load().PiholeDNSBind; got != tc.want {
				t.Errorf("bind = %q, want %q", got, tc.want)
			}
		})
	}
}
