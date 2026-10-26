// Package install puts the stack on the machine and takes it off again: the dashboard's systemd
// service and the nightly backup timer (`sudo hsctl install`), and the true uninstall
// (`sudo hsctl uninstall`).
package install

import (
	"embed"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/Rishikesh01/homeserver/hsctl/internal/config"
	"github.com/Rishikesh01/homeserver/hsctl/internal/fsutil"
	"github.com/Rishikesh01/homeserver/hsctl/internal/paths"
	"github.com/Rishikesh01/homeserver/hsctl/internal/provision"
)

// The systemd units, with __DIR__ (the repo) and __BIN__ (the hsctl binary) filled in on install.
// `hsctl install` is the one installer (`make -C hsctl install-services` just calls it), so the
// dashboard unit can't be written two different ways.
//
//go:embed systemd/hsctl-ui.service systemd/hsctl-backup.service systemd/hsctl-backup.timer
var unitFiles embed.FS

// UnitDir is where the units go.
const UnitDir = "/etc/systemd/system"

// Units are the systemd units hsctl installs.
var Units = []string{"hsctl-ui.service", "hsctl-backup.service", "hsctl-backup.timer"}

// Docker is what install and uninstall need from Docker.
type Docker interface {
	Run(dir string, args ...string) error
	Output(dir string, args ...string) (string, error)
	Quiet(dir string, args ...string) error
	Volumes() ([]string, error)
	Require() error
	Problem() string
}

// Settings loads and saves the stack's settings.
type Settings interface {
	Loaded() config.Settings
	Save(config.Settings) error
}

// Stack starts the HTTPS front door on a first install.
type Stack interface {
	StartProxy() error
}

// Asker confirms an uninstall with the person at the terminal.
type Asker interface {
	CanAsk() bool
	Typed(word string, n int) bool
}

// Service installs and uninstalls one repo's stack.
type Service struct {
	Repo     paths.Repo
	Docker   Docker
	Settings Settings
	Stack    Stack
	Ask      Asker
	// UIPassword returns (creating on first use) the dashboard's admin password.
	UIPassword func() (string, error)
	// Configured reports whether the apps are set up (every core .env exists).
	Configured func() bool

	Out, Err io.Writer
}

// New returns the install service for repo.
func New(repo paths.Repo, dk Docker, settings Settings, st Stack, ask Asker, uiPassword func() (string, error), configured func() bool) *Service {
	return &Service{Repo: repo, Docker: dk, Settings: settings, Stack: st, Ask: ask,
		UIPassword: uiPassword, Configured: configured, Out: os.Stdout, Err: os.Stderr}
}

