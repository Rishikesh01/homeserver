package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

// Backups use restic: encrypted, deduplicated, snapshotted, many backends (local
// path, USB, SFTP to another host, S3/Backblaze B2). What we protect:
//   - a fresh Postgres dump (consistent DB) + every data volume's files
//   - the per-service .env + setup.conf (so a restore can rebuild the stack)
// Volume files live under /var/lib/docker/volumes, so `backup run` needs root
// (run via sudo, or from the root-owned systemd timer).

var backupVolumes = []string{
	"vaultwarden_vw-data",
	"nextcloud_nc-data", "nextcloud_db-data", "nextcloud_nc-html",
	"caddy_caddy-data",
}

const (
	backupConfFile = "backup.conf"
	resticPassFile = ".restic-password"
	defaultRetention = "--keep-daily 7 --keep-weekly 4 --keep-monthly 6"
)

type backupCfg struct {
	Repo      string // restic repository (e.g. /mnt/usb/restic, sftp:user@host:/path, b2:bucket:path)
	Retention string
}

func loadBackupCfg(repo string) backupCfg {
	c := backupCfg{
		Repo:      filepath.Join(repo, "backups", "restic"), // safe default: local; change for off-box
		Retention: defaultRetention,
	}
	if kv, err := readKV(filepath.Join(repo, backupConfFile)); err == nil {
		if v := kv["RESTIC_REPO"]; v != "" {
			c.Repo = v
		}
		if v := kv["RETENTION"]; v != "" {
			c.Retention = v
		}
	}
	return c
}

func (c backupCfg) save(repo string) error {
	return writeFile0600(filepath.Join(repo, backupConfFile), fmt.Sprintf(
		"# hsctl backup config. RESTIC_REPO: local path, sftp:user@host:/path, b2:bucket:path, s3:...\n"+
			"RESTIC_REPO=%s\nRETENTION=%s\n", c.Repo, c.Retention))
}

// backupCmd builds the `hsctl backup` command tree.
func backupCmd() *cobra.Command {
	b := &cobra.Command{Use: "backup", Short: "Encrypted backups (restic)"}

	cfgCmd := &cobra.Command{Use: "config", Short: "Set the backup destination / retention / password",
		Args: cobra.NoArgs, RunE: runBackupConfig}
	cfgCmd.Flags().String("repo", "", "restic repository (local path / sftp: / b2: / s3:)")
	cfgCmd.Flags().String("retention", "", "restic forget policy")
	cfgCmd.Flags().String("password", "", "set the restic repo password (stored in .restic-password)")

	// withRestic wraps a run that needs restic + a loaded config.
	withRestic := func(run func(repo string, cfg backupCfg) error) func(*cobra.Command, []string) error {
		return func(*cobra.Command, []string) error {
			if err := requireRestic(); err != nil {
				return err
			}
			repo := repoDir()
			return run(repo, loadBackupCfg(repo))
		}
	}

	initCmd := &cobra.Command{Use: "init", Short: "Create the encrypted restic repo (first time)", Args: cobra.NoArgs,
		RunE: withRestic(func(repo string, cfg backupCfg) error { ensureResticPassword(repo); return resticRun(repo, cfg, "init") })}
	runCmd := &cobra.Command{Use: "run", Short: "Take a snapshot (DB dump + data volumes + config)", Args: cobra.NoArgs,
		RunE: withRestic(backupRun)}
	listCmd := &cobra.Command{Use: "list", Aliases: []string{"snapshots"}, Short: "List snapshots", Args: cobra.NoArgs,
		RunE: withRestic(func(repo string, cfg backupCfg) error { return resticRun(repo, cfg, "snapshots") })}
	forgetCmd := &cobra.Command{Use: "forget", Short: "Apply the retention policy and prune", Args: cobra.NoArgs,
		RunE: withRestic(func(repo string, cfg backupCfg) error {
			return resticRun(repo, cfg, append([]string{"forget", "--prune"}, strings.Fields(cfg.Retention)...)...)
		})}

	restoreCmd := &cobra.Command{Use: "restore [snapshot]", Short: "Extract a snapshot (default: latest) to --target",
		Args: cobra.MaximumNArgs(1), RunE: runBackupRestore}
	restoreCmd.Flags().String("target", "", "directory to restore into (default: <repo>/restore)")

	b.AddCommand(cfgCmd, initCmd, runCmd, listCmd, restoreCmd, forgetCmd)
	return b
}

func runBackupConfig(cmd *cobra.Command, _ []string) error {
	repo := repoDir()
	cfg := loadBackupCfg(repo)
	f := cmd.Flags()
	if f.Changed("repo") {
		cfg.Repo, _ = f.GetString("repo")
	}
	if f.Changed("retention") {
		cfg.Retention, _ = f.GetString("retention")
	}
	if pw, _ := f.GetString("password"); pw != "" {
		if err := writeFile0600(filepath.Join(repo, resticPassFile), pw+"\n"); err != nil {
			return err
		}
	}
	if err := cfg.save(repo); err != nil {
		return err
	}
	fmt.Printf("saved %s (repo=%s)\n", backupConfFile, cfg.Repo)
	fmt.Println("next: hsctl backup init   then   hsctl backup run   (run needs root for volume files)")
	return nil
}

