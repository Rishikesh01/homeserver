//go:build integration

package setup_test

import (
	"bufio"
	"bytes"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/Rishikesh01/homeserver/hsctl/internal/config"
	"github.com/Rishikesh01/homeserver/hsctl/internal/docker"
	"github.com/Rishikesh01/homeserver/hsctl/internal/envfile"
	"github.com/Rishikesh01/homeserver/hsctl/internal/itest"
	"github.com/Rishikesh01/homeserver/hsctl/internal/paths"
	"github.com/Rishikesh01/homeserver/hsctl/internal/priv"
	"github.com/Rishikesh01/homeserver/hsctl/internal/prompt"
	"github.com/Rishikesh01/homeserver/hsctl/internal/provision"
	"github.com/Rishikesh01/homeserver/hsctl/internal/setup"
)

func str(s string) *string { return &s }

// newSetup wires setup for repo; input, when not nil, is what the person at the terminal types.
func newSetup(repo paths.Repo, input *string) (*setup.Service, *bytes.Buffer) {
	var out bytes.Buffer
	dk := docker.New(repo.Root)
	dk.Stdout, dk.Stderr = &out, &out
	prov := provision.New(repo, dk)
	prov.Out, prov.Err = &out, &out
	term := &prompt.Terminal{Out: &out, Interactive: func() bool { return false }}
	if input != nil {
		term = &prompt.Terminal{In: bufio.NewReader(strings.NewReader(*input)), Out: &out}
	}
	s := setup.New(repo, config.NewStore(repo, config.LocalHost{}), prov, term, dk, priv.Root{})
	s.Out, s.Err = &out, &out
	return s, &out
}

func env(repo paths.Repo, rel, key string) string {
	kv, _ := envfile.Read(repo.Path(rel))
	return kv[key]
}

