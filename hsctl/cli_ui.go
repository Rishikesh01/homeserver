package main

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/Rishikesh01/homeserver/hsctl/internal/jobs"
	"github.com/Rishikesh01/homeserver/hsctl/internal/priv"
	"github.com/Rishikesh01/homeserver/hsctl/internal/web"
	"github.com/spf13/cobra"
)

func (a *app) uiCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "ui", Short: "Serve the web dashboard", Args: cobra.NoArgs, RunE: a.inRepo(a.runUI)}
	cmd.Flags().String("addr", "", "listen address, e.g. 127.0.0.1:8088 (default: loopback + the docker bridge gateway, on the dashboard port from setup)")
	return cmd
}

// runUI serves the dashboard. It runs under systemd, so its structured log lines on stderr land in
// the journal (journalctl -u hsctl-ui); CLI commands keep their plain, user-facing stdout.
func (a *app) runUI(cmd *cobra.Command, _ []string) error {
	addr, _ := cmd.Flags().GetString("addr")
	pass, err := a.secrets.UIPassword()
	if err != nil {
		return err
	}
	// Dashboard jobs re-run this binary with the command to run.
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("cannot locate the hsctl binary: %w", err)
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	s := web.New(a.repo, pass, web.Deps{
		Settings:  a.store,
		Provision: a.prov,
		Stack:     a.stack,
		Backup:    a.backup,
		Restic:    a.restic,
		Updates:   a.updates,
		Devices:   a.devices,
		Sysinfo:   a.sysinfo,
		Docker:    a.docker,
		Jobs:      jobs.New(a.repo.Root, exe, log),
		Lock:      a.lock,
		Priv:      priv.Root{},
	})
	s.Log = log
	return s.Serve(addr)
}
