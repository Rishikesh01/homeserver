// Package config is the stack's settings: what the admin chose (server IP, timezone, dashboard
// port, which apps run, …), saved as setup.conf, overlaid on what the deployed .env files say and
// on values autodetected from the host.
package config

import (
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/Rishikesh01/homeserver/hsctl/internal/envfile"
	"github.com/Rishikesh01/homeserver/hsctl/internal/fsutil"
	"github.com/Rishikesh01/homeserver/hsctl/internal/layout"
	"github.com/Rishikesh01/homeserver/hsctl/internal/paths"
)

// Settings is everything a user can configure. It round-trips to setup.conf as KEY=value pairs.
type Settings struct {
	ServerIP  string
	TZ        string
	ACMEEmail string
	// UIPort is the dashboard's port. It's the one port that's still a host port: the dashboard
	// runs on the host (not a container), so Caddy reaches it via the docker host gateway.
	UIPort           int
	PiholeDNSBind    string
	VWSignupsAllowed bool
	// DisabledApps are compose folders the admin switched off: `hsctl up` skips them and the
	// dashboard hides their tiles. Their data volumes are kept.
	DisabledApps []string
}

// Host is what the store asks the machine it runs on.
type Host interface {
	IP() string             // the LAN IPv4 the default route uses, "" if unknown
	Timezone() string       // e.g. Europe/Berlin
	PortBusy(port int) bool // something listens on the TCP port
}

// Store loads and saves one repo's settings.
type Store struct {
	Repo paths.Repo
	Host Host

	once     sync.Once
	defaults Settings
}

// NewStore returns the settings store for repo, autodetecting defaults from host.
func NewStore(repo paths.Repo, host Host) *Store { return &Store{Repo: repo, Host: host} }

// Defaults returns settings seeded from autodetected host values. PiholeDNSBind is left blank and
// filled by Normalize AFTER any overrides, so it follows the final IP.
//
// The detection shells out (ip route, timedatectl, ss) and Load runs on every dashboard request,
// so it's computed once per process: the host IP/timezone/free port don't change under a running
// server, and any saved SERVER_IP/UI_PORT in setup.conf overrides these anyway.
func (s *Store) Defaults() Settings {
	s.once.Do(func() {
		ip := s.Host.IP()
		if ip == "" {
			ip = "192.168.1.10"
		}
		s.defaults = Settings{ServerIP: ip, TZ: s.Host.Timezone(), ACMEEmail: "you@example.com",
			VWSignupsAllowed: true, UIPort: s.pickPort(8088)}
	})
	return s.defaults
}

// pickPort returns the first port >= start that isn't currently in use.
func (s *Store) pickPort(start int) int {
	p := start
	for s.Host.PortBusy(p) {
		p++
	}
	return p
}

// DefaultDNSBind picks where Pi-hole's DNS should listen: all interfaces, unless the host already
// has a resolver on :53 — then the LAN IP, so the two can coexist.
func (s *Store) DefaultDNSBind(serverIP string) string {
	if s.Host.PortBusy(53) {
		return serverIP
	}
	return "0.0.0.0"
}

// Normalize fills any derived field left blank, from the (final) ServerIP. Call it after applying
// setup.conf / .env / flag overrides.
func (s *Store) Normalize(c *Settings) {
	if c.PiholeDNSBind == "" {
		c.PiholeDNSBind = s.DefaultDNSBind(c.ServerIP)
	}
	if c.UIPort == 0 {
		c.UIPort = s.pickPort(8088)
	}
}

// Load returns the defaults overlaid with the deployed .env files (so it matches a running stack)
// and then any saved setup.conf. Callers apply flags, then Normalize.
func (s *Store) Load() Settings {
	c := s.Defaults()
	s.overlayFromEnv(&c)
	s.overlayFromConf(&c)
	return c
}

// Loaded is Load followed by Normalize — what most callers want.
func (s *Store) Loaded() Settings {
	c := s.Load()
	s.Normalize(&c)
	return c
}

func (s *Store) overlayFromConf(c *Settings) {
	kv, err := envfile.Read(s.Repo.Path(paths.SetupConf))
	if err != nil {
		return
	}
	get := func(k, def string) string {
		if v, ok := kv[k]; ok {
			return v
		}
		return def
	}
	c.ServerIP = get("SERVER_IP", c.ServerIP)
	c.TZ = get("TZ_VAL", c.TZ)
	c.ACMEEmail = get("ACME_EMAIL", c.ACMEEmail)
	c.UIPort = AtoiDef(get("UI_PORT", ""), c.UIPort)
	c.PiholeDNSBind = get("PIHOLE_DNS_BIND", c.PiholeDNSBind)
	c.VWSignupsAllowed = get("VW_SIGNUPS_ALLOWED", BoolStr(c.VWSignupsAllowed, "true", "false")) == "true"
	c.DisabledApps = SplitCSV(get("DISABLED_APPS", ""))
}

