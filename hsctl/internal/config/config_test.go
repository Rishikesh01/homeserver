//go:build integration

package config_test

import (
	"fmt"
	"net"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Rishikesh01/homeserver/hsctl/internal/config"
	"github.com/Rishikesh01/homeserver/hsctl/internal/envfile"
	"github.com/Rishikesh01/homeserver/hsctl/internal/itest"
)

// listen holds a real TCP port open for the rest of the test.
func listen(t *testing.T, port int) {
	t.Helper()
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		t.Skipf("can't listen on :%d here: %v", port, err)
	}
	t.Cleanup(func() { ln.Close() })
}

// TestLocalHost asks the machine itself: the default route's source address, a real timezone, and
// whether something listens on a port.
func TestLocalHost(t *testing.T) {
	h := config.LocalHost{}
	listen(t, 18765)
	cases := []struct {
		name string
		got  any
		want any
	}{
		{"IP is one of this machine's", config.LocalIPv4(h.IP()), true},
		{"IP is IPv4", config.ValidIPv4(h.IP()) == nil, true},
		{"timezone is real", config.ValidTZ(h.Timezone()) == nil, true},
		{"a listening port is busy", h.PortBusy(18765), true},
		{"a free port isn't", h.PortBusy(18766), false},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("%s: got %v, want %v (IP %q, TZ %q)", tc.name, tc.got, tc.want, h.IP(), h.Timezone())
		}
	}
}

// TestStore: the defaults come from the host — the first free dashboard port, Pi-hole beside a
// resolver already on :53 — and what Save writes, Load reads back.
func TestStore(t *testing.T) {
	h := config.LocalHost{}
	cases := []struct {
		name string
		busy []int
		edit func(*config.Settings)
		want func(config.Settings) bool
	}{
		{"defaults", nil, nil, func(c config.Settings) bool {
			return c.ServerIP == h.IP() && c.UIPort == 8088 && c.TZ == h.Timezone() && c.PiholeDNSBind == "0.0.0.0" && c.VWSignupsAllowed
		}},
		{"busy ports skipped", []int{8088, 8089}, nil, func(c config.Settings) bool { return c.UIPort == 8090 }},
		{"resolver on :53 binds Pi-hole to the IP", []int{53}, nil, func(c config.Settings) bool { return c.PiholeDNSBind == h.IP() }},
		{"every field survives", nil, func(c *config.Settings) {
			*c = config.Settings{ServerIP: "10.0.0.9", TZ: "Asia/Kolkata", ACMEEmail: "me@example.com", UIPort: 9000,
				PiholeDNSBind: "10.0.0.9", VWSignupsAllowed: false, DisabledApps: []string{"stirling", "it-tools"}}
		}, func(c config.Settings) bool {
			return c.ServerIP == "10.0.0.9" && c.TZ == "Asia/Kolkata" && c.ACMEEmail == "me@example.com" && c.UIPort == 9000 &&
				c.PiholeDNSBind == "10.0.0.9" && !c.VWSignupsAllowed && slices.Equal(c.DisabledApps, []string{"stirling", "it-tools"})
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, p := range tc.busy {
				listen(t, p)
			}
			s := config.NewStore(itest.Repo(t), h)
			c := s.Loaded()
			if tc.edit != nil {
				tc.edit(&c)
			}
			if err := s.Save(c); err != nil {
				t.Fatal(err)
			}
			// A fresh store, as the next command would have.
			if got := config.NewStore(s.Repo, h).Loaded(); !tc.want(got) {
				t.Errorf("loaded %+v", got)
			}
		})
	}
}