// TestRun: setup settles the settings from flags (or questions), saves them, writes the config and
// says what's next — and refuses bad input before writing anything.
func TestRun(t *testing.T) {
	cases := []struct {
		name    string
		before  []setup.Options // earlier runs
		input   *string         // a terminal session, if any
		opts    setup.Options
		wantErr string
		wantOut []string
		check   func(t *testing.T, repo paths.Repo)
	}{
		{"first setup from flags", nil, nil, setup.Options{Yes: true, ServerIP: str("10.0.0.5"), TZ: str("Asia/Kolkata"),
			DisabledApps: &[]string{"stirling", "pihole"}}, "",
			[]string{"NEW LOGINS", "Vaultwarden /admin token", "Next: hsctl up   then   sudo hsctl install", "isn't an address of this machine"},
			func(t *testing.T, repo paths.Repo) {
				c := config.NewStore(repo, config.LocalHost{}).Load()
				if c.ServerIP != "10.0.0.5" || c.TZ != "Asia/Kolkata" || !slices.Equal(c.DisabledApps, []string{"pihole", "stirling"}) {
					t.Errorf("saved %+v", c)
				}
				if env(repo, "nextcloud/.env", "NC_TRUSTED_DOMAINS") != "10.0.0.5" || env(repo, "pihole/.env", "TZ") != "Asia/Kolkata" {
					t.Error("the .env files don't follow the flags")
				}
			}},
		{"nothing to change", []setup.Options{{Yes: true, ServerIP: str("10.0.0.5")}}, nil, setup.Options{Yes: true}, "",
			[]string{"Nothing to change"}, nil},
		{"a changed setting is applied", []setup.Options{{Yes: true, ServerIP: str("10.0.0.5")}}, nil,
			setup.Options{Yes: true, VWSignups: new(bool)}, "",
			[]string{"Applied your changes", "VW_SIGNUPS_ALLOWED true -> false", "Next: hsctl up   (recreates"},
			func(t *testing.T, repo paths.Repo) {
				if env(repo, "vaultwarden/.env", "VW_SIGNUPS_ALLOWED") != "false" {
					t.Error("not applied")
				}
			}},
		{"a new IP moves a Pi-hole bound to the old one", []setup.Options{{Yes: true, ServerIP: str("10.0.0.5"), PiholeDNSBind: str("10.0.0.5")}}, nil,
			setup.Options{Yes: true, ServerIP: str("10.0.0.9")}, "", []string{"PIHOLE_DNS_BIND 10.0.0.5 -> 0.0.0.0", "Nextcloud isn't running"},
			func(t *testing.T, repo paths.Repo) {
				if env(repo, "caddy/.env", "SERVER_IP") != "10.0.0.9" {
					t.Error("Caddy doesn't follow the new IP")
				}
			}},
		{"bad IP", nil, nil, setup.Options{Yes: true, ServerIP: str("not-an-ip")}, "nothing was written", nil,
			func(t *testing.T, repo paths.Repo) {
				if itest.ReadFile(repo.Path(paths.SetupConf)) != "" || itest.ReadFile(repo.Path("caddy/.env")) != "" {
					t.Error("something was written")
				}
			}},
		{"unknown app", nil, nil, setup.Options{Yes: true, DisabledApps: &[]string{"bogus"}}, `--disable-apps: unknown app "bogus"`, nil, nil},
		{"force needs --yes off a terminal", []setup.Options{{Yes: true}}, nil, setup.Options{Force: true}, "add --yes", nil, nil},
		{"force aborted at the prompt", []setup.Options{{Yes: true}}, str("nope\n"), setup.Options{Force: true},
			"", []string{"Type REGENERATE to continue", "aborted — nothing was changed"}, nil},
		{"force confirmed", []setup.Options{{Yes: true}}, str("REGENERATE\n\n\n\n\n\n\n"), setup.Options{Force: true}, "",
			[]string{"NEW LOGINS", "Pi-hole admin"}, nil},
		{"questions on a terminal", nil, str("10.0.0.7\nMars/Base\nEurope/Berlin\n9000\n\nn\nstirling,bogus\nstirling\n"), setup.Options{}, "",
			[]string{"== Configure", `isn't a timezone name`, `unknown app "bogus"`},
			func(t *testing.T, repo paths.Repo) {
				c := config.NewStore(repo, config.LocalHost{}).Load()
				if c.ServerIP != "10.0.0.7" || c.TZ != "Europe/Berlin" || c.UIPort != 9000 || c.VWSignupsAllowed ||
					!slices.Equal(c.DisabledApps, []string{"stirling"}) || c.PiholeDNSBind != "0.0.0.0" {
					t.Errorf("saved %+v", c)
				}
			}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			itest.NeedDocker(t)
			repo := itest.Checkout(t)
			for _, o := range tc.before {
				s, out := newSetup(repo, nil)
				if err := s.Run(o); err != nil {
					t.Fatalf("earlier run: %v\n%s", err, out.String())
				}
			}
			before := itest.ReadFile(repo.Path("vaultwarden/.env"))
			s, out := newSetup(repo, tc.input)
			err := s.Run(tc.opts)
			if (err == nil) != (tc.wantErr == "") || (err != nil && !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("Run = %v, want %q\n%s", err, tc.wantErr, out.String())
			}
			for _, w := range tc.wantOut {
				if !strings.Contains(out.String(), w) {
					t.Errorf("output lacks %q:\n%s", w, out.String())
				}
			}
			if strings.Contains(tc.name, "aborted") && itest.ReadFile(repo.Path("vaultwarden/.env")) != before {
				t.Error("an aborted --force changed secrets")
			}
			if tc.check != nil {
				tc.check(t, repo)
			}
		})
	}
}

