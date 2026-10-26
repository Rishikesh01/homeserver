package main

import (
	"embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

// The systemd units, with __DIR__ (the repo) and __BIN__ (the hsctl binary) filled in on install.
// `hsctl install` is the one installer (`make -C hsctl install-services` just calls it), so the
// dashboard unit can't be written two different ways.
//
//go:embed systemd/hsctl-ui.service systemd/hsctl-backup.service systemd/hsctl-backup.timer
var unitFiles embed.FS

const unitDir = "/etc/systemd/system"

var unitNames = []string{"hsctl-ui.service", "hsctl-backup.service", "hsctl-backup.timer"}

// cmdInstall is the one terminal command a first install needs after installing hsctl.
// It runs as root (`sudo hsctl install`) — it writes to /etc/systemd, drives systemctl
// and docker — and leaves the box in a state where EVERYTHING else happens in the browser:
//
//   - saves setup.conf from autodetected values (so the dashboard port is fixed before the
//     service starts — Caddy must point at the same port),
//   - creates the dashboard admin password (chowned back to the sudo-invoking user via
//     writeFileAtomic, so it stays printable and readable after we exit),
//   - on a first install, brings up Caddy alone so https://<ip>/ already answers — the
//     dashboard's setup wizard then configures + starts the rest,
//   - installs + enables the systemd service for the dashboard, so it runs persistently and
//     auto-starts on boot (the containers already do, via restart: unless-stopped),
//   - installs the nightly backup timer, switched on once a backup destination is configured
//     (`hsctl backup init` switches it on later).
//
// Safe to re-run on an existing install: it refreshes the units and restarts the dashboard on
// the current binary.
func cmdInstall() error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("hsctl install needs root — re-run as:\n  sudo hsctl install")
	}
	repo, err := requireRepoDir()
	if err != nil {
		return err
	}
	exe, err := installableBinary()
	if err != nil {
		return err
	}
	if err := requireDocker(); err != nil {
		return err
	}

	c := LoadConfig(repo)
	c.Normalize()
	if !fileExists(filepath.Join(repo, confFile)) {
		if err := c.Save(repo); err != nil {
			return err
		}
		fmt.Printf("Saved %s (autodetected: IP %s, timezone %s) — you can change these in the dashboard.\n", confFile, c.ServerIP, c.TZ)
	}
	pass, err := uiPassword(repo)
	if err != nil {
		return err
	}

	firstRun := len(missingEnv()) > 0
	if firstRun {
		// Caddy only needs its own .env (no secrets) — start it now so the dashboard is
		// reachable over HTTPS from any device on the LAN, before the apps exist.
		if !fileExists(filepath.Join(repo, "caddy/.env")) {
			if err := writeFile0600(filepath.Join(repo, "caddy/.env"), c.caddyEnv()); err != nil {
				return err
			}
		}
		if err := ensureEdgeNetwork(); err != nil {
			return err
		}
		fmt.Println("== starting Caddy (the HTTPS front door) ==")
		if err := dockerRun(filepath.Join(repo, "caddy"), composeUpArgs()...); err != nil {
			return fmt.Errorf("caddy: %w", err)
		}
	}

	fmt.Println("Installing the systemd services (dashboard + nightly backup timer)...")
	// We're root: write the units directly (root-owned — unitDir is root's, so writeFileAtomic
	// leaves them root's).
	for _, name := range unitNames {
		b, err := unitFiles.ReadFile("systemd/" + name)
		if err != nil {
			return err
		}
		unit := strings.NewReplacer("__DIR__", repo, "__BIN__", exe).Replace(string(b))
		if err := writeFile0644(filepath.Join(unitDir, name), unit); err != nil {
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
	timerOn := fileExists(filepath.Join(repo, backupConfFile))
	if timerOn {
		steps = append(steps, []string{"systemctl", "enable", "--now", "hsctl-backup.timer"})
	}
	for _, c := range steps {
		cmd := exec.Command(c[0], c[1:]...)
		cmd.Stdout, cmd.Stderr, cmd.Stdin = os.Stdout, os.Stderr, os.Stdin
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("%s: %w", strings.Join(c, " "), err)
		}
	}
	if timerOn {
		fmt.Println("Nightly backups: on (03:30). See them on the dashboard's Backups page.")
	} else {
		fmt.Println("Nightly backups: installed, and switched on as soon as you set a destination and")
		fmt.Println("initialize it (dashboard → Backups, or sudo hsctl backup init).")
	}
	if fromDashboard {
		fmt.Println("To run a new hsctl binary, restart the dashboard: sudo systemctl restart hsctl-ui")
	}
	fmt.Printf("\nDone — the dashboard now runs as a service and auto-starts on boot.\n\n")
	if firstRun {
		fmt.Printf("Finish setting up in your browser (from any device on this network):\n\n")
		fmt.Printf("    https://%s/\n\n", c.ServerIP)
		fmt.Printf("    username  admin\n")
		fmt.Printf("    password  %s\n\n", pass)
		fmt.Printf("Your browser will warn about the certificate the first time — that's expected\n")
		fmt.Printf("(the server made its own). Click through once; the wizard shows how to install\n")
		fmt.Printf("it so the warning goes away for good. The password is also in %s.\n", filepath.Join(repo, ".ui-password"))
	} else {
		fmt.Printf("Open https://%s   (user 'admin', password in %s — or: hsctl secrets show)\n",
			c.ServerIP, filepath.Join(repo, ".ui-password"))
	}
	return nil
}

// installableBinary returns the hsctl binary the root services should run — refusing one that a
// normal user could replace, because the service would then run THEIR code as root (and a later
// `make build` would silently swap the dashboard's binary).
func installableBinary() (string, error) {
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

// enableBackupTimer switches the nightly backup timer on when it's installed, now that a backup
// repository exists. Best-effort (no systemd, not root): it says what to run instead.
func enableBackupTimer() {
	if os.Geteuid() != 0 || !fileExists(filepath.Join(unitDir, "hsctl-backup.timer")) {
		return
	}
	if out, err := exec.Command("systemctl", "enable", "--now", "hsctl-backup.timer").CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "couldn't switch the nightly backup timer on (%v): %s\nrun: sudo systemctl enable --now hsctl-backup.timer\n",
			err, strings.TrimSpace(string(out)))
		return
	}
	fmt.Println("nightly backups: on (03:30)")
}

// uiServiceActive reports whether the dashboard systemd service is running.
func uiServiceActive() bool {
	out, _ := exec.Command("systemctl", "is-active", "hsctl-ui.service").Output()
	return strings.TrimSpace(string(out)) == "active"
}