func backupRun(repo string, cfg backupCfg) error {
	ensureResticPassword(repo)
	staging := filepath.Join(repo, "backups", "staging")
	if err := os.MkdirAll(staging, 0700); err != nil {
		return err
	}
	dumpNextcloudDB(repo, staging) // best-effort; warns inside

	var paths []string
	for _, v := range backupVolumes {
		if mp, err := dockerOut(repo, "volume", "inspect", "-f", "{{.Mountpoint}}", v); err == nil && mp != "" {
			paths = append(paths, mp)
		} else {
			fmt.Fprintf(os.Stderr, "skip volume %s (not found)\n", v)
		}
	}
	paths = append(paths, staging)
	for _, s := range services {
		if p := filepath.Join(repo, s, ".env"); fileExists(p) {
			paths = append(paths, p)
		}
	}
	if p := filepath.Join(repo, confFile); fileExists(p) {
		paths = append(paths, p)
	}

	if err := resticRun(repo, cfg, append([]string{"backup", "--tag", "homeserver"}, paths...)...); err != nil {
		return err
	}
	if cfg.Retention != "" {
		return resticRun(repo, cfg, append([]string{"forget", "--prune"}, strings.Fields(cfg.Retention)...)...)
	}
	return nil
}

// runBackupRestore extracts a snapshot to a directory (default <repo>/restore). It does
// NOT overwrite live data — putting volumes/DB back into the stack is the manual final
// step (printed below, and in README -> Backup & restore), because it's destructive.
func runBackupRestore(cmd *cobra.Command, args []string) error {
	if err := requireRestic(); err != nil {
		return err
	}
	repo := repoDir()
	cfg := loadBackupCfg(repo)
	target, _ := cmd.Flags().GetString("target")
	if target == "" {
		target = filepath.Join(repo, "restore")
	}
	snap := "latest"
	if len(args) > 0 {
		snap = args[0]
	}
	ensureResticPassword(repo)
	if err := os.MkdirAll(target, 0700); err != nil {
		return err
	}
	if err := resticRun(repo, cfg, "restore", snap, "--target", target); err != nil {
		return err
	}
	fmt.Printf("\nExtracted snapshot %q to %s (DB dump + volume data + config files).\n", snap, target)
	fmt.Println("To put it back into the stack (disaster recovery):")
	fmt.Println("  1. hsctl down")
	fmt.Println("  2. copy each restored volume's files back into /var/lib/docker/volumes/<name>/_data")
	fmt.Println("  3. import the Postgres dump into the nextcloud-db container (see README)")
	fmt.Println("  4. hsctl up")
	return nil
}

// dumpNextcloudDB writes a consistent SQL dump (preferred over the raw volume on restore).
func dumpNextcloudDB(repo, staging string) {
	f, err := os.Create(filepath.Join(staging, "nextcloud-db.sql"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "db dump: cannot create file:", err)
		return
	}
	defer f.Close()
	c := dockerCmd(repo, "exec", "-T", "nextcloud-db", "pg_dump", "-U", "nextcloud", "nextcloud")
	c.Stdout, c.Stderr = f, os.Stderr
	if err := c.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "db dump: skipped (is nextcloud-db up?):", err)
	}
}

func resticRun(repo string, cfg backupCfg, args ...string) error {
	c := exec.Command("restic", args...)
	c.Env = append(os.Environ(),
		"RESTIC_REPOSITORY="+cfg.Repo,
		"RESTIC_PASSWORD_FILE="+filepath.Join(repo, resticPassFile),
	)
	c.Stdout, c.Stderr, c.Stdin = os.Stdout, os.Stderr, os.Stdin
	return c.Run()
}

func resticInstalled() bool { _, err := exec.LookPath("restic"); return err == nil }

// resticOutput captures combined output (for the UI to display snapshots).
func resticOutput(repo string, cfg backupCfg, args ...string) (string, error) {
	c := exec.Command("restic", args...)
	c.Env = append(os.Environ(),
		"RESTIC_REPOSITORY="+cfg.Repo,
		"RESTIC_PASSWORD_FILE="+filepath.Join(repo, resticPassFile))
	out, err := c.CombinedOutput()
	return string(out), err
}

func requireRestic() error {
	if _, err := exec.LookPath("restic"); err != nil {
		return fmt.Errorf("restic not installed — install it first:\n" +
			"  sudo apt-get install -y restic   (or download from https://restic.net)")
	}
	return nil
}

func ensureResticPassword(repo string) {
	path := filepath.Join(repo, resticPassFile)
	if fileExists(path) {
		return
	}
	pw := genPassword(32)
	_ = writeFile0600(path, pw+"\n")
	fmt.Printf("generated restic repo password -> %s  (BACK THIS UP; without it backups are unrecoverable)\n", path)
}