// TestMoveDashboard: a new dashboard port reaches the running pieces — the dashboard service is
// restarted, and the running Caddy recreated so it forwards to the new port.
func TestMoveDashboard(t *testing.T) {
	itest.NeedRoot(t)
	itest.NeedDocker(t)
	cases := []struct {
		name      string
		active    bool
		wantCalls []string
		wantOut   string
	}{
		{"dashboard service running", true, []string{"is-active hsctl-ui.service", "restart hsctl-ui.service"}, "restarted the dashboard service"},
		{"no dashboard service", false, []string{"is-active hsctl-ui.service"}, "the dashboard service isn't running"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sc := itest.FakeSystemctl(t)
			sc.SetActive(tc.active)
			repo := itest.Checkout(t)
			s, out := newSetup(repo, nil)
			s.DashboardActive = func() bool { return activeService() }
			if err := s.Run(setup.Options{Yes: true, ServerIP: str("10.0.0.5")}); err != nil {
				t.Fatal(err)
			}
			c := docker.New(repo.Root)
			if err := c.EnsureNetwork("homeserver-edge"); err != nil {
				t.Fatal(err)
			}
			if o, err := c.Combined(repo.Path("caddy"), "compose", "up", "-d", "--quiet-pull"); err != nil {
				t.Fatalf("start caddy: %v\n%s", err, o)
			}
			out.Reset()
			// The admin edits setup.conf, then re-runs setup.
			if err := envfile.Set(repo.Path(paths.SetupConf), "UI_PORT", "9123"); err != nil {
				t.Fatal(err)
			}
			if err := s.Run(setup.Options{Yes: true}); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), "the dashboard moves to port 9123") || !strings.Contains(out.String(), tc.wantOut) ||
				!strings.Contains(out.String(), "recreated Caddy") {
				t.Errorf("output:\n%s", out.String())
			}
			if got := itest.Docker(t, "inspect", "-f", "{{range .Config.Env}}{{println .}}{{end}}", "caddy"); !strings.Contains(got, "HOME_UPSTREAM=host.docker.internal:9123") {
				t.Errorf("Caddy wasn't recreated with the new port:\n%s", got)
			}
			calls := sc.Calls()
			for _, w := range tc.wantCalls {
				if !slices.Contains(calls, w) {
					t.Errorf("systemctl calls %v lack %q", calls, w)
				}
			}
			if !tc.active && slices.Contains(calls, "restart hsctl-ui.service") {
				t.Error("restarted a dashboard that isn't running")
			}
		})
	}
}

// activeService asks systemctl (the fake on PATH) whether the dashboard runs, as install does.
func activeService() bool {
	out, _ := priv.Root{}.Command("systemctl", "is-active", "hsctl-ui.service").Output()
	return strings.TrimSpace(string(out)) == "active"
}

// TestDeployed: what the stack runs with comes from caddy/.env, not from a setup.conf that may
// already hold the next values.
func TestDeployed(t *testing.T) {
	cases := []struct {
		name, caddyEnv string
		wantIP         string
		wantPort       int
	}{
		{"not set up", "", "10.0.0.9", 9000},
		{"running stack", "SERVER_IP=10.0.0.5\nHOME_UPSTREAM=host.docker.internal:8088\n", "10.0.0.5", 8088},
		{"hand-set upstream keeps the port", "SERVER_IP=10.0.0.5\nHOME_UPSTREAM=dashboard\n", "10.0.0.5", 9000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := itest.Repo(t)
			if tc.caddyEnv != "" {
				itest.WriteFile(t, repo.Root, "caddy/.env", tc.caddyEnv)
			}
			ip, port := setup.Deployed(repo, config.Settings{ServerIP: "10.0.0.9", UIPort: 9000})
			if ip != tc.wantIP || port != tc.wantPort {
				t.Errorf("Deployed = %s %d, want %s %d", ip, port, tc.wantIP, tc.wantPort)
			}
		})
	}
}

// TestUnreadable runs as a normal user: a setup.conf left root-owned stops setup before it could
// save autodetected values over the real settings.
func TestUnreadable(t *testing.T) {
	if !itest.Unprivileged(t) {
		return
	}
	cases := []string{paths.SetupConf, "caddy/.env"}
	for _, rel := range cases {
		repo := itest.Repo(t)
		itest.WriteFile(t, repo.Root, rel, "SERVER_IP=10.0.0.5\n")
		if err := os.Chmod(repo.Path(rel), 0); err != nil {
			t.Fatal(err)
		}
		s, _ := newSetup(repo, nil)
		if err := s.Run(setup.Options{Yes: true}); err == nil || !strings.Contains(err.Error(), "can't read "+rel) {
			t.Errorf("%s: %v", rel, err)
		}
	}
}