// overlayFromEnv reflects the deployed .env files into c (so settings match a live stack, and
// re-saving setup.conf reconciles any drift).
func (s *Store) overlayFromEnv(c *Settings) {
	if kv, err := envfile.Read(s.Repo.Path("caddy/.env")); err == nil {
		// Caddy issues the certificates for this IP, so it's the deployed server address.
		if v := kv["SERVER_IP"]; v != "" {
			c.ServerIP = v
		}
		if v := kv["ACME_EMAIL"]; v != "" {
			c.ACMEEmail = v
		}
		// The dashboard is a host process reached via host.docker.internal:<port>.
		if v := PortFromUpstream(kv["HOME_UPSTREAM"]); v > 0 {
			c.UIPort = v
		}
	}
	if kv, err := envfile.Read(s.Repo.Path("vaultwarden/.env")); err == nil {
		if _, ok := kv["VW_SIGNUPS_ALLOWED"]; ok {
			c.VWSignupsAllowed = kv["VW_SIGNUPS_ALLOWED"] == "true"
		}
	}
	if kv, err := envfile.Read(s.Repo.Path("pihole/.env")); err == nil {
		if v := kv["TZ"]; v != "" {
			c.TZ = v
		}
		if v := kv["PIHOLE_DNS_BIND"]; v != "" {
			c.PiholeDNSBind = v
		}
	}
}

// Save writes setup.conf (0600). Not secrets — just settings.
func (s *Store) Save(c Settings) error {
	var b strings.Builder
	b.WriteString("# Saved by hsctl — your configuration (NOT secrets). To change a setting, edit it here and\n" +
		"# run `hsctl setup --yes` (or just run `hsctl setup`): that applies it to the .env files.\n")
	for _, kv := range [][2]string{
		{"SERVER_IP", c.ServerIP}, {"TZ_VAL", c.TZ}, {"ACME_EMAIL", c.ACMEEmail},
		{"UI_PORT", strconv.Itoa(c.UIPort)},
		{"PIHOLE_DNS_BIND", c.PiholeDNSBind},
		{"VW_SIGNUPS_ALLOWED", BoolStr(c.VWSignupsAllowed, "true", "false")},
		{"DISABLED_APPS", strings.Join(c.DisabledApps, ",")},
	} {
		fmt.Fprintf(&b, "%s=%s\n", kv[0], kv[1])
	}
	return fsutil.WritePrivate(s.Repo.Path(paths.SetupConf), b.String())
}

// IsDisabled reports whether the app (compose folder) is switched off.
func (c Settings) IsDisabled(dir string) bool {
	for _, d := range c.DisabledApps {
		if d == dir {
			return true
		}
	}
	return false
}

// SetDisabled adds or removes dir from DisabledApps, keeping the list in start order (and ignoring
// anything that isn't an app).
func (c *Settings) SetDisabled(dir string, disabled bool) {
	set := map[string]bool{}
	for _, d := range c.DisabledApps {
		set[d] = true
	}
	set[dir] = disabled
	c.DisabledApps = nil
	for _, d := range layout.Apps() {
		if set[d] {
			c.DisabledApps = append(c.DisabledApps, d)
		}
	}
}

// Enabled is the stack's start order with switched-off apps removed (the proxy always included).
func (c Settings) Enabled() []string {
	var out []string
	for _, s := range layout.Services {
		if s == layout.Proxy || !c.IsDisabled(s) {
			out = append(out, s)
		}
	}
	return out
}

// PortFromUpstream parses "host.docker.internal:8082" -> 8082.
func PortFromUpstream(s string) int {
	if i := strings.LastIndex(s, ":"); i >= 0 {
		return AtoiDef(s[i+1:], 0)
	}
	return 0
}

// SplitCSV parses "a, b,c" into ["a","b","c"], dropping blanks.
func SplitCSV(s string) []string {
	var out []string
	for _, f := range strings.Split(s, ",") {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}

// AtoiDef parses a positive integer, or returns def.
func AtoiDef(s string, def int) int {
	if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil && n > 0 {
		return n
	}
	return def
}

// BoolStr picks t or f.
func BoolStr(b bool, t, f string) string {
	if b {
		return t
	}
	return f
}
