package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const uiUnit = "/etc/systemd/system/hsctl-ui.service"

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
//     auto-starts on boot (the containers already do, via restart: unless-stopped).
//
// Safe to re-run on an existing install: it only (re)installs the service.
func cmdInstall() error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("hsctl install needs root — re-run as:\n  sudo hsctl install")
	}
	repo, err := requireRepoDir()
	if err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("can't locate hsctl binary: %w", err)
	}
	if err := dockerCmd(repo, "info").Run(); err != nil {
		return fmt.Errorf("Docker isn't reachable (is it installed and running? `curl -fsSL https://get.docker.com | sh`)")
	}

	c := LoadConfig(repo)
	c.Normalize()
	if !fileExists(filepath.Join(repo, confFile)) {
		if err := c.Save(repo); err != nil {
			return err
		}
		fmt.Printf("Saved %s (autodetected: IP %s, timezone %s) — you can change these in the dashboard.\n", confFile, c.ServerIP, c.TZ)
	}
	pass := uiPassword(repo)

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
		if err := dockerRun(filepath.Join(repo, "caddy"), "compose", "up", "-d"); err != nil {
			return fmt.Errorf("caddy: %w", err)
		}
	}

	unit := fmt.Sprintf(`[Unit]
Description=hsctl homeserver web UI (dashboard)
After=docker.service
Wants=docker.service

[Service]
WorkingDirectory=%s
ExecStart=%s ui
Restart=on-failure
User=root

[Install]
WantedBy=multi-user.target
`, repo, exe)

	fmt.Println("Installing the dashboard as a systemd service...")
	// We're root: write the unit directly (root-owned — deliberately NOT through
	// writeFileAtomic, whose sudo-chown is for the user's files, not /etc).
	if err := os.WriteFile(uiUnit, []byte(unit), 0644); err != nil {
		return err
	}
	for _, c := range [][]string{
		{"systemctl", "daemon-reload"},
		{"systemctl", "enable", "--now", "hsctl-ui.service"},
	} {
		cmd := exec.Command(c[0], c[1:]...)
		cmd.Stdout, cmd.Stderr, cmd.Stdin = os.Stdout, os.Stderr, os.Stdin
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("%s: %w", strings.Join(c, " "), err)
		}
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
		fmt.Printf("Open https://%s\n", c.ServerIP)
	}
	return nil
}

// uiServiceActive reports whether the dashboard systemd service is running.
func uiServiceActive() bool {
	out, _ := exec.Command("systemctl", "is-active", "hsctl-ui.service").Output()
	return strings.TrimSpace(string(out)) == "active"
}
