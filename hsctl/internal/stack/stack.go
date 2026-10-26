// Package stack runs the homeserver's services: one docker compose project per folder, started
// apps first and the proxy (Caddy) last, stopped in reverse — and switches apps on and off.
package stack

import (
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/Rishikesh01/homeserver/hsctl/internal/config"
	"github.com/Rishikesh01/homeserver/hsctl/internal/fsutil"
	"github.com/Rishikesh01/homeserver/hsctl/internal/layout"
	"github.com/Rishikesh01/homeserver/hsctl/internal/paths"
)

// Docker is what the stack needs from Docker.
type Docker interface {
	Run(dir string, args ...string) error
	Output(dir string, args ...string) (string, error)
	Combined(dir string, args ...string) (string, error)
	Require() error
	EnsureNetwork(name string) error
	ComposeUpArgs(extra ...string) []string
}

// Settings loads and saves the stack's settings.
type Settings interface {
	Loaded() config.Settings
	Save(config.Settings) error
}

// Provisioner keeps the generated configuration in step with the stack.
type Provisioner interface {
	MissingEnv(config.Settings) []string
	MigrateSharedNetworkEnv()
	EnsureNextcloudTrusts(ip string) (bool, error)
}

// Service starts, stops and reports on the stack.
type Service struct {
	Repo      paths.Repo
	Docker    Docker
	Settings  Settings
	Provision Provisioner
	// DashboardActive reports whether the dashboard runs as a service, for `up`'s reminder.
	DashboardActive func() bool

	Out, Err io.Writer
}

// New returns the stack service for repo.
func New(repo paths.Repo, dk Docker, settings Settings, prov Provisioner) *Service {
	return &Service{Repo: repo, Docker: dk, Settings: settings, Provision: prov,
		DashboardActive: func() bool { return false }, Out: os.Stdout, Err: os.Stderr}
}

// UpEach runs start for each service in order, carrying on past a failure so one broken app (say
// Pi-hole unable to bind :53) can't keep the rest — and Caddy, the only way to the dashboard —
// down. It returns the services that failed.
func UpEach(order []string, start func(svc string) error) (failed []string) {
	for _, svc := range order {
		if err := start(svc); err != nil {
			failed = append(failed, svc)
		}
	}
	return failed
}

// StopOrder is the order services go down in — the reverse of the start order, so Caddy stops
// first — without Caddy when keepProxy is set. Every app is included, switched off or not, so a
// since-disabled app's leftovers stop too.
func StopOrder(keepProxy bool) []string {
	var out []string
	for i := len(layout.Services) - 1; i >= 0; i-- {
		if s := layout.Services[i]; !keepProxy || s != layout.Proxy {
			out = append(out, s)
		}
	}
	return out
}

// Up starts every enabled service (apps, then Caddy), carrying on past one that fails.
func (s *Service) Up() error {
	if err := fsutil.RequireReadable(s.Repo.Root, paths.EnvFiles...); err != nil {
		return err
	}
	if err := s.Docker.Require(); err != nil {
		return err
	}
	cfg := s.Settings.Loaded()
	if m := s.Provision.MissingEnv(cfg); len(m) > 0 {
		return fmt.Errorf("missing .env for %s — run: hsctl setup", strings.Join(m, ", "))
	}
	s.Provision.MigrateSharedNetworkEnv()
	if err := s.Docker.EnsureNetwork(layout.EdgeNetwork); err != nil {
		return err
	}
	if len(cfg.DisabledApps) > 0 {
		fmt.Fprintf(s.Out, "(skipping disabled apps: %s — `hsctl apps enable <name>` to turn one on)\n", strings.Join(cfg.DisabledApps, ", "))
	}
	failed := UpEach(cfg.Enabled(), func(svc string) error {
		fmt.Fprintf(s.Out, "== up: %s ==\n", svc)
		err := s.Docker.Run(s.Repo.Path(svc), s.Docker.ComposeUpArgs()...)
		if err != nil {
			fmt.Fprintf(s.Err, "!! %s didn't start (%v) — carrying on with the rest\n", svc, err)
		}
		return err
	})
	// Nextcloud only reads NC_TRUSTED_DOMAINS on first install; keep its own list in step with the
	// server IP so an IP change doesn't lock it behind "untrusted domain". Best-effort: a fresh
	// Nextcloud may still be installing (and then takes the env value anyway).
	if !cfg.IsDisabled("nextcloud") {
		if added, err := s.Provision.EnsureNextcloudTrusts(cfg.ServerIP); err == nil && added {
			fmt.Fprintf(s.Out, "nextcloud: added %s to its trusted domains\n", cfg.ServerIP)
		}
	}
	fmt.Fprintln(s.Out)
	if err := s.Status(); err != nil {
		return err
	}
	if len(failed) > 0 {
		if slices.Contains(failed, "pihole") {
			fmt.Fprintln(s.Out, "\nPi-hole needs port 53. If something else on this machine holds it, bind Pi-hole to the")
			fmt.Fprintln(s.Out, "LAN IP (hsctl setup --pihole-dns-bind <LAN IP>), or switch it off: hsctl apps disable pihole")
			fmt.Fprintln(s.Out, "(nothing else depends on Pi-hole — devices only use it if you point them at it).")
		}
		return fmt.Errorf("%s didn't start — see the output above", strings.Join(failed, ", "))
	}
	if !s.DashboardActive() {
		fmt.Fprintln(s.Out, "\nNote: the dashboard isn't running as a service yet — run `sudo hsctl install`")
		fmt.Fprintln(s.Out, "so it auto-starts on boot (or `hsctl ui` to just run it now).")
	}
	return nil
}

