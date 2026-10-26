// Package paths knows where the homeserver checkout is and where hsctl keeps its files in it.
package paths

import (
	"fmt"
	"os"
	"path/filepath"
)

// The files hsctl writes, relative to the repo.
const (
	SetupConf      = "setup.conf"
	BackupConf     = "backup.conf"
	ResticPassword = ".restic-password"
	BackupEnv      = ".backup-env" // cloud credentials for restic (S3 keys)
	UIPassword     = ".ui-password"
	LockFile       = ".hsctl.lock"
	Welcome        = "WELCOME.txt"
	CACert         = "caddy-root-ca.crt"
	PiholeList     = "pihole/custom.list"
	Services       = "services.json"
	Onboarding     = "ONBOARDING.md"

	Backups     = "backups"
	Staging     = "backups/staging"
	RestoreDir  = "backups/restore" // where a snapshot is extracted (git-ignored)
	LastBackup  = "backups/.last-backup"
	LastRun     = "backups/.last-run"
	LastReplica = "backups/.last-replica"
)

// EnvFiles are the generated per-service .env files. docker compose reads them on every up/down.
var EnvFiles = []string{"vaultwarden/.env", "nextcloud/.env", "pihole/.env", "caddy/.env"}

// Owned are the repo-relative files and directories hsctl writes, parents before children: the
// ones a root run must leave owned by the repo's owner (see fsutil.RepairOwnership).
var Owned = []string{
	Backups, Staging, LastBackup, LastRun, LastReplica,
	SetupConf, BackupConf, ResticPassword, BackupEnv, UIPassword,
	"vaultwarden/.env", "nextcloud/.env", "pihole/.env", "caddy/.env",
	"stirling/.env", "it-tools/.env", "imagetools/.env",
	PiholeList, Welcome, CACert, LockFile,
}

// Repo is a homeserver checkout.
type Repo struct {
	Root string
}

// Path joins elements onto the repo root.
func (r Repo) Path(elem ...string) string {
	return filepath.Join(append([]string{r.Root}, elem...)...)
}

// Require fails — instead of letting a command silently work on some other folder — when Root
// isn't a homeserver checkout.
func (r Repo) Require() error {
	if !IsRepo(r.Root) {
		return fmt.Errorf("not in the homeserver repo (resolved to %s)\n"+
			"run from the repo directory, or set HOMESERVER_DIR:\n"+
			"  cd /path/to/homeserver && hsctl ...\n"+
			"  # or: HOMESERVER_DIR=/path/to/homeserver hsctl ...", r.Root)
	}
	return nil
}

// IsRepo reports whether dir is a homeserver checkout (it has caddy/docker-compose.yml).
func IsRepo(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, "caddy", "docker-compose.yml"))
	return err == nil
}

// Guess finds the checkout hsctl should work on: $HOMESERVER_DIR, else the nearest folder at or
// above the working directory that is one, else the folder above the binary's. When none is, it
// returns the working directory — Require then says so.
func Guess() Repo {
	if d := os.Getenv("HOMESERVER_DIR"); d != "" {
		return Repo{Root: d}
	}
	if d, _ := os.Getwd(); d != "" {
		for {
			if IsRepo(d) {
				return Repo{Root: d}
			}
			parent := filepath.Dir(d)
			if parent == d {
				break
			}
			d = parent
		}
	}
	if exe, err := os.Executable(); err == nil {
		if up := filepath.Dir(filepath.Dir(exe)); IsRepo(up) {
			return Repo{Root: up}
		}
	}
	wd, _ := os.Getwd()
	return Repo{Root: wd}
}
