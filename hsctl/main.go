// Command hsctl is the control tool for the homeserver stack: a CLI (with shell
// completion via Cobra) and the web dashboard it serves. It configures + generates
// .env files, starts/stops the stack, fetches the CA cert, and runs backups.
package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

// version is reported by `hsctl --version`. It's overridden at build time via
// -ldflags "-X main.version=<git tag>" (see the Makefile), so a released binary reports its
// git tag (e.g. v1.1.0). A plain `go build` with no ldflags reports "dev".
var version = "dev"

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
		// A root run (sudo, the dashboard service, the backup timer) first hands back any of
		// hsctl's files an older version left root-owned, so the admin can read them again.
		PersistentPreRun: func(*cobra.Command, []string) { repairRepoOwnership() },
	}

	setup := &cobra.Command{
		Use:   "setup",
		Short: "Configure and generate each service's .env, or apply changed settings (interactive)",
		Args:  setupArgs,
		RunE:  runSetup,
	}
	setup.Flags().Bool("yes", false, "non-interactive: accept defaults/setup.conf/flags")
	setup.Flags().Bool("force", false, "regenerate secrets (asks you to type REGENERATE; keeps the ones existing data needs)")
	setup.Flags().String("server-ip", "", "server LAN IP")
	setup.Flags().String("tz", "", "timezone")
	setup.Flags().String("pihole-dns-bind", "", "Pi-hole :53 bind IP")
	setup.Flags().Bool("vw-signups", true, "allow open Vaultwarden signups")
	setup.Flags().String("disable-apps", "", "comma-separated apps to switch off (e.g. stirling,it-tools)")

	up := &cobra.Command{Use: "up", Short: "Start the stack (apps + tools, then caddy)", Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error { return cmdUp() }}

	down := &cobra.Command{Use: "down", Short: "Stop the stack", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			v, _ := c.Flags().GetBool("volumes")
			k, _ := c.Flags().GetBool("keep-proxy")
			return cmdDown(v, k)
		}}
	down.Flags().Bool("volumes", false, "also delete data volumes (DESTRUCTIVE)")
	down.Flags().Bool("keep-proxy", false, "leave Caddy running, so the dashboard stays reachable (and with --volumes its certificate is kept)")

	restart := &cobra.Command{Use: "restart", Short: "Stop the apps and start the stack again (Caddy keeps running)", Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error { return cmdRestart() }}

	status := &cobra.Command{Use: "status", Short: "Show container status", Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error { return cmdStatus() }}

	getca := &cobra.Command{Use: "get-ca", Short: "Write caddy-root-ca.crt to install on devices", Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error { return cmdGetCA() }}

	install := &cobra.Command{Use: "install", Short: "Run the dashboard as a systemd service (auto-start on boot)", Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error { return cmdInstall() }}

	ui := &cobra.Command{Use: "ui", Short: "Serve the web dashboard", Args: cobra.NoArgs, RunE: runUI}
	ui.Flags().String("addr", "", "listen address (default :<UI port from setup.conf>)")

	updates := &cobra.Command{Use: "updates", Short: "Check whether newer app images are available (read-only unless --apply)", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			apply, _ := c.Flags().GetStringSlice("apply")
			yes, _ := c.Flags().GetBool("yes")
			return cmdUpdates(apply, yes)
		}}
	updates.Flags().StringSlice("apply", nil,
		"apply updates: a container name (repeatable), or \"all\" for every routine (non-major) update")
	updates.Flags().Bool("yes", false, "skip the confirmation prompt")

	images := &cobra.Command{Use: "images", Short: "Check every pinned image runs on the platforms we support", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			want, _ := c.Flags().GetStringSlice("platforms")
			return cmdImages(want)
		}}
	images.Flags().StringSlice("platforms", supportedPlatforms, "platforms every pinned image must publish")

	uninstall := &cobra.Command{Use: "uninstall", Short: "Remove the stack + hsctl; keeps app data and backups unless told otherwise", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			data, _ := c.Flags().GetBool("data")
			all, _ := c.Flags().GetBool("all")
			yes, _ := c.Flags().GetBool("yes")
			return cmdUninstall(data, all, yes)
		}}
	uninstall.Flags().Bool("data", false, "also delete the app data volumes (asks to type DESTROY)")
	uninstall.Flags().Bool("all", false, "delete the app data volumes AND the backups (asks to type DESTROY)")
	uninstall.Flags().Bool("yes", false, "skip the confirmation — only for the default, data-keeping tier")

	root.AddCommand(setup, up, down, restart, status, getca, install, ui, updates, images, uninstall, appsCmd(), backupCmd(), secretsCmd())
	return root
}

// repairRepoOwnership runs repairOwnership on the repo when we're root, and says what it fixed.
func repairRepoOwnership() {
	if os.Geteuid() != 0 {
		return
	}
	repo := repoDir()
	if !isRepo(repo) {
		return
	}
	if fixed := repairOwnership(repo); len(fixed) > 0 {
		fmt.Fprintf(os.Stderr, "gave %s back to the repo's owner (a root run had left them root-owned)\n",
			strings.Join(fixed, ", "))
	}
}
