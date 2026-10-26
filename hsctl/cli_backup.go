package main

import (
	"github.com/Rishikesh01/homeserver/hsctl/internal/backup"
	"github.com/Rishikesh01/homeserver/hsctl/internal/paths"
	"github.com/spf13/cobra"
)

func (a *app) backupCmd() *cobra.Command {
	b := &cobra.Command{Use: "backup", Short: "Encrypted backups (restic)"}

	cfgCmd := &cobra.Command{Use: "config", Short: "Set the backup destination / retention / password",
		Args: cobra.NoArgs, RunE: a.inRepo(a.runBackupConfig)}
	cfgCmd.Flags().String("repo", "", "restic repository (local path / sftp:user@host:/path / s3:…)")
	cfgCmd.Flags().String("retention", "", "restic --keep-* options, e.g. '"+backup.RetentionExample+"'")
	cfgCmd.Flags().String("password", "", "set or change the backup password (\"-\" reads it from stdin); changing it re-keys the repository")
	cfgCmd.Flags().Bool("pin-restic", false, "record the installed restic version as the pinned baseline")
	cfgCmd.Flags().String("require-mount", "", "refuse backups unless this path is a real mount (empty string disables)")
	cfgCmd.Flags().String("replica", "", "off-site replica repository for 'backup replicate' (empty string disables)")
	cfgCmd.Flags().String("notify-url", "", "URL to POST a message to when a backup fails, e.g. https://ntfy.sh/<topic> (empty string disables)")

	initCmd := &cobra.Command{Use: "init", Short: "Create the encrypted restic repo (first time)", Args: cobra.NoArgs,
		RunE: a.inRepo(a.locked("backup init", 0, func(*cobra.Command, []string) error { return a.backup.Init() }))}

	runCmd := &cobra.Command{Use: "run", Short: "Take a snapshot (DB dump + data volumes + config) [root]", Args: cobra.NoArgs,
		RunE: a.inRepo(func(cmd *cobra.Command, args []string) error {
			// The nightly timer passes --wait, so it waits out a dashboard job or a terminal
			// command instead of skipping the night's backup.
			wait, _ := cmd.Flags().GetDuration("wait")
			// Record every outcome — including a run refused before it started (another operation
			// holding the lock, disk not mounted, no password) — so a failing nightly backup shows
			// on the dashboard and, if configured, sends a notification.
			err := backup.RequireRoot("backup run", "it reads Docker's volume files")
			if err == nil {
				err = a.locked("backup run", wait, func(*cobra.Command, []string) error { return a.backup.Run() })(cmd, args)
			}
			a.backup.RecordRun(err)
			return err
		})}
	runCmd.Flags().Duration("wait", 0, "if another hsctl operation is running, wait up to this long for it (e.g. 30m)")

	listCmd := &cobra.Command{Use: "list", Aliases: []string{"snapshots"}, Short: "List snapshots", Args: cobra.NoArgs,
		RunE: a.inRepo(func(*cobra.Command, []string) error { return a.backup.List() })}

	forgetCmd := &cobra.Command{Use: "forget", Short: "Apply the retention policy and prune", Args: cobra.NoArgs,
		RunE: a.inRepo(a.locked("backup forget", 0, func(*cobra.Command, []string) error { return a.backup.Forget() }))}

	restoreCmd := &cobra.Command{Use: "restore [snapshot]", Short: "Extract a snapshot (default: latest); --into-volumes puts it back [root]",
		Args: cobra.MaximumNArgs(1), RunE: a.inRepo(a.locked("backup restore", 0, func(c *cobra.Command, args []string) error {
			var o backup.RestoreOptions
			if len(args) > 0 {
				o.Snapshot = args[0]
			}
			o.Target, _ = c.Flags().GetString("target")
			o.IntoVolumes, _ = c.Flags().GetBool("into-volumes")
			o.Yes, _ = c.Flags().GetBool("yes")
			return a.backup.Restore(o)
		}))}
	restoreCmd.Flags().String("target", "", "directory to restore into (default: <repo>/"+paths.RestoreDir+")")
	restoreCmd.Flags().Bool("into-volumes", false, "DR put-back: stop the stack, restore every volume in place (incl. Vaultwarden from staging), bring it up")
	restoreCmd.Flags().Bool("yes", false, "skip the confirmation prompt for --into-volumes")

	verifyCmd := &cobra.Command{Use: "verify", Aliases: []string{"selftest", "test"},
		Short: "Self-test backup+restore on a throwaway Docker volume (never touches live data) [root]",
		Args:  cobra.NoArgs, RunE: a.inRepo(func(c *cobra.Command, _ []string) error {
			image, _ := c.Flags().GetString("image")
			keep, _ := c.Flags().GetBool("keep")
			return a.backup.Verify(image, keep)
		})}
	verifyCmd.Flags().String("image", "", "container image for the test fixture (default: a local image)")
	verifyCmd.Flags().Bool("keep", false, "keep the test volume + temp repo afterwards (for debugging)")

	replicateCmd := &cobra.Command{Use: "replicate", Short: "Copy all snapshots to the off-site replica repo",
		Args: cobra.NoArgs, RunE: a.inRepo(a.locked("backup replicate", 0, func(*cobra.Command, []string) error { return a.backup.Replicate() }))}

	checkCmd := &cobra.Command{Use: "check", Short: "Check your REAL backup repository is intact (restic check + re-read a sample of the data)",
		Args: cobra.NoArgs, RunE: a.inRepo(a.locked("backup check", 0, func(c *cobra.Command, _ []string) error {
			subset, _ := c.Flags().GetString("read-data-subset")
			return a.backup.Check(subset)
		}))}
	checkCmd.Flags().String("read-data-subset", "5%", "how much stored data to re-read and verify (e.g. 5%, 1/10, or 100% for all)")

	b.AddCommand(cfgCmd, initCmd, runCmd, listCmd, restoreCmd, verifyCmd, checkCmd, forgetCmd, replicateCmd)
	return b
}

// runBackupConfig hands `backup config` the flags that were actually given (an empty string clears
// the guard, the replica or the notification URL).
func (a *app) runBackupConfig(cmd *cobra.Command, _ []string) error {
	f := cmd.Flags()
	str := func(name string) *string {
		if !f.Changed(name) {
			return nil
		}
		v, _ := f.GetString(name)
		return &v
	}
	o := backup.Options{Repo: str("repo"), Retention: str("retention"), RequireMount: str("require-mount"),
		Replica: str("replica"), NotifyURL: str("notify-url")}
	o.PinRestic, _ = f.GetBool("pin-restic")
	if v, _ := f.GetString("password"); v != "" {
		pw, err := backup.ReadPasswordArg(v)
		if err != nil {
			return err
		}
		o.Password = pw
	}
	return a.backup.Configure(o)
}
