// Command hsctl is the control tool for the homeserver stack: a CLI (with shell
// completion via Cobra) and the web dashboard it serves. It configures + generates
// .env files, starts/stops the stack, fetches the CA cert, and runs backups.
//
// main only wires the services (app.go) and defines the commands; the work is done by the
// packages under internal/.
package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/Rishikesh01/homeserver/hsctl/internal/fsutil"
	"github.com/Rishikesh01/homeserver/hsctl/internal/images"
	"github.com/Rishikesh01/homeserver/hsctl/internal/paths"
	"github.com/spf13/cobra"
)

// version is reported by `hsctl --version`. It's overridden at build time via
// -ldflags "-X main.version=<git tag>" (see the Makefile), so a released binary reports its
// git tag (e.g. v1.1.0). A plain `go build` with no ldflags reports "dev".
var version = "dev"

func main() {
	if err := rootCmd(newApp(paths.Guess())).Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func rootCmd(a *app) *cobra.Command {
	root := &cobra.Command{
		Use:           "hsctl",
		Short:         "Control the homeserver stack (CLI + web dashboard)",
		Long:          "hsctl configures, runs, and backs up the homeserver stack, and serves its web dashboard.",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
		// A root run (sudo, the dashboard service, the backup timer) first hands back any of
		// hsctl's files an older version left root-owned, so the admin can read them again.
		PersistentPreRun: func(*cobra.Command, []string) { a.repairOwnership() },
	}

	up := &cobra.Command{Use: "up", Short: "Start the stack (apps + tools, then caddy)", Args: cobra.NoArgs,
		RunE: a.inRepo(a.locked("up", 0, func(*cobra.Command, []string) error { return a.stack.Up() }))}

	down := &cobra.Command{Use: "down", Short: "Stop the stack", Args: cobra.NoArgs,
		RunE: a.inRepo(a.locked("down", 0, func(c *cobra.Command, _ []string) error {
			v, _ := c.Flags().GetBool("volumes")
			k, _ := c.Flags().GetBool("keep-proxy")
			return a.stack.Down(v, k)
		}))}
	down.Flags().Bool("volumes", false, "also delete data volumes (DESTRUCTIVE)")
	down.Flags().Bool("keep-proxy", false, "leave Caddy running, so the dashboard stays reachable (and with --volumes its certificate is kept)")

	restart := &cobra.Command{Use: "restart", Short: "Stop the apps and start the stack again (Caddy keeps running)", Args: cobra.NoArgs,
		RunE: a.inRepo(a.locked("restart", 0, func(*cobra.Command, []string) error { return a.stack.Restart() }))}

	status := &cobra.Command{Use: "status", Short: "Show container status", Args: cobra.NoArgs,
		RunE: a.inRepo(func(*cobra.Command, []string) error { return a.stack.Status() })}

	getca := &cobra.Command{Use: "get-ca", Short: "Write caddy-root-ca.crt to install on devices", Args: cobra.NoArgs,
		RunE: a.inRepo(func(*cobra.Command, []string) error { return a.stack.GetCA() })}

	install := &cobra.Command{Use: "install", Short: "Run the dashboard as a systemd service (auto-start on boot)", Args: cobra.NoArgs,
		RunE: a.locked("install", 0, func(*cobra.Command, []string) error { return a.install.Install() })}

	updates := &cobra.Command{Use: "updates", Short: "Check whether newer app images are available (read-only unless --apply)", Args: cobra.NoArgs,
		RunE: a.inRepo(func(c *cobra.Command, args []string) error {
			apply, _ := c.Flags().GetStringSlice("apply")
			yes, _ := c.Flags().GetBool("yes")
			if len(apply) == 0 { // a read-only check never needs the lock
				return a.updates.Run(nil, yes)
			}
			return a.locked("updates --apply", 0, func(*cobra.Command, []string) error { return a.updates.Run(apply, yes) })(c, args)
		})}
	updates.Flags().StringSlice("apply", nil,
		"apply updates: a container name (repeatable), or \"all\" for every routine (non-major) update")
	updates.Flags().Bool("yes", false, "skip the confirmation prompt")

	imgs := &cobra.Command{Use: "images", Short: "Check every pinned image runs on the platforms we support", Args: cobra.NoArgs,
		RunE: a.inRepo(func(c *cobra.Command, _ []string) error {
			want, _ := c.Flags().GetStringSlice("platforms")
			return a.images.Check(want)
		})}
	imgs.Flags().StringSlice("platforms", images.SupportedPlatforms, "platforms every pinned image must publish")

	uninstall := &cobra.Command{Use: "uninstall", Short: "Remove the stack + hsctl; keeps app data and backups unless told otherwise", Args: cobra.NoArgs,
		RunE: a.locked("uninstall", 0, func(c *cobra.Command, _ []string) error {
			data, _ := c.Flags().GetBool("data")
			all, _ := c.Flags().GetBool("all")
			yes, _ := c.Flags().GetBool("yes")
			return a.install.Uninstall(data, all, yes)
		})}
	uninstall.Flags().Bool("data", false, "also delete the app data volumes (asks to type DESTROY)")
	uninstall.Flags().Bool("all", false, "delete the app data volumes AND the backups (asks to type DESTROY)")
	uninstall.Flags().Bool("yes", false, "skip the confirmation — only for the default, data-keeping tier")

	root.AddCommand(a.setupCmd(), up, down, restart, status, getca, install, a.uiCmd(), updates, imgs, uninstall,
		a.appsCmd(), a.backupCmd(), a.secretsCmd())
	return root
}

// repairOwnership runs fsutil.RepairOwnership on the repo when we're root, and says what it fixed.
func (a *app) repairOwnership() {
	if os.Geteuid() != 0 || !paths.IsRepo(a.repo.Root) {
		return
	}
	if fixed := fsutil.RepairOwnership(a.repo.Root, paths.Owned); len(fixed) > 0 {
		fmt.Fprintf(os.Stderr, "gave %s back to the repo's owner (a root run had left them root-owned)\n",
			strings.Join(fixed, ", "))
	}
}
