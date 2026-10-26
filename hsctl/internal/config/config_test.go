package config

import (
	"slices"
	"strings"
	"testing"

	"github.com/Rishikesh01/homeserver/hsctl/internal/envfile"
	"github.com/Rishikesh01/homeserver/hsctl/internal/paths"
)

// fixedHost is a host with known answers, so the store's defaults are predictable.
type fixedHost struct{ busy map[int]bool }

func (fixedHost) IP() string            { return "10.0.0.5" }
func (fixedHost) Timezone() string      { return "Europe/Berlin" }
func (h fixedHost) PortBusy(p int) bool { return h.busy[p] }

// TestSetupConfExampleParses: the shipped example must pre-seed cleanly — every value it sets is
// a plain value, never a comment glued onto it.
func TestSetupConfExampleParses(t *testing.T) {
	kv, err := envfile.Read("../../../setup.conf.example")
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range kv {
		if strings.ContainsAny(v, "# ") {
			t.Errorf("%s=%q carries a comment or space", k, v)
		}
	}
	if kv["VW_SIGNUPS_ALLOWED"] != "true" {
		t.Errorf("VW_SIGNUPS_ALLOWED = %q, want true", kv["VW_SIGNUPS_ALLOWED"])
	}
}

// TestStoreRoundTrip: what Save writes, Load reads back; defaults come from the host and the
// first free dashboard port.
func TestStoreRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		busy map[int]bool
		edit func(*Settings)
		want func(Settings) bool
	}{
		{"defaults", nil, nil, func(c Settings) bool { return c.ServerIP == "10.0.0.5" && c.UIPort == 8088 && c.TZ == "Europe/Berlin" }},
		{"busy port skipped", map[int]bool{8088: true, 8089: true}, nil, func(c Settings) bool { return c.UIPort == 8090 }},
		{"disabled apps survive", nil, func(c *Settings) { c.SetDisabled("it-tools", true) },
			func(c Settings) bool { return slices.Equal(c.DisabledApps, []string{"it-tools"}) }},
		{"resolver on :53 binds pi-hole to the IP", map[int]bool{53: true}, nil, func(c Settings) bool { return c.PiholeDNSBind == "10.0.0.5" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := NewStore(paths.Repo{Root: t.TempDir()}, fixedHost{tc.busy})
			c := s.Loaded()
			if tc.edit != nil {
				tc.edit(&c)
			}
			if err := s.Save(c); err != nil {
				t.Fatal(err)
			}
			if got := s.Loaded(); !tc.want(got) {
				t.Errorf("loaded %+v", got)
			}
		})
	}
}

// TestSetDisabled keeps the list in start order and ignores the proxy.
func TestSetDisabled(t *testing.T) {
	var c Settings
	c.SetDisabled("it-tools", true)
	c.SetDisabled("vaultwarden", true)
	c.SetDisabled("caddy", true) // not an app: ignored
	if got := strings.Join(c.DisabledApps, ","); got != "vaultwarden,it-tools" {
		t.Errorf("order = %q", got)
	}
	c.SetDisabled("vaultwarden", false)
	if !c.IsDisabled("it-tools") || c.IsDisabled("vaultwarden") {
		t.Errorf("toggle back failed: %v", c.DisabledApps)
	}
	if e := c.Enabled(); slices.Contains(e, "it-tools") || e[len(e)-1] != "caddy" {
		t.Errorf("Enabled = %v", e)
	}
}

// TestValidate refuses each kind of bad value before anything is written.
func TestValidate(t *testing.T) {
	good := Settings{ServerIP: "10.0.0.5", TZ: "Etc/UTC", UIPort: 8088, PiholeDNSBind: "0.0.0.0"}
	cases := []struct {
		name string
		edit func(*Settings)
		ok   bool
	}{
		{"good", func(*Settings) {}, true},
		{"bad ip", func(c *Settings) { c.ServerIP = "not-an-ip" }, false},
		{"bad tz", func(c *Settings) { c.TZ = "Mars/Olympus" }, false},
		{"bad port", func(c *Settings) { c.UIPort = 70000 }, false},
		{"bad bind", func(c *Settings) { c.PiholeDNSBind = "x" }, false},
		{"unknown app", func(c *Settings) { c.DisabledApps = []string{"nope"} }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := good
			tc.edit(&c)
			if err := Validate(c); (err == nil) != tc.ok {
				t.Errorf("Validate = %v, want ok=%v", err, tc.ok)
			}
		})
	}
}
