package backup

import (
	"fmt"
	"path/filepath"

	"github.com/Rishikesh01/homeserver/hsctl/internal/envfile"
	"github.com/Rishikesh01/homeserver/hsctl/internal/fsutil"
	"github.com/Rishikesh01/homeserver/hsctl/internal/paths"
)

// DefaultRetention is the policy a new backup.conf starts with.
const DefaultRetention = "--keep-daily 7 --keep-weekly 4 --keep-monthly 6"

// Settings is backup.conf: where backups go and how they're kept.
type Settings struct {
	Repo          string // restic repository (e.g. /mnt/usb/restic, sftp:user@host:/path, s3:host/bucket)
	Retention     string
	ResticVersion string // pinned restic version; `backup verify` fails if the installed one differs
	RequireMount  string // if set, a path that MUST be on a mounted backup disk (see RequireMount)
	ReplicaRepo   string // optional second restic repo (off-site) every backup copies to
	NotifyURL     string // optional URL that gets a POST when a backup run fails (ntfy-style)
}

// ConfigFiles are the backup settings a run depends on. If one exists but can't be read, loading
// would fall back to defaults — and back up to the wrong place, or with a new password.
var ConfigFiles = []string{paths.BackupConf, paths.ResticPassword, paths.BackupEnv}

// SnapshotConfigFiles are the repo files (besides each app's .env) every snapshot carries alongside
// the data: what a rebuild on new hardware needs to come back as it was. (.restic-password isn't
// one — a snapshot is useless without it, so it must live elsewhere.)
var SnapshotConfigFiles = []string{
	paths.SetupConf, paths.PiholeList, paths.UIPassword, paths.Services, paths.BackupConf, paths.BackupEnv,
}

// Settings loads backup.conf, with defaults for anything unset.
func (s *Service) Settings() Settings {
	c := Settings{
		Repo:      s.Repo.Path("backups", "restic"), // safe default: local; change for off-box
		Retention: DefaultRetention,
	}
	if kv, err := envfile.Read(s.Repo.Path(paths.BackupConf)); err == nil {
		if v := kv["RESTIC_REPO"]; v != "" {
			c.Repo = v
		}
		if v := kv["RETENTION"]; v != "" {
			c.Retention = v
		}
		c.ResticVersion = kv["RESTIC_VERSION"] // empty = no version pinned yet
		c.RequireMount = kv["REQUIRE_MOUNT"]   // empty = no mount guard (e.g. default repo on root disk)
		c.ReplicaRepo = kv["REPLICA_REPO"]     // empty = no off-site replica configured
		c.NotifyURL = kv["NOTIFY_URL"]         // empty = no failure notifications
	}
	return c
}

// SaveSettings writes backup.conf.
func (s *Service) SaveSettings(c Settings) error {
	out := fmt.Sprintf(
		"# hsctl backup config. RESTIC_REPO: local path, sftp:user@host:/path, s3:...\n"+
			"RESTIC_REPO=%s\nRETENTION=%s\n", c.Repo, c.Retention)
	if c.ResticVersion != "" {
		out += "# Pinned restic version. `backup verify` FAILS if the installed restic differs, so a\n" +
			"# system upgrade that swaps restic out is caught. Re-baseline: backup config --pin-restic\n" +
			"RESTIC_VERSION=" + c.ResticVersion + "\n"
	}
	if c.RequireMount != "" {
		out += "# Backup ops refuse to run unless this path is on a mounted backup disk, so a backup\n" +
			"# never lands on the system disk when the external disk isn't mounted.\n" +
			"REQUIRE_MOUNT=" + c.RequireMount + "\n"
	}
	if c.ReplicaRepo != "" {
		out += "# Off-site replica: every backup copies itself here (same encryption password).\n" +
			"# Cloud credentials go in " + paths.BackupEnv + " (KEY=VALUE per line).\n" +
			"REPLICA_REPO=" + c.ReplicaRepo + "\n"
	}
	if c.NotifyURL != "" {
		out += "# POSTed a plain-text message when a backup run fails (e.g. an ntfy topic URL).\n" +
			"NOTIFY_URL=" + c.NotifyURL + "\n"
	}
	return fsutil.WritePrivate(s.Repo.Path(paths.BackupConf), out)
}

