package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

func cmdBackup(args []string) error {
	repo := repoDir()
	if len(args) == 0 {
		return fmt.Errorf("usage: hsctl backup <config|init|run|list|forget>")
	}
	sub, rest := args[0], args[1:]
	if sub == "config" {
		return backupConfig(repo, rest)
	}
	if err := requireRestic(); err != nil {
		return err
	}
	cfg := loadBackupCfg(repo)
	switch sub {
	case "init":
		ensureResticPassword(repo)
		return resticRun(repo, cfg, "init")
	case "run":
		return backupRun(repo, cfg)
	case "list", "snapshots":
		return resticRun(repo, cfg, "snapshots")
	case "forget":
		return resticRun(repo, cfg, append([]string{"forget", "--prune"}, strings.Fields(cfg.Retention)...)...)
	default:
		return fmt.Errorf("unknown backup subcommand %q", sub)
	}
}

func backupConfig(repo string, args []string) error {
	fs := flag.NewFlagSet("backup config", flag.ContinueOnError)
	cfg := loadBackupCfg(repo)
	fs.StringVar(&cfg.Repo, "repo", cfg.Repo, "restic repository (local path / sftp: / b2: / s3:)")
	fs.StringVar(&cfg.Retention, "retention", cfg.Retention, "restic forget policy")
	pw := fs.String("password", "", "set the restic repo password (stored in .restic-password)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *pw != "" {
		if err := writeFile0600(filepath.Join(repo, resticPassFile), *pw+"\n"); err != nil {
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
