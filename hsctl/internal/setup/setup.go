// Package setup is `hsctl setup`: it settles the stack's settings (from setup.conf, the deployed
// .env files, flags and — on a terminal — questions), saves them, generates or updates every
// service's configuration, and follows changes through to the running pieces that need them.
package setup

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/Rishikesh01/homeserver/hsctl/internal/config"
	"github.com/Rishikesh01/homeserver/hsctl/internal/envfile"
	"github.com/Rishikesh01/homeserver/hsctl/internal/fsutil"
	"github.com/Rishikesh01/homeserver/hsctl/internal/layout"
	"github.com/Rishikesh01/homeserver/hsctl/internal/paths"
	"github.com/Rishikesh01/homeserver/hsctl/internal/provision"
)

// Store loads, normalizes and saves the settings.
type Store interface {
	Load() config.Settings
	Normalize(*config.Settings)
	Save(config.Settings) error
	DefaultDNSBind(serverIP string) string
}

// Provisioner writes the generated configuration.
type Provisioner interface {
	Generate(c config.Settings, force bool) (provision.Result, error)
	TrustNextcloudIP(ip string)
}

// Asker is the person at the terminal.
type Asker interface {
	CanAsk() bool
	Ask(prompt, def string) string
	AskValid(prompt, def string, validate func(string) error) string
	YesNo(prompt string, def bool) bool
	Typed(word string, n int) bool
}

// Docker recreates Caddy when the dashboard moves.
type Docker interface {
	ContainerState(name string) string
	Combined(dir string, args ...string) (string, error)
	ComposeUpArgs(extra ...string) []string
}

// Privileged runs a command as root (restarting the dashboard).
type Privileged interface {
	Command(name string, args ...string) *exec.Cmd
}

// Service runs setup for one repo.
type Service struct {
	Repo      paths.Repo
	Store     Store
	Provision Provisioner
	Ask       Asker
	Docker    Docker
	Priv      Privileged
	// DashboardActive reports whether the dashboard runs as a service.
	DashboardActive func() bool

	Out, Err io.Writer
}

// New returns the setup service for repo.
func New(repo paths.Repo, store Store, prov Provisioner, ask Asker, dk Docker, priv Privileged) *Service {
	return &Service{Repo: repo, Store: store, Provision: prov, Ask: ask, Docker: dk, Priv: priv,
		DashboardActive: func() bool { return false }, Out: os.Stdout, Err: os.Stderr}
}

// Options are setup's flags; nil fields weren't given.
type Options struct {
	Yes           bool // non-interactive: accept defaults/setup.conf/flags
	Force         bool // regenerate the secrets that are safe to regenerate
	ServerIP      *string
	TZ            *string
	PiholeDNSBind *string
	VWSignups     *bool
	DisabledApps  *[]string
}

