package backup

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/Rishikesh01/homeserver/hsctl/internal/envfile"
	"github.com/Rishikesh01/homeserver/hsctl/internal/fsutil"
	"github.com/Rishikesh01/homeserver/hsctl/internal/layout"
	"github.com/Rishikesh01/homeserver/hsctl/internal/paths"
	"github.com/Rishikesh01/homeserver/hsctl/internal/restic"
)

// Volumes returns every Docker volume that belongs to one of the stack's services (a volume is
// named "<compose-project>_<name>", and the project is the service's folder), minus pure caches.
// Discovering volumes instead of hardcoding them means a newly added service's data is protected
// automatically — no list to keep in sync, so a service can't silently go missing from backups.
func (s *Service) Volumes() ([]string, error) {
	all, err := s.Docker.Volumes()
	if err != nil {
		return nil, fmt.Errorf("listing docker volumes (is docker up?): %w", err)
	}
	ours := map[string]bool{}
	for _, svc := range layout.Services {
		ours[svc] = true
	}
	var vols []string
	for _, name := range all {
		if layout.CacheVolumes[name] {
			continue
		}
		project := name
		if i := strings.IndexByte(name, '_'); i >= 0 {
			project = name[:i]
		}
		if ours[project] {
			vols = append(vols, name)
		}
	}
	sort.Strings(vols)
	return vols, nil
}

// Init creates the encrypted repository (first time) and switches the nightly timer on.
func (s *Service) Init() error {
	cfg, err := s.preflight()
	if err != nil {
		return err
	}
	if err := s.refuseInitOnSystemDisk(cfg); err != nil {
		return err
	}
	passFile := s.Repo.Path(paths.ResticPassword)
	generated := !fsutil.Exists(passFile)
	if err := s.ensurePassword(cfg); err != nil {
		return err
	}
	// Record the restic version we're initialising with, so `backup verify` can later catch a
	// restic that drifted (e.g. after a system upgrade).
	if cfg.ResticVersion == "" {
		if v, err := s.Restic.Version(); err == nil {
			cfg.ResticVersion = v
			_ = s.SaveSettings(cfg)
			fmt.Fprintf(s.Out, "pinned restic version baseline: %s\n", v)
		}
	}
	defer s.handBackLocalRepo(cfg)
	if err := s.Restic.Run(cfg.Repo, "init"); err != nil {
		// A password generated just now must never outlive a failed init: for a repository that
		// already exists (only detectable for a remote one by trying), it would stand in for the
		// lost real password and make every later run fail with "wrong password".
		if generated {
			_ = os.Remove(passFile)
			fmt.Fprintln(s.Err, "removed the password generated for this init — nothing was created")
		}
		return err
	}
	// There's somewhere to back up to now, so the nightly timer can do its job.
	if !fsutil.Exists(s.Repo.Path(paths.BackupConf)) {
		if err := s.SaveSettings(cfg); err != nil {
			return err
		}
	}
	s.EnableTimer()
	return nil
}

// List prints the snapshots.
func (s *Service) List() error {
	cfg, err := s.preflight()
	if err != nil {
		return err
	}
	if err := s.RequirePassword(); err != nil {
		return err
	}
	out, err := s.Restic.Output(cfg.Repo, "snapshots")
	fmt.Fprint(s.Out, out)
	if err != nil {
		if restic.RepoMissing(out) {
			return fmt.Errorf("there's no backup repository at %s yet — create it with: sudo hsctl backup init", cfg.Repo)
		}
		return err
	}
	if !strings.Contains(out, "snapshot") || strings.TrimSpace(out) == "" {
		fmt.Fprintln(s.Out, "no snapshots yet — take one with: sudo hsctl backup run")
	}
	return nil
}

// Snapshots returns restic's snapshot listing for the dashboard (its combined output).
func (s *Service) Snapshots() (string, error) {
	return s.Restic.Output(s.Settings().Repo, "snapshots")
}

// Stats returns the repository's raw-data stats for the dashboard.
func (s *Service) Stats() (string, error) {
	out, err := s.Restic.Output(s.Settings().Repo, "stats", "--mode", "raw-data")
	return strings.TrimSpace(out), err
}

// Forget applies the retention policy and prunes.
func (s *Service) Forget() error {
	cfg, err := s.preflight()
	if err != nil {
		return err
	}
	defer s.handBackLocalRepo(cfg)
	if err := s.RequirePassword(); err != nil {
		return err
	}
	args, err := ForgetArgs(cfg.Retention)
	if err != nil {
		return fmt.Errorf("%w\nfix it with: hsctl backup config --retention '%s'", err, RetentionExample)
	}
	return s.Restic.Run(cfg.Repo, args...)
}

