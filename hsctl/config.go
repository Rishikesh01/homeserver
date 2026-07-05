package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// Config is everything a user can configure. It round-trips to setup.conf as
// KEY=value pairs.
type Config struct {
	ServerIP  string
	TZ        string
	ACMEEmail string
	// UIPort is the hsctl web UI port. It's the one app port that's still a host port: the
	// dashboard runs on the host (not a container), so Caddy reaches it via the host gateway.
	// The other apps moved onto the shared "edge" network and no longer publish HTTP ports.
	UIPort           int
	PiholeDNSBind    string
	VWSignupsAllowed bool
}

const confFile = "setup.conf"

var (
	defaultsOnce   sync.Once
	cachedDefaults Config
)

// Defaults returns a config seeded from autodetected host values. PiholeDNSBind is left
// blank and filled by Normalize AFTER any overrides, so it follows the final IP.
//
// The detection shells out (ip route, timedatectl, ss) and LoadConfig runs on every dashboard
// request, so we compute it once per process: the host IP/timezone/free port don't change under
// a running server, and any saved SERVER_IP/UI_PORT in setup.conf overrides these anyway.
func Defaults() Config {
	defaultsOnce.Do(func() { cachedDefaults = detectDefaults() })
	return cachedDefaults
}

func detectDefaults() Config {
	ip := detectIP()
	if ip == "" {
		ip = "192.168.1.10"
	}
	c := Config{
		ServerIP:         ip,
		TZ:               detectTZ(),
		ACMEEmail:        "you@example.com",
		VWSignupsAllowed: true,
	}
	c.UIPort = pickPort(8088)
	return c
}

// Normalize fills any derived field left blank, from the (final) ServerIP. Call it
// after applying setup.conf / .env / flag overrides.
func (c *Config) Normalize() {
	if c.PiholeDNSBind == "" {
		c.PiholeDNSBind = "0.0.0.0"
		if portBusy(53) {
			c.PiholeDNSBind = c.ServerIP
		}
	}
	if c.UIPort == 0 {
		c.UIPort = pickPort(8088)
	}
}

// LoadConfig returns Defaults overlaid with the actual deployed .env (so it matches a
// running stack) and then any saved setup.conf. Callers apply flags then Normalize.
func LoadConfig(repo string) Config {
	c := Defaults()
	overlayFromEnv(&c, repo)
	overlayFromConf(&c, repo)
	return c
}

func overlayFromConf(c *Config, repo string) {
	kv, err := readKV(filepath.Join(repo, confFile))
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
	c.UIPort = atoiDef(get("UI_PORT", ""), c.UIPort)
	c.PiholeDNSBind = get("PIHOLE_DNS_BIND", c.PiholeDNSBind)
	c.VWSignupsAllowed = get("VW_SIGNUPS_ALLOWED", boolStr(c.VWSignupsAllowed, "true", "false")) == "true"
}

// overlayFromEnv reflects the actual deployed .env files into c (so config matches a
// live stack, and re-saving setup.conf reconciles any drift).
func overlayFromEnv(c *Config, repo string) {
	if kv, err := readKV(filepath.Join(repo, "caddy/.env")); err == nil {
		if v := kv["ACME_EMAIL"]; v != "" {
			c.ACMEEmail = v
		}
		// The dashboard is still a host process reached via host.docker.internal:<port>.
		if v := portFromUpstream(kv["HOME_UPSTREAM"]); v > 0 {
			c.UIPort = v
		}
	}
	if kv, err := readKV(filepath.Join(repo, "vaultwarden/.env")); err == nil {
		if _, ok := kv["VW_SIGNUPS_ALLOWED"]; ok {
			c.VWSignupsAllowed = kv["VW_SIGNUPS_ALLOWED"] == "true"
		}
	}
	if kv, err := readKV(filepath.Join(repo, "pihole/.env")); err == nil {
		if v := kv["TZ"]; v != "" {
			c.TZ = v
		}
		if v := kv["PIHOLE_DNS_BIND"]; v != "" {
			c.PiholeDNSBind = v
		}
	}
}

// portFromUpstream parses "host.docker.internal:8082" -> 8082.
func portFromUpstream(s string) int {
	if i := strings.LastIndex(s, ":"); i >= 0 {
		return atoiDef(s[i+1:], 0)
	}
	return 0
}

// Save writes setup.conf (0600). Not secrets — just settings.
func (c Config) Save(repo string) error {
	tf := func(b bool) string {
		if b {
			return "true"
		}
		return "false"
	}
	var b strings.Builder
	b.WriteString("# Saved by hsctl — your configuration (NOT secrets). Edit + re-run freely.\n")
	for _, kv := range [][2]string{
		{"SERVER_IP", c.ServerIP}, {"TZ_VAL", c.TZ}, {"ACME_EMAIL", c.ACMEEmail},
		{"UI_PORT", strconv.Itoa(c.UIPort)},
		{"PIHOLE_DNS_BIND", c.PiholeDNSBind},
		{"VW_SIGNUPS_ALLOWED", tf(c.VWSignupsAllowed)},
	} {
		fmt.Fprintf(&b, "%s=%s\n", kv[0], kv[1])
	}
	return writeFile0600(filepath.Join(repo, confFile), b.String())
}

// ---- helpers ----

func readKV(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	m := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		m[strings.TrimSpace(k)] = unquote(strings.TrimSpace(v))
	}
	return m, sc.Err()
}

func unquote(s string) string {
	if len(s) >= 2 {
		if (s[0] == '\'' && s[len(s)-1] == '\'') || (s[0] == '"' && s[len(s)-1] == '"') {
			return s[1 : len(s)-1]
		}
	}
	return s
}

func atoiDef(s string, def int) int {
	if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil && n > 0 {
		return n
	}
	return def
}

func detectIP() string {
	out, err := exec.Command("ip", "-4", "route", "get", "1.1.1.1").Output()
	if err != nil {
		return ""
	}
	f := strings.Fields(string(out))
	for i, w := range f {
		if w == "src" && i+1 < len(f) {
			return f[i+1]
		}
	}
	return ""
}

func detectTZ() string {
	if out, err := exec.Command("timedatectl", "show", "-p", "Timezone", "--value").Output(); err == nil {
		if s := strings.TrimSpace(string(out)); s != "" {
			return s
		}
	}
	if b, err := os.ReadFile("/etc/timezone"); err == nil {
		if s := strings.TrimSpace(string(b)); s != "" {
			return s
		}
	}
	return "Etc/UTC"
}

// portBusy reports whether a TCP port is currently listening (via `ss`).
func portBusy(p int) bool {
	out, err := exec.Command("ss", "-ltn").Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), fmt.Sprintf(":%d ", p))
}

// pickPort returns the first port >= start that isn't currently in use.
func pickPort(start int) int {
	p := start
	for portBusy(p) {
		p++
	}
	return p
}