// Run settles the settings, saves them, and generates or updates the configuration.
func (s *Service) Run(o Options) error {
	if o.Force && !o.Yes {
		if !s.Ask.CanAsk() {
			return fmt.Errorf("--force regenerates secrets; to do that non-interactively, add --yes")
		}
		fmt.Fprintln(s.Out, "--force generates NEW secrets: the Vaultwarden /admin token, the Pi-hole and Redis")
		fmt.Fprintln(s.Out, "passwords, and — only if Nextcloud has no data yet — its database and admin passwords.")
		fmt.Fprintln(s.Out, "The old ones stop working. Your app data and other settings are kept.")
		if !s.Ask.Typed("REGENERATE", 1) {
			fmt.Fprintln(s.Out, "aborted — nothing was changed")
			return nil
		}
	}
	// A config file we can't read must stop us here: loading would silently fall back to
	// autodetected values and then save them over the real settings.
	if err := fsutil.RequireReadable(s.Repo.Root, append([]string{paths.SetupConf}, paths.EnvFiles...)...); err != nil {
		return err
	}
	c := s.Store.Load()
	// What the stack runs with now — Caddy's certificate address and where it forwards to the
	// dashboard. setup.conf can already hold new values (edit, then `setup --yes`), so these come
	// from caddy/.env; on a first setup they're the autodetected values.
	deployedIP, deployedPort := Deployed(s.Repo, c)
	// apply only the flags the user actually set, over the loaded settings
	if o.ServerIP != nil {
		c.ServerIP = *o.ServerIP
	}
	if o.TZ != nil {
		c.TZ = *o.TZ
	}
	if o.PiholeDNSBind != nil {
		c.PiholeDNSBind = *o.PiholeDNSBind
	}
	if o.VWSignups != nil {
		c.VWSignupsAllowed = *o.VWSignups
	}
	if o.DisabledApps != nil {
		c.DisabledApps = nil
		for _, d := range *o.DisabledApps {
			if !layout.IsApp(d) {
				return fmt.Errorf("--disable-apps: unknown app %q — one of: %s", d, strings.Join(layout.Apps(), ", "))
			}
			c.SetDisabled(d, true)
		}
	}

	if !o.Yes && s.Ask.CanAsk() {
		c = s.prompt(c, deployedIP)
	} else if c.ServerIP != deployedIP && c.PiholeDNSBind == deployedIP && o.PiholeDNSBind == nil {
		// Pi-hole was bound to the old IP; let Normalize re-derive the bind for the new one.
		c.PiholeDNSBind = ""
	}
	s.Store.Normalize(&c)
	if err := config.Validate(c); err != nil {
		return err
	}
	if !config.LocalIPv4(c.ServerIP) {
		fmt.Fprintf(s.Err, "warning: %s isn't an address of this machine — the apps will only be "+
			"reachable there if it forwards to this box.\n", c.ServerIP)
	}
	if err := s.Store.Save(c); err != nil {
		return err
	}
	fmt.Fprintln(s.Out, "Saved", s.Repo.Path(paths.SetupConf))

	res, err := s.Provision.Generate(c, o.Force)
	if err != nil {
		return err
	}
	if len(res.Changed) > 0 {
		fmt.Fprintln(s.Out, "\nApplied your changes to the existing config:")
		for _, l := range res.Changed {
			fmt.Fprintln(s.Out, "  "+l)
		}
		if c.ServerIP != deployedIP {
			s.Provision.TrustNextcloudIP(c.ServerIP)
		}
		if c.UIPort != deployedPort {
			s.moveDashboard(c.UIPort)
		}
	}
	if len(res.Kept) > 0 {
		fmt.Fprintln(s.Out, "\nKept (rotating these would break existing data):")
		for _, k := range res.Kept {
			fmt.Fprintln(s.Out, "  "+k)
		}
		fmt.Fprintln(s.Out, "  For a completely fresh start, delete the data first: hsctl down --volumes")
	}
	if len(res.Secrets) > 0 {
		fmt.Fprintln(s.Out, "\n========== NEW LOGINS — save these into a password manager ==========")
		for _, sec := range res.Secrets {
			fmt.Fprintf(s.Out, "%-26s %s\n", sec.Label, sec.Value)
		}
		fmt.Fprintln(s.Out, "=====================================================================")
		fmt.Fprintln(s.Out, "See them again anytime with `hsctl secrets show` (read from the .env files) —")
		fmt.Fprintln(s.Out, "except the Vaultwarden /admin token, which is stored hashed: copy it now.")
	} else if len(res.Changed) == 0 {
		fmt.Fprintln(s.Out, "Nothing to change — the existing .env files already match these settings.")
	}
	switch {
	case len(res.Secrets) > 0 && !s.DashboardActive():
		fmt.Fprintln(s.Out, "\nNext: hsctl up   then   sudo hsctl install   (runs the dashboard on boot)")
	case len(res.Secrets) > 0 || len(res.Changed) > 0:
		fmt.Fprintln(s.Out, "\nNext: hsctl up   (recreates the containers whose settings changed)")
	}
	return nil
}