// Check checks the real repository: restic's structural check plus re-reading subset of the data.
func (s *Service) Check(subset string) error {
	cfg, err := s.preflight()
	if err != nil {
		return err
	}
	if err := s.RequirePassword(); err != nil {
		return err
	}
	fmt.Fprintf(s.Out, "checking %s (structure, plus re-reading %s of the stored data)...\n", cfg.Repo, subset)
	if err := s.Restic.Run(cfg.Repo, "check", "--read-data-subset="+subset); err != nil {
		return fmt.Errorf("the backup repository has problems — see restic's output above: %w", err)
	}
	fmt.Fprintln(s.Out, "\nthe backup repository is intact.")
	return nil
}

// RunOptions are `hsctl backup run`'s choices.
type RunOptions struct {
	// Images saves the app images themselves into this snapshot (true) or leaves them out (false),
	// whatever BACKUP_IMAGES says; nil follows the setting. Their versions are recorded either way.
	Images *bool
}

// Run takes a snapshot: a fresh Nextcloud database dump, Vaultwarden's database fileset, every data
// volume, the config and the app images' versions (with the images themselves, if asked), then
// applies the retention policy and copies to the off-site replica.
func (s *Service) Run(o RunOptions) error {
	cfg, err := s.preflight()
	if err != nil {
		return err
	}
	defer s.handBackLocalRepo(cfg)
	if err := s.RequirePassword(); err != nil {
		return err
	}
	if err := s.Docker.Require(); err != nil {
		return err
	}
	if cur, err := s.Restic.Version(); err == nil && cfg.ResticVersion != "" && cur != cfg.ResticVersion {
		fmt.Fprintf(s.Err, "warning: restic %s differs from pinned %s — the `apt-mark hold restic` "+
			"pin may have been overridden by a system upgrade. Run `hsctl backup verify` to re-test.\n",
			cur, cfg.ResticVersion)
	}
	staging := s.Repo.Path(paths.Staging)
	if err := fsutil.MkdirAllOwned(staging, 0700); err != nil {
		return err
	}
	var problems []string // things that went wrong but still left a usable snapshot
	if err := s.dumpNextcloudDB(staging); err != nil {
		fmt.Fprintln(s.Err, err)
		problems = append(problems, err.Error())
	}
	// Stage ONLY Vaultwarden's SQLite DB (the one thing unsafe to copy live). Its attachments, keys
	// and config are static files backed up live with the volume below — so the stop stays ~1s no
	// matter how big attachments grow. vwExclude is the live DB path to skip (it'd be torn).
	vwExclude := s.stageVaultwardenDB(staging)

	vols, err := s.Volumes()
	if err != nil {
		return fmt.Errorf("discovering docker volumes: %w", err)
	}
	// Refuse to take an "empty" backup. A real stack always has data volumes; zero means docker
	// discovery came back blank (daemon hiccup, stack down) — and since we prune right after, an
	// empty snapshot would delete older, good snapshots. Fail loudly instead.
	if len(vols) == 0 {
		return fmt.Errorf("no stack data volumes found — is the stack up (hsctl status)? " +
			"refusing to take an empty backup that would then prune your good snapshots")
	}
	fmt.Fprintf(s.Out, "volumes to back up (%d): %s\n", len(vols), strings.Join(vols, " "))
	var targets []string
	for _, v := range vols {
		// A volume we just discovered must resolve. If it doesn't, abort rather than write a
		// snapshot that's silently missing a service's data (and then prune behind it).
		mp, err := s.Docker.VolumeMountpoint(v)
		if err != nil {
			return fmt.Errorf("aborting rather than writing an incomplete snapshot: %w", err)
		}
		targets = append(targets, mp)
	}
	targets = append(targets, staging)
	for _, svc := range layout.Services { // every app's .env: secrets, settings and any applied image update
		if p := s.Repo.Path(svc, ".env"); fsutil.Exists(p) {
			targets = append(targets, p)
		}
	}
	for _, rel := range SnapshotConfigFiles {
		if p := s.Repo.Path(rel); fsutil.Exists(p) {
			targets = append(targets, p)
		}
	}
	// The app images last, once nothing else can stop the backup: saving them takes a while. They
	// land in staging, and leave it again once restic has them — they're GBs of the server's disk.
	saveImages := cfg.Images
	if o.Images != nil {
		saveImages = *o.Images
	}
	defer os.RemoveAll(filepath.Join(staging, ImageArchiveDir))
	problems = append(problems, s.stageImages(staging, saveImages)...)

	args := []string{"backup", "--tag", "homeserver"}
	if vwExclude != "" {
		// Exclude the live (torn) DB from the volume backup; the consistent copy is in staging.
		args = append(args, "--exclude", vwExclude)
	}
	if err := s.Restic.Run(cfg.Repo, append(args, targets...)...); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() == 3 {
			// restic saved a snapshot, but some files couldn't be read. Don't prune behind an
			// incomplete snapshot, and say so plainly instead of "exit status 3".
			return fmt.Errorf("the snapshot is INCOMPLETE — restic couldn't read some files (see above); " +
				"old snapshots were not pruned")
		}
		return err
	}
	// The snapshot is safely written — record that now, so a later prune failure doesn't make the
	// dashboard claim the backup itself is overdue.
	s.stamp(paths.LastBackup)
	if cfg.Retention != "" {
		args, err := ForgetArgs(cfg.Retention)
		if err != nil {
			return fmt.Errorf("the snapshot was saved, but old ones were NOT pruned: %w\n"+
				"fix it with: hsctl backup config --retention '%s'", err, RetentionExample)
		}
		if err := s.Restic.Run(cfg.Repo, args...); err != nil {
			return err
		}
	}
	// The off-site copy goes with every backup — nightly included — so it never quietly falls behind.
	if cfg.ReplicaRepo != "" {
		fmt.Fprintln(s.Out, "\n== off-site replica ==")
		if err := s.replicate(cfg); err != nil {
			problems = append(problems, "copying it off-site failed: "+FirstLine(err.Error()))
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("the snapshot was saved, but with problems:\n  %s", strings.Join(problems, "\n  "))
	}
	return nil
}