// TestOverlays: settings follow the deployed .env files (so they match a running stack), and a
// saved setup.conf — the admin's next values — wins over both.
func TestOverlays(t *testing.T) {
	deployed := map[string]string{
		"caddy/.env":       "SERVER_IP=10.0.0.5\nACME_EMAIL=ops@example.com\nHOME_UPSTREAM=host.docker.internal:8099\n",
		"vaultwarden/.env": "VW_SIGNUPS_ALLOWED=false\n",
		"pihole/.env":      "TZ=Europe/Berlin\nPIHOLE_DNS_BIND=10.0.0.5\n",
	}
	cases := []struct {
		name      string
		setupConf string
		want      config.Settings
	}{
		{"from the deployed .env files", "", config.Settings{ServerIP: "10.0.0.5", TZ: "Europe/Berlin", ACMEEmail: "ops@example.com",
			UIPort: 8099, PiholeDNSBind: "10.0.0.5", VWSignupsAllowed: false}},
		{"setup.conf wins", "SERVER_IP=10.0.0.9\nTZ_VAL=Asia/Kolkata\nUI_PORT=9000\nVW_SIGNUPS_ALLOWED=true\nPIHOLE_DNS_BIND=\nDISABLED_APPS=pihole, stirling\n",
			config.Settings{ServerIP: "10.0.0.9", TZ: "Asia/Kolkata", ACMEEmail: "ops@example.com", UIPort: 9000,
				PiholeDNSBind: "0.0.0.0", VWSignupsAllowed: true, DisabledApps: []string{"pihole", "stirling"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := itest.Repo(t)
			for f, c := range deployed {
				itest.WriteFile(t, repo.Root, f, c)
			}
			if tc.setupConf != "" {
				itest.WriteFile(t, repo.Root, "setup.conf", tc.setupConf)
			}
			got := config.NewStore(repo, config.LocalHost{}).Loaded()
			if fmt.Sprint(got) != fmt.Sprint(tc.want) {
				t.Errorf("loaded\n %+v\nwant\n %+v", got, tc.want)
			}
		})
	}
}

// TestSetupConfExample: the shipped example pre-seeds cleanly — every value is a plain value, never
// a comment glued onto it — and loads as valid settings.
func TestSetupConfExample(t *testing.T) {
	example := filepath.Join(itest.SourceRoot(t), "setup.conf.example")
	kv, err := envfile.Read(example)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct{ key, want string }{
		{"VW_SIGNUPS_ALLOWED", "true"},
	}
	for _, tc := range cases {
		if kv[tc.key] != tc.want {
			t.Errorf("%s = %q, want %q", tc.key, kv[tc.key], tc.want)
		}
	}
	for k, v := range kv {
		if strings.ContainsAny(v, "# ") {
			t.Errorf("%s=%q carries a comment or space", k, v)
		}
	}
	repo := itest.Repo(t)
	itest.WriteFile(t, repo.Root, "setup.conf", itest.ReadFile(example))
	if err := config.Validate(config.NewStore(repo, config.LocalHost{}).Loaded()); err != nil {
		t.Errorf("the example doesn't load as valid settings: %v", err)
	}
}

// TestApps: switching apps keeps the list in start order and never touches the proxy; the start
// order skips switched-off apps but always ends with Caddy.
func TestApps(t *testing.T) {
	cases := []struct {
		name         string
		ops          [][2]any // dir, disabled
		wantDisabled []string
		wantEnabled  string
	}{
		{"none", nil, nil, "vaultwarden,nextcloud,pihole,stirling,it-tools,imagetools,caddy"},
		{"start order kept", [][2]any{{"it-tools", true}, {"vaultwarden", true}}, []string{"vaultwarden", "it-tools"},
			"nextcloud,pihole,stirling,imagetools,caddy"},
		{"toggle back", [][2]any{{"it-tools", true}, {"vaultwarden", true}, {"vaultwarden", false}}, []string{"it-tools"},
			"vaultwarden,nextcloud,pihole,stirling,imagetools,caddy"},
		{"proxy and junk ignored", [][2]any{{"caddy", true}, {"../etc", true}}, nil,
			"vaultwarden,nextcloud,pihole,stirling,it-tools,imagetools,caddy"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var c config.Settings
			for _, op := range tc.ops {
				c.SetDisabled(op[0].(string), op[1].(bool))
			}
			if !slices.Equal(c.DisabledApps, tc.wantDisabled) {
				t.Errorf("DisabledApps = %v, want %v", c.DisabledApps, tc.wantDisabled)
			}
			for _, d := range tc.wantDisabled {
				if !c.IsDisabled(d) {
					t.Errorf("IsDisabled(%s) = false", d)
				}
			}
			if got := strings.Join(c.Enabled(), ","); got != tc.wantEnabled {
				t.Errorf("Enabled = %s, want %s", got, tc.wantEnabled)
			}
		})
	}
}

// TestValidate refuses each kind of bad value before anything is written, naming every problem.
func TestValidate(t *testing.T) {
	good := config.Settings{ServerIP: "10.0.0.5", TZ: "Etc/UTC", UIPort: 8088, PiholeDNSBind: "0.0.0.0"}
	cases := []struct {
		name string
		edit func(*config.Settings)
		want []string // "" = valid
	}{
		{"good", func(*config.Settings) {}, nil},
		{"bad ip", func(c *config.Settings) { c.ServerIP = "not-an-ip" }, []string{`"not-an-ip" isn't an IPv4 address`}},
		{"ipv6", func(c *config.Settings) { c.ServerIP = "fe80::1" }, []string{"isn't an IPv4 address"}},
		{"bad tz", func(c *config.Settings) { c.TZ = "Mars/Olympus" }, []string{"isn't a timezone name"}},
		{"Local isn't a zone", func(c *config.Settings) { c.TZ = "Local" }, []string{"isn't a timezone name"}},
		{"bad port", func(c *config.Settings) { c.UIPort = 70000 }, []string{"isn't a port number"}},
		{"bad bind", func(c *config.Settings) { c.PiholeDNSBind = "x" }, []string{`"x" isn't an IPv4 address`}},
		{"unknown app", func(c *config.Settings) { c.DisabledApps = []string{"nope"} }, []string{`unknown app "nope"`}},
		{"several at once", func(c *config.Settings) { c.ServerIP, c.TZ = "x", "y" }, []string{`"x" isn't`, `"y" isn't`}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := good
			tc.edit(&c)
			err := config.Validate(c)
			if (err == nil) != (tc.want == nil) {
				t.Fatalf("Validate = %v, want errors %v", err, tc.want)
			}
			for _, w := range tc.want {
				if !strings.Contains(err.Error(), w) || !strings.Contains(err.Error(), "nothing was written") {
					t.Errorf("error lacks %q: %v", w, err)
				}
			}
		})
	}
}

// TestParsers are the small parsers setup.conf and the .env files go through.
func TestParsers(t *testing.T) {
	cases := []struct {
		name string
		got  any
		want any
	}{
		{"port from upstream", config.PortFromUpstream("host.docker.internal:8082"), 8082},
		{"port from junk", config.PortFromUpstream("nope"), 0},
		{"csv", strings.Join(config.SplitCSV(" a, b,,c "), "|"), "a|b|c"},
		{"empty csv", len(config.SplitCSV("")), 0},
		{"atoi", config.AtoiDef(" 42 ", 7), 42},
		{"atoi junk", config.AtoiDef("x", 7), 7},
		{"atoi zero", config.AtoiDef("0", 7), 7},
		{"bool true", config.BoolStr(true, "yes", "no"), "yes"},
		{"bool false", config.BoolStr(false, "yes", "no"), "no"},
		{"loopback is local", config.LocalIPv4("127.0.0.1"), true},
		{"documentation address isn't", config.LocalIPv4("203.0.113.9"), false},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("%s = %v, want %v", tc.name, tc.got, tc.want)
		}
	}
}