// Deployed returns the server IP and dashboard port the stack runs with now (from caddy/.env),
// falling back to c's for a stack that isn't set up yet.
func Deployed(repo paths.Repo, c config.Settings) (ip string, port int) {
	ip, port = c.ServerIP, c.UIPort
	if kv, err := envfile.Read(repo.Path("caddy/.env")); err == nil {
		if v := kv["SERVER_IP"]; v != "" {
			ip = v
		}
		if p := config.PortFromUpstream(kv["HOME_UPSTREAM"]); p > 0 {
			port = p
		}
	}
	return ip, port
}

// moveDashboard follows a changed dashboard port through to the running pieces: the dashboard
// (which picks its port only at startup) is restarted, and Caddy recreated so it forwards to the
// new port. Otherwise the next `up` would point Caddy at a port nothing listens on and lock the
// admin out of the dashboard. Run from the dashboard itself, the restart outlives this command (the
// unit's KillMode=process keeps this job running).
func (s *Service) moveDashboard(port int) {
	fmt.Fprintf(s.Out, "  the dashboard moves to port %d:\n", port)
	if s.DashboardActive() {
		if out, err := s.Priv.Command("systemctl", "restart", "hsctl-ui.service").CombinedOutput(); err != nil {
			fmt.Fprintf(s.Err, "  couldn't restart the dashboard (%v): %s\n  run: sudo systemctl restart hsctl-ui\n",
				err, strings.TrimSpace(string(out)))
		} else {
			fmt.Fprintln(s.Out, "    restarted the dashboard service")
		}
	} else {
		fmt.Fprintln(s.Out, "    (the dashboard service isn't running — start it on the new port with: sudo hsctl install)")
	}
	if s.Docker.ContainerState(layout.Proxy) == "running" {
		if out, err := s.Docker.Combined(s.Repo.Path(layout.Proxy), s.Docker.ComposeUpArgs()...); err != nil {
			fmt.Fprintf(s.Err, "  couldn't recreate Caddy (%v): %s\n  run: hsctl up\n", err, strings.TrimSpace(out))
		} else {
			fmt.Fprintln(s.Out, "    recreated Caddy so it forwards to the new port")
		}
	}
}

// prompt asks for each setting. deployedIP is the address the stack runs on now, so a Pi-hole bind
// that followed the old IP follows the new one by default.
func (s *Service) prompt(c config.Settings, deployedIP string) config.Settings {
	fmt.Fprintln(s.Out, "== Configure (Enter accepts each [default]) ==")
	c.ServerIP = s.Ask.AskValid("Server LAN IP", c.ServerIP, config.ValidIPv4)
	c.TZ = s.Ask.AskValid("Timezone", c.TZ, config.ValidTZ)
	c.UIPort = config.AtoiDef(s.Ask.AskValid("Dashboard (web UI) port", strconv.Itoa(c.UIPort), config.ValidPort), c.UIPort)
	// derived defaults follow the IP just entered
	dnsDef := c.PiholeDNSBind
	if dnsDef == "" || (dnsDef == deployedIP && c.ServerIP != deployedIP) {
		dnsDef = s.Store.DefaultDNSBind(c.ServerIP)
	}
	c.PiholeDNSBind = s.Ask.AskValid("Pi-hole DNS bind IP", dnsDef, config.ValidIPv4)
	c.VWSignupsAllowed = s.Ask.YesNo("Allow open Vaultwarden signups?", c.VWSignupsAllowed)
	fmt.Fprintf(s.Out, "  Apps: %s\n", strings.Join(layout.Apps(), ", "))
	for {
		cur := strings.Join(c.DisabledApps, ",")
		if cur == "" {
			cur = "none"
		}
		ans := s.Ask.Ask("Apps to switch OFF (comma-separated, or none)", cur)
		if ans == "none" {
			c.DisabledApps = nil
			break
		}
		c.DisabledApps = nil
		bad := ""
		for _, d := range config.SplitCSV(ans) {
			if !layout.IsApp(d) {
				bad = d
				break
			}
			c.SetDisabled(d, true)
		}
		if bad == "" {
			break
		}
		fmt.Fprintf(s.Out, "  unknown app %q\n", bad)
	}
	return c
}