// Replicate copies every snapshot the off-site replica doesn't have yet.
func (s *Service) Replicate() error {
	cfg, err := s.preflight()
	if err != nil {
		return err
	}
	return s.replicate(cfg)
}

// replicate copies every snapshot the off-site replica doesn't have yet from the primary repo
// (`restic copy` — incremental and deduplicated), creating the replica repo on first run, then
// applies the same retention there. Combined with the primary this gives 3-2-1 coverage: the
// replica survives fire/theft/disk death at home.
func (s *Service) replicate(cfg Settings) error {
	if cfg.ReplicaRepo == "" {
		return fmt.Errorf("no off-site replica configured. Set one first:\n"+
			"  hsctl backup config --replica sftp:user@nas:/backups   (or s3:host/bucket, or a local path)\n"+
			"Cloud credentials (e.g. AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY) go in %s, one KEY=VALUE per line.",
			paths.BackupEnv)
	}
	if err := s.RequirePassword(); err != nil {
		return err
	}
	// First run: create the replica repo. `cat config` is the cheap does-it-exist probe — but only
	// "no repository there" means create one; anything else (wrong password, host unreachable) is
	// the real problem to report, not a reason to try `init` over an existing repo.
	if out, err := s.Restic.Output(cfg.ReplicaRepo, "cat", "config"); err != nil {
		if !restic.RepoMissing(out) {
			return fmt.Errorf("can't open the replica %s: %s", cfg.ReplicaRepo, FirstLine(out))
		}
		fmt.Fprintln(s.Out, "replica repo not initialized yet — creating it...")
		if err := s.Restic.Run(cfg.ReplicaRepo, "init"); err != nil {
			return fmt.Errorf("initialize replica %s: %w", cfg.ReplicaRepo, err)
		}
	}
	fmt.Fprintf(s.Out, "copying snapshots to the replica: %s\n", cfg.ReplicaRepo)
	if err := s.Restic.Run(cfg.ReplicaRepo, "copy",
		"--from-repo", cfg.Repo, "--from-password-file", s.Repo.Path(paths.ResticPassword)); err != nil {
		return fmt.Errorf("copy to replica: %w", err)
	}
	s.stamp(paths.LastReplica)
	if cfg.Retention != "" {
		args, err := ForgetArgs(cfg.Retention)
		if err != nil {
			return fmt.Errorf("copied, but the replica was NOT pruned: %w", err)
		}
		return s.Restic.Run(cfg.ReplicaRepo, args...)
	}
	return nil
}