// Install is the one terminal command a first install needs after installing hsctl. It runs as
// root (`sudo hsctl install`) — it writes to /etc/systemd, drives systemctl and docker — and leaves
// the box in a state where EVERYTHING else happens in the browser:
//
//   - saves setup.conf from autodetected values (so the dashboard port is fixed before the service
//     starts — Caddy must point at the same port),
//   - creates the dashboard admin password (owned by the repo's owner, so it stays readable),
//   - on a first install, brings up Caddy alone so https://<ip>/ already answers — the dashboard's
//     setup wizard then configures + starts the rest,
//   - installs + enables the systemd service for the dashboard, so it runs persistently and
//     auto-starts on boot (the containers already do, via restart: unless-stopped),
//   - installs the nightly backup timer, switched on once a backup destination is configured
//     (`hsctl backup init` switches it on later).
//
// Safe to re-run on an existing install: it refreshes the units and restarts the dashboard on the
// current binary.
func (s *Service) Install() error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("hsctl install needs root — re-run as:\n  sudo hsctl install")
	}
	if err := s.Repo.Require(); err != nil {
		return err
	}
	exe, err := InstallableBinary()
	if err != nil {
		return err
	}
	if err := s.Docker.Require(); err != nil {
		return err
	}

	c := s.Settings.Loaded()
	if !fsutil.Exists(s.Repo.Path(paths.SetupConf)) {
		if err := s.Settings.Save(c); err != nil {
			return err
		}
		fmt.Fprintf(s.Out, "Saved %s (autodetected: IP %s, timezone %s) — you can change these in the dashboard.\n", paths.SetupConf, c.ServerIP, c.TZ)
	}
	pass, err := s.UIPassword()
	if err != nil {
		return err
	}

	firstRun := !s.Configured()
	if firstRun {
		// Caddy only needs its own .env (no secrets) — start it now so the dashboard is reachable
		// over HTTPS from any device on the LAN, before the apps exist.
		if !fsutil.Exists(s.Repo.Path("caddy/.env")) {
			if err := fsutil.WritePrivate(s.Repo.Path("caddy/.env"), provision.CaddyEnv(c)); err != nil {
				return err
			}
		}
		fmt.Fprintln(s.Out, "== starting Caddy (the HTTPS front door) ==")
		if err := s.Stack.StartProxy(); err != nil {
			return fmt.Errorf("caddy: %w", err)
		}
	}

	fmt.Fprintln(s.Out, "Installing the systemd services (dashboard + nightly backup timer)...")
	// We're root: write the units directly (root-owned — UnitDir is root's, so the atomic write
	// leaves them root's).
	for _, name := range Units {
		b, err := unitFiles.ReadFile("systemd/" + name)
		if err != nil {
			return err
		}
		unit := strings.NewReplacer("__DIR__", s.Repo.Root, "__BIN__", exe).Replace(string(b))
		if err := fsutil.WritePublic(filepath.Join(UnitDir, name), unit); err != nil {
			return fmt.Errorf("write %s: %w", name, err)
		}
	}
	steps := [][]string{
		{"systemctl", "daemon-reload"},
		{"systemctl", "enable", "--now", "hsctl-ui.service"},
	}
	// Restart a dashboard that was already running so it runs the new binary/unit — unless this IS
	// the dashboard (a Command Center button): restarting it would cut this command off.
	fromDashboard := os.Getenv("HSCTL_FROM_DASHBOARD") != ""
	if !fromDashboard {
		steps = append(steps, []string{"systemctl", "try-restart", "hsctl-ui.service"})
	}
	timerOn := fsutil.Exists(s.Repo.Path(paths.BackupConf))
	if timerOn {
		steps = append(steps, []string{"systemctl", "enable", "--now", "hsctl-backup.timer"})
	}
	for _, c := range steps {
		cmd := exec.Command(c[0], c[1:]...)
		cmd.Stdout, cmd.Stderr, cmd.Stdin = s.Out, s.Err, os.Stdin
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("%s: %w", strings.Join(c, " "), err)
		}
	}
	if timerOn {
		fmt.Fprintln(s.Out, "Nightly backups: on (03:30). See them on the dashboard's Backups page.")
	} else {
		fmt.Fprintln(s.Out, "Nightly backups: installed, and switched on as soon as you set a destination and")
		fmt.Fprintln(s.Out, "initialize it (dashboard → Backups, or sudo hsctl backup init).")
	}
	if fromDashboard {
		fmt.Fprintln(s.Out, "To run a new hsctl binary, restart the dashboard: sudo systemctl restart hsctl-ui")
	}
	fmt.Fprintf(s.Out, "\nDone — the dashboard now runs as a service and auto-starts on boot.\n\n")
	if firstRun {
		fmt.Fprintf(s.Out, "Finish setting up in your browser (from any device on this network):\n\n")
		fmt.Fprintf(s.Out, "    https://%s/\n\n", c.ServerIP)
		fmt.Fprintf(s.Out, "    username  admin\n")
		fmt.Fprintf(s.Out, "    password  %s\n\n", pass)
		fmt.Fprintf(s.Out, "Your browser will warn about the certificate the first time — that's expected\n")
		fmt.Fprintf(s.Out, "(the server made its own). Click through once; the wizard shows how to install\n")
		fmt.Fprintf(s.Out, "it so the warning goes away for good. The password is also in %s.\n", s.Repo.Path(paths.UIPassword))
	} else {
		fmt.Fprintf(s.Out, "Open https://%s   (user 'admin', password in %s — or: hsctl secrets show)\n",
			c.ServerIP, s.Repo.Path(paths.UIPassword))
	}
	return nil
}

// InstallableBinary returns the hsctl binary the root services should run — refusing one that a
// normal user could replace, because the service would then run THEIR code as root (and a later
// `make build` would silently swap the dashboard's binary).
func InstallableBinary() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("can't locate the hsctl binary: %w", err)
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	for _, p := range []string{exe, filepath.Dir(exe)} {
		fi, err := os.Stat(p)
		if err != nil {
			return "", err
		}
		st, ok := fi.Sys().(*syscall.Stat_t)
		if !ok || st.Uid != 0 || fi.Mode().Perm()&0022 != 0 {
			return "", fmt.Errorf("%s is writable by a normal user, so a root service mustn't run it.\n"+
				"Install the binary system-wide first, then run that one:\n"+
				"  make -C hsctl install      # -> /usr/local/bin/hsctl\n"+
				"  sudo /usr/local/bin/hsctl install", exe)
		}
	}
	return exe, nil
}

// EnableBackupTimer switches the nightly backup timer on when it's installed, now that a backup
// repository exists. Best-effort (no systemd, not root): it says what to run instead.
func (s *Service) EnableBackupTimer() {
	if os.Geteuid() != 0 || !fsutil.Exists(filepath.Join(UnitDir, "hsctl-backup.timer")) {
		return
	}
	if out, err := exec.Command("systemctl", "enable", "--now", "hsctl-backup.timer").CombinedOutput(); err != nil {
		fmt.Fprintf(s.Err, "couldn't switch the nightly backup timer on (%v): %s\nrun: sudo systemctl enable --now hsctl-backup.timer\n",
			err, strings.TrimSpace(string(out)))
		return
	}
	fmt.Fprintln(s.Out, "nightly backups: on (03:30)")
}

// DashboardActive reports whether the dashboard systemd service is running.
func DashboardActive() bool {
	out, _ := exec.Command("systemctl", "is-active", "hsctl-ui.service").Output()
	return strings.TrimSpace(string(out)) == "active"
}
