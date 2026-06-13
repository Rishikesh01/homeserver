package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

const uiUnit = "/etc/systemd/system/hsctl-ui.service"

// cmdInstall installs + enables the systemd service for the dashboard, so it runs
// persistently and auto-starts on boot (the containers already do, via
// restart: unless-stopped). Uses sudo for the /etc/systemd writes.
func cmdInstall(_ []string) error {
	repo := repoDir()
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("can't locate hsctl binary: %w", err)
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

	fmt.Println("Installing the dashboard as a systemd service (needs sudo)...")
	if err := sudoTee(uiUnit, unit); err != nil {
		return err
	}
	for _, c := range [][]string{
		{"systemctl", "daemon-reload"},
		{"systemctl", "enable", "--now", "hsctl-ui.service"},
	} {
		cmd := exec.Command("sudo", c...)
		cmd.Stdout, cmd.Stderr, cmd.Stdin = os.Stdout, os.Stderr, os.Stdin
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("sudo %s: %w", strings.Join(c, " "), err)
		}
	}
	fmt.Printf("\nDone — the dashboard now runs as a service and auto-starts on boot.\n")
	fmt.Printf("Open https://%s\n", LoadConfig(repo).ServerIP)
	return nil
}

// sudoTee writes content to a root-owned path via `sudo tee`.
func sudoTee(path, content string) error {
	cmd := exec.Command("sudo", "tee", path)
	cmd.Stdin = strings.NewReader(content)
	cmd.Stdout = io.Discard
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// uiServiceActive reports whether the dashboard systemd service is running.
func uiServiceActive() bool {
	out, _ := exec.Command("systemctl", "is-active", "hsctl-ui.service").Output()
	return strings.TrimSpace(string(out)) == "active"
}