// dbFilesetSuffixes are the SQLite WAL-mode fileset parts. db.sqlite3-wal can hold committed writes
// NOT yet checkpointed into the main file, and db.sqlite3-shm is its index — so all three must be
// copied/restored together or recent writes are lost (this exact bug shipped once).
var dbFilesetSuffixes = []string{"", "-wal", "-shm"}

// stageVaultwardenDB copies Vaultwarden's SQLite fileset (db.sqlite3 + -wal + -shm) into
// staging/vaultwarden/ and returns the live DB-glob to EXCLUDE from the volume backup. Only the DB
// fileset is unsafe to copy live (the three files read at different instants can be torn);
// everything else — attachments, rsa_key.pem, config.json — is static and backed up live with the
// volume. So the container is stopped only long enough to copy the (small) fileset, then
// restarted. The stop stays ~1s regardless of how large attachments grow, which is the whole point.
//
// ALL of -wal/-shm are copied too (not just db.sqlite3): docker stop does NOT reliably checkpoint
// the WAL, so recent writes can still be sitting in db.sqlite3-wal. Copying the fileset and
// restoring it lets SQLite replay the WAL on open — no committed write is left behind.
//
// Best-effort, like dumpNextcloudDB: if Vaultwarden isn't running (the at-rest volume is then
// already consistent) or any step fails, nothing is staged and it returns "" — the backup then
// takes the whole live volume (DB included). Mirror any change here in the self-test's
// verifyVaultwarden and in restoreIntoVolumes (which overlays the staged fileset).
func (s *Service) stageVaultwardenDB(staging string) (excludePath string) {
	svc := layout.Vaultwarden
	// Clear the last run's copy FIRST, whatever happens next: a snapshot taken while Vaultwarden is
	// stopped carries its database in the volume, and a stale staged copy beside it would be laid
	// over that newer database on restore. Every path below that doesn't finish staging also leaves
	// the folder gone, never half-filled.
	dest := filepath.Join(staging, "vaultwarden")
	if err := os.RemoveAll(dest); err != nil {
		fmt.Fprintln(s.Err, "vaultwarden: clearing staging dir:", err)
		return ""
	}
	if s.Docker.ContainerState(svc) != "running" {
		return "" // stack down: the volume (incl. DB) isn't being written, so the live backup is consistent
	}
	mp, err := s.Docker.VolumeMountpoint(layout.VaultwardenVolume)
	if err != nil {
		fmt.Fprintf(s.Err, "vaultwarden: data volume not found, backing up live volume as-is: %v\n", err)
		return ""
	}
	if err := fsutil.MkdirAllOwned(dest, 0700); err != nil {
		fmt.Fprintln(s.Err, "vaultwarden: staging mkdir:", err)
		return ""
	}
	// An interrupt (Ctrl-C, systemd stopping the timer's run, a reboot) during the stop window
	// would skip the deferred restart below and leave the password manager down — and stopped on
	// purpose, so `restart: unless-stopped` wouldn't even bring it back after a reboot. Catch it,
	// restart Vaultwarden, then exit.
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	windowDone := make(chan struct{})
	go func() {
		select {
		case sig := <-sigs:
			fmt.Fprintf(s.Err, "\nvaultwarden: interrupted (%v) during the backup's brief stop — restarting it before exiting\n", sig)
			_, _ = s.Docker.Output(s.Repo.Root, "start", svc)
			os.Exit(130)
		case <-windowDone:
		}
	}()
	if _, err := s.Docker.Output(s.Repo.Root, "stop", svc); err != nil {
		signal.Stop(sigs)
		close(windowDone)
		_ = os.RemoveAll(dest)
		fmt.Fprintf(s.Err, "vaultwarden: stop failed, backing up live volume as-is: %v\n", err)
		return ""
	}
	// Always restart, even if the copy fails — never leave the password manager down.
	defer func() {
		if _, err := s.Docker.Output(s.Repo.Root, "start", svc); err != nil {
			fmt.Fprintln(s.Err, "vaultwarden: WARNING — failed to restart after backup, start it manually:", err)
		}
		signal.Stop(sigs)
		close(windowDone)
	}()
	if !fsutil.Exists(filepath.Join(mp, "db.sqlite3")) {
		_ = os.RemoveAll(dest)
		fmt.Fprintln(s.Err, "vaultwarden: db.sqlite3 not found, backing up live volume as-is")
		return ""
	}
	// Copy the whole fileset (main + -wal + -shm). -wal/-shm may be absent (already checkpointed);
	// copy whatever exists. Missing -wal would silently drop uncheckpointed writes.
	for _, suf := range dbFilesetSuffixes {
		f := filepath.Join(mp, "db.sqlite3"+suf)
		if !fsutil.Exists(f) {
			continue
		}
		staged := filepath.Join(dest, "db.sqlite3"+suf)
		if err := fsutil.CopyFile(f, staged, s.Err); err != nil {
			_ = os.RemoveAll(dest)
			fmt.Fprintln(s.Err, "vaultwarden: DB fileset copy to staging failed, backing up live volume as-is:", err)
			return ""
		}
		fsutil.InheritOwner(staged) // the admin's, like the rest of backups/ (restore re-owns it to match the volume)
	}
	fmt.Fprintln(s.Out, "vaultwarden: staged the SQLite fileset (db.sqlite3 + WAL; container stopped ~1s); attachments backed up live")
	// Exclude the live DB fileset from the volume backup — the consistent copy is in staging.
	// restic matches this glob against each file's absolute path.
	return filepath.Join(mp, "db.sqlite3*")
}

