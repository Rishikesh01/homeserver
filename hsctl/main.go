// Command hsctl is the control tool for the homeserver stack: a CLI (with shell
// completion via Cobra) and the web dashboard it serves. It configures + generates
// .env files, starts/stops the stack, fetches the CA cert, and runs backups.
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

const version = "0.2.0"

func main() {
	if err := rootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func rootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "hsctl",
		Short:         "Control the homeserver stack (CLI + web dashboard)",
		Long:          "hsctl configures, runs, and backs up the homeserver stack, and serves its web dashboard.",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	setup := &cobra.Command{
		Use:   "setup",
		Short: "Configure and generate each service's .env (interactive)",
		Args:  cobra.NoArgs,
		RunE:  runSetup,
	}
	setup.Flags().Bool("yes", false, "non-interactive: accept defaults/setup.conf/flags")
	setup.Flags().Bool("force", false, "regenerate ALL secrets (DESTRUCTIVE for live data)")
	setup.Flags().String("server-ip", "", "server LAN IP")
	setup.Flags().String("tz", "", "timezone")
	setup.Flags().String("email", "", "admin email")
	setup.Flags().Int("vw-port", 0, "Vaultwarden host port")
	setup.Flags().Int("nc-port", 0, "Nextcloud host port")
	setup.Flags().Int("pihole-port", 0, "Pi-hole web port")
	setup.Flags().String("pihole-dns-bind", "", "Pi-hole :53 bind IP")
	setup.Flags().Bool("vw-signups", true, "allow open Vaultwarden signups")

	up := &cobra.Command{Use: "up", Short: "Start the stack (apps + tools, then caddy)", Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error { return cmdUp() }}

	down := &cobra.Command{Use: "down", Short: "Stop the stack", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error { v, _ := c.Flags().GetBool("volumes"); return cmdDown(v) }}
	down.Flags().Bool("volumes", false, "also delete data volumes (DESTRUCTIVE)")

	status := &cobra.Command{Use: "status", Short: "Show container status", Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error { return cmdStatus() }}

	getca := &cobra.Command{Use: "get-ca", Short: "Write caddy-root-ca.crt to install on devices", Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error { return cmdGetCA() }}

	install := &cobra.Command{Use: "install", Short: "Run the dashboard as a systemd service (auto-start on boot)", Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error { return cmdInstall() }}

	ui := &cobra.Command{Use: "ui", Short: "Serve the web dashboard", Args: cobra.NoArgs, RunE: runUI}
	ui.Flags().String("addr", "", "listen address (default :<UI port from setup.conf>)")

	root.AddCommand(setup, up, down, status, getca, install, ui, backupCmd(), secretsCmd(), migrateCmd())
	return root
}