// Options are `hsctl backup config`'s changes; nil fields stay as they are.
type Options struct {
	Repo, Retention, RequireMount, Replica, NotifyURL *string
	Password                                          string // "" = keep; see SetPassword
	PinRestic                                         bool
}

// Configure applies opts to backup.conf (and the password file).
func (s *Service) Configure(opts Options) error {
	// Saving over a backup.conf we couldn't read would replace the real destination with defaults.
	if err := fsutil.RequireReadable(s.Repo.Root, ConfigFiles...); err != nil {
		return err
	}
	cfg := s.Settings()
	if opts.Repo != nil {
		old := cfg.Repo
		cfg.Repo = *opts.Repo
		// A destination on an external-disk mountpoint gets the disk guard automatically, so a
		// backup can't land on the system disk while the disk is unplugged.
		if opts.RequireMount == nil {
			if g, changed := FollowGuard(cfg.RequireMount, old, cfg.Repo); changed {
				cfg.RequireMount = g
				if g != "" {
					fmt.Fprintf(s.Out, "set the backup-disk guard to %s: backups refuse to run unless a disk is mounted there\n"+
						"(turn it off with --require-mount \"\")\n", g)
				}
			}
		}
	}
	if opts.Retention != nil {
		if _, err := ParseRetention(*opts.Retention); err != nil {
			return err
		}
		cfg.Retention = *opts.Retention
	}
	if opts.RequireMount != nil {
		cfg.RequireMount = *opts.RequireMount // empty string clears the guard
	}
	if opts.Replica != nil {
		cfg.ReplicaRepo = *opts.Replica // empty string clears the replica
	}
	if opts.NotifyURL != nil {
		cfg.NotifyURL = *opts.NotifyURL // empty string turns notifications off
	}
	if opts.Password != "" {
		if err := s.SetPassword(cfg, opts.Password, s.Out); err != nil {
			return err
		}
	}
	if opts.PinRestic {
		v, err := s.Restic.Version()
		if err != nil {
			return fmt.Errorf("--pin-restic: %w", err)
		}
		cfg.ResticVersion = v
		fmt.Fprintf(s.Out, "pinned restic version: %s\n", v)
	}
	if err := s.SaveSettings(cfg); err != nil {
		return err
	}
	fmt.Fprintf(s.Out, "saved %s (repo=%s)\n", paths.BackupConf, cfg.Repo)
	if w := GuardMismatch(cfg); w != "" {
		fmt.Fprintln(s.Err, "warning: "+w)
	}
	fmt.Fprintln(s.Out, "next: sudo hsctl backup init   then   sudo hsctl backup run   (they read Docker's volume files)")
	return nil
}

// handBackLocalRepo gives a restic repository that lives inside the homeserver repo (the default
// backups/restic) back to the admin after root wrote to it, so `hsctl backup list` and plain
// `restic` work without sudo. A repo elsewhere (an external disk under /mnt, sftp:, …) is left
// exactly as restic made it.
func (s *Service) handBackLocalRepo(cfg Settings) {
	if !filepath.IsAbs(cfg.Repo) || !fsutil.Within(cfg.Repo, s.Repo.Root) {
		return
	}
	// Walk from the repo's top-level folder that holds it (backups/, which restic may have created
	// itself), so the folders in between are handed back too.
	rel, err := filepath.Rel(s.Repo.Root, cfg.Repo)
	if err != nil || rel == "." {
		return
	}
	top := rel
	if i := indexSep(rel); i >= 0 {
		top = rel[:i]
	}
	fsutil.HandBackTree(s.Repo.Path(top), s.Repo.Root)
}

func indexSep(p string) int {
	for i := 0; i < len(p); i++ {
		if p[i] == filepath.Separator {
			return i
		}
	}
	return -1
}