// Down stops the stack. keepProxy leaves Caddy running: the dashboard is only reachable through
// it, so a stop started FROM the dashboard must keep it up or the admin is locked out (Start all
// would be unreachable without SSH). With volumes, Caddy's data — the certificate authority every
// device trusts — is then kept too.
func (s *Service) Down(volumes, keepProxy bool) error {
	if err := fsutil.RequireReadable(s.Repo.Root, paths.EnvFiles...); err != nil {
		return err
	}
	if err := s.Docker.Require(); err != nil {
		return err
	}
	down := []string{"compose", "down"}
	if volumes {
		down = append(down, "-v")
		fmt.Fprintln(s.Out, "!! --volumes: data volumes will be DELETED")
	}
	for _, svc := range StopOrder(keepProxy) {
		fmt.Fprintf(s.Out, "== down: %s ==\n", svc)
		if err := s.Docker.Run(s.Repo.Path(svc), down...); err != nil {
			return fmt.Errorf("%s: %w", svc, err)
		}
	}
	if keepProxy {
		fmt.Fprintln(s.Out, "\nThe apps are stopped. Caddy (and so the dashboard) is still running — start them again")
		fmt.Fprintln(s.Out, "with Start all in the dashboard, or `hsctl up`. Stopped apps stay stopped after a reboot.")
	} else {
		fmt.Fprintln(s.Out, "\nEverything is stopped, Caddy included, so the dashboard is unreachable until `hsctl up`.")
		fmt.Fprintln(s.Out, "Stopped containers stay stopped after a reboot too.")
	}
	return nil
}

// Restart stops the apps and starts the stack again. Caddy keeps running throughout (up leaves a
// running container alone), so the dashboard stays reachable.
func (s *Service) Restart() error {
	if err := s.Down(false, true); err != nil {
		return err
	}
	fmt.Fprintln(s.Out)
	return s.Up()
}

// StartProxy starts Caddy on its own — the first install's front door, and the way back in after
// a failed restore.
func (s *Service) StartProxy() error {
	if err := s.Docker.EnsureNetwork(layout.EdgeNetwork); err != nil {
		return err
	}
	return s.Docker.Run(s.Repo.Path(layout.Proxy), s.Docker.ComposeUpArgs()...)
}

// Container is one stack container's state, for the dashboard.
type Container struct {
	Name    string
	State   string
	Status  string
	Running bool
}

// Containers lists the stack's containers, stopped ones included.
func (s *Service) Containers() ([]Container, error) {
	args := []string{"ps", "-a", "--format", "{{.Names}}|{{.State}}|{{.Status}}"}
	for _, n := range layout.ContainerFilters {
		args = append(args, "--filter", "name="+n)
	}
	out, err := s.Docker.Output(s.Repo.Root, args...)
	if err != nil {
		return nil, err
	}
	var res []Container
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		p := strings.SplitN(line, "|", 3)
		if len(p) < 3 {
			continue
		}
		res = append(res, Container{p[0], p[1], p[2], p[1] == "running"})
	}
	return res, nil
}

// ContainerNames lists the stack's real container names (stopped ones included).
func (s *Service) ContainerNames() ([]string, error) {
	cs, err := s.Containers()
	if err != nil {
		return nil, fmt.Errorf("listing containers (is docker up?): %w", err)
	}
	var names []string
	for _, c := range cs {
		names = append(names, c.Name)
	}
	return names, nil
}

// Status prints each stack container and its state.
func (s *Service) Status() error {
	if err := s.Docker.Require(); err != nil {
		return err
	}
	// -a: a container that failed to start (Exited, Created) is exactly what status must show.
	args := []string{"ps", "-a", "--format", "table {{.Names}}\t{{.Status}}"}
	for _, n := range layout.ContainerFilters {
		args = append(args, "--filter", "name="+n)
	}
	out, err := s.Docker.Output(s.Repo.Root, args...)
	if err != nil {
		return err
	}
	if len(strings.Split(strings.TrimSpace(out), "\n")) <= 1 { // just the header
		fmt.Fprintln(s.Out, "no stack containers yet — start the stack with: hsctl up")
		return nil
	}
	fmt.Fprintln(s.Out, out)
	return nil
}

// CA returns Caddy's root certificate (PEM), read from the running Caddy container.
func (s *Service) CA() ([]byte, error) {
	out, err := s.Docker.Output(s.Repo.Root, "exec", layout.Proxy, "cat", layout.CaddyCA)
	if err != nil {
		return nil, fmt.Errorf("reading CA from caddy (is it up?): %w", err)
	}
	return []byte(out + "\n"), nil
}

// GetCA writes caddy-root-ca.crt for installing on devices.
func (s *Service) GetCA() error {
	if err := s.Docker.Require(); err != nil {
		return err
	}
	ca, err := s.CA()
	if err != nil {
		return err
	}
	dst := s.Repo.Path(paths.CACert)
	if err := fsutil.WritePublic(dst, string(ca)); err != nil {
		return err
	}
	fmt.Fprintln(s.Out, "wrote", dst)
	fmt.Fprintln(s.Out, "install it as a trusted CA on each device (see ONBOARDING.md)")
	return nil
}