// NextcloudDumpFile is the consistent Postgres dump inside the staging dir.
const NextcloudDumpFile = "nextcloud-db.sql"

// PgDumpFooter ends every complete plain-format pg_dump. A dump without it was cut short.
const PgDumpFooter = "-- PostgreSQL database dump complete"

// dumpNextcloudDB writes a consistent SQL dump of Nextcloud's database into staging — the copy a
// restore rebuilds the database from (the raw volume is read while Postgres runs, so it isn't
// guaranteed consistent). It writes to a temp file and only renames it into place once pg_dump
// succeeded, and on any failure removes the old dump: a snapshot carrying the PREVIOUS backup's
// dump would quietly restore stale data. With Nextcloud's database stopped there's nothing to
// dump (its volume is at rest, so the volume copy is consistent) and it returns nil.
func (s *Service) dumpNextcloudDB(staging string) error {
	dumpPath := filepath.Join(staging, NextcloudDumpFile)
	if s.Docker.ContainerState(layout.NextcloudDB) != "running" {
		_ = os.Remove(dumpPath)
		return nil
	}
	tmp := dumpPath + ".tmp"
	f, err := fsutil.CreateOwned(tmp, 0600)
	if err != nil {
		_ = os.Remove(dumpPath)
		return fmt.Errorf("nextcloud db dump: cannot create %s: %w", tmp, err)
	}
	user, db := s.nextcloudDBNames()
	// No -T: that's docker-compose syntax; plain `docker exec` rejects it. No TTY is needed —
	// pg_dump's stdout is redirected to the dump file below.
	c := s.Docker.Command(s.Repo.Root, "exec", layout.NextcloudDB, "pg_dump", "-U", user, db)
	c.Stdout, c.Stderr = f, s.Err
	runErr := c.Run()
	closeErr := f.Close()
	if runErr == nil && closeErr == nil && DumpComplete(tmp) {
		if err := os.Rename(tmp, dumpPath); err == nil {
			return nil
		}
	}
	_ = os.Remove(tmp)
	_ = os.Remove(dumpPath)
	if runErr == nil {
		runErr = fmt.Errorf("the dump came out incomplete")
	}
	return fmt.Errorf("nextcloud db dump failed (%v) — this snapshot has only the raw database volume", runErr)
}

// nextcloudDBNames returns the Postgres user and database from nextcloud/.env (with the defaults
// the compose file uses).
func (s *Service) nextcloudDBNames() (user, db string) {
	user, db = "nextcloud", "nextcloud"
	if kv, err := envfile.Read(s.Repo.Path(layout.NextcloudFolder, ".env")); err == nil {
		if v := kv["POSTGRES_USER"]; v != "" {
			user = v
		}
		if v := kv["POSTGRES_DB"]; v != "" {
			db = v
		}
	}
	return user, db
}

// DumpComplete reports whether a plain-format pg_dump file ends with pg_dump's completion footer.
func DumpComplete(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || fi.Size() == 0 {
		return false
	}
	tail := make([]byte, 512)
	off := fi.Size() - int64(len(tail))
	if off < 0 {
		off, tail = 0, tail[:fi.Size()]
	}
	n, _ := f.ReadAt(tail, off)
	return strings.Contains(string(tail[:n]), PgDumpFooter)
}
