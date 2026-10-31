package backup

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/Rishikesh01/homeserver/hsctl/internal/docker"
	"github.com/Rishikesh01/homeserver/hsctl/internal/fsutil"
	"github.com/Rishikesh01/homeserver/hsctl/internal/layout"
	"github.com/Rishikesh01/homeserver/hsctl/internal/paths"
)

// RestoreOptions are `hsctl backup restore`'s choices.
type RestoreOptions struct {
	Snapshot    string // "" = latest
	Target      string // "" = backups/restore (emptied first, deleted after a put-back)
	IntoVolumes bool   // put it back into the live stack (disaster recovery)
	Yes         bool   // skip the confirmation for IntoVolumes
	// CurrentImages starts the restored data on the images this server pins now, instead of the
	// versions the backup was taken with (and the images it saved).
	CurrentImages bool
}

// Restore extracts a snapshot to a folder and, with IntoVolumes, puts it back into the stack.
// Without IntoVolumes it doesn't touch live data — putting volumes back is then the manual final
// step it prints.
func (s *Service) Restore(o RestoreOptions) error {
	if err := s.Restic.Require(); err != nil {
		return err
	}
	if o.IntoVolumes {
		if err := RequireRoot("backup restore --into-volumes", "it writes Docker's volume files"); err != nil {
			return err
		}
	}
	cfg, err := s.preflight()
	if err != nil {
		return err
	}
	userTarget := strings.TrimSpace(o.Target) != ""
	target := o.Target
	if !userTarget {
		target = s.Repo.Path(paths.RestoreDir)
	}
	// Putting back from a folder that already holds files would copy those leftovers into the live
	// volumes too (restic restore only adds files). Only the default scratch folder is ours to
	// empty; a --target you chose must start out empty.
	if userTarget && o.IntoVolumes {
		if entries, err := os.ReadDir(target); err == nil && len(entries) > 0 {
			return fmt.Errorf("--target %s isn't empty — with --into-volumes, anything already in it would be "+
				"copied into the live volumes too. Use an empty folder (or leave --target off)", target)
		}
	}
	snap := o.Snapshot
	if snap == "" {
		snap = "latest"
	}
	if err := s.RequirePassword(); err != nil {
		return err
	}
	// A user-supplied --target is the operator's own directory: extract INTO it without wiping
	// (restic overlays). Only the default reusable scratch dir gets emptied first, so a prior
	// extraction there can't contaminate an --into-volumes put-back.
	if userTarget {
		if err := fsutil.MkdirAllOwned(target, 0700); err != nil {
			return err
		}
	} else if err := PrepareTarget(target); err != nil {
		return err
	}
	if err := s.Restic.Run(cfg.Repo, "restore", snap, "--target", target); err != nil {
		return err
	}
	fmt.Fprintf(s.Out, "\nExtracted snapshot %q to %s (DB dump + volume data + config files).\n", snap, target)

	if o.IntoVolumes {
		if !o.Yes {
			fmt.Fprintln(s.Out, "\n--into-volumes will STOP the stack, WIPE each data volume, and overwrite it")
			fmt.Fprintln(s.Out, "with the snapshot's contents (Vaultwarden from its staged copy), then start the stack")
			if o.CurrentImages {
				fmt.Fprintln(s.Out, "on the app versions this server pins now.")
			} else {
				fmt.Fprintln(s.Out, "on the app versions the backup was taken with.")
			}
			if !s.Ask.CanAsk() || !s.Ask.YesNo("Proceed with this destructive restore?", false) {
				fmt.Fprintln(s.Out, "aborted — the snapshot is still extracted at", target)
				return nil
			}
		}
		err := s.IntoVolumes(target, o.CurrentImages)
		// The extraction holds plaintext copies of every secret and database; whether the put-back
		// worked or not, the snapshot still has it all, so the copy goes. (A --target you chose is
		// yours to keep.)
		if !userTarget {
			if rerr := os.RemoveAll(target); rerr == nil {
				fmt.Fprintln(s.Out, "\nremoved the extracted copy at", target)
			}
		}
		return err
	}

	fmt.Fprintln(s.Out, "It holds plaintext copies of your data and secrets — delete it when you're done.")
	fmt.Fprintln(s.Out, "To put it back into the stack (disaster recovery), or just run `sudo hsctl backup restore --into-volumes`:")
	fmt.Fprintln(s.Out, "  1. hsctl down")
	fmt.Fprintln(s.Out, "  2. for each volume, EMPTY it first (so deleted files don't come back), then copy the snapshot's files in:")
	fmt.Fprintln(s.Out, "       sudo find /var/lib/docker/volumes/<name>/_data -mindepth 1 -delete")
	fmt.Fprintf(s.Out, "       sudo cp -a %s/var/lib/docker/volumes/<name>/_data/. /var/lib/docker/volumes/<name>/_data/\n", target)
	fmt.Fprintln(s.Out, "     (this restores the Postgres DB volume too)")
	fmt.Fprintln(s.Out, "  3. hsctl up")
	fmt.Fprintln(s.Out, "  Vaultwarden: step 2 restores its volume (attachments/keys) but NOT its DB — the")
	fmt.Fprintln(s.Out, "  consistent SQLite fileset (copied during a brief stop) is in backups/staging/vaultwarden/.")
	fmt.Fprintln(s.Out, "  Overlay it (db.sqlite3 + any -wal/-shm) onto the restored volume before step 3:")
	fmt.Fprintln(s.Out, "       cp -a <target>/.../backups/staging/vaultwarden/db.sqlite3* \\")
	fmt.Fprintln(s.Out, "            /var/lib/docker/volumes/vaultwarden_vw-data/_data/")
	fmt.Fprintln(s.Out, "  (If staging was skipped at backup time, the volume already includes its DB — nothing to overlay.)")
	fmt.Fprintln(s.Out, "  Nextcloud: the database volume was copied while Postgres ran; the consistent copy is the")
	fmt.Fprintln(s.Out, "  SQL dump in backups/staging/nextcloud-db.sql (`--into-volumes` rebuilds the database from it).")
	if staging := findStaging(target); staging != "" && fsutil.Exists(filepath.Join(staging, ImageManifestFile)) {
		fmt.Fprintln(s.Out, "  App versions: the images the backup was taken with are listed in")
		fmt.Fprintf(s.Out, "  %s (`--into-volumes` pins them again).\n", filepath.Join(staging, ImageManifestFile))
		if fsutil.Exists(filepath.Join(staging, ImageArchiveDir)) {
			fmt.Fprintf(s.Out, "  The images themselves are in %s — load each one with:\n", filepath.Join(staging, ImageArchiveDir))
			fmt.Fprintf(s.Out, "       docker load -i %s/<image>.tar\n", filepath.Join(staging, ImageArchiveDir))
		}
	}
	return nil
}

// PrepareTarget makes target an empty directory before `restic restore` writes into it. restic
// restore is ADDITIVE — it won't remove files already in the target that aren't in the snapshot —
// and the default target is reused across restores. Without wiping first, leftovers from an
// earlier extraction (individual files, or a whole volume dir) would be copied into the live
// volumes by a put-back, resurrecting deleted data or even a volume not in the requested snapshot.
// Guarded so a blank/mistyped target can't wipe a system path.
func PrepareTarget(target string) error {
	clean := filepath.Clean(target)
	if clean == "" || clean == "." || clean == "/" || clean == filepath.Dir(clean) {
		return fmt.Errorf("refusing to use unsafe restore target %q", target)
	}
	if err := os.RemoveAll(clean); err != nil {
		if errors.Is(err, fs.ErrPermission) {
			return fmt.Errorf("an earlier extraction run as root is still in %s and this user can't remove it — "+
				"it holds plaintext copies of your data: sudo rm -rf %s", clean, clean)
		}
		return fmt.Errorf("clearing restore target %s: %w", clean, err)
	}
	return fsutil.MkdirAllOwned(clean, 0700)
}

// Plan is what a snapshot extracted under a target holds, worked out before anything is stopped
// or wiped.
type Plan struct {
	VolsRoot  string   // <target>/var/lib/docker/volumes
	Volumes   []string // volume names with a _data dir to put back
	VWInLoop  bool     // the Vaultwarden volume is among them
	VWStaging string   // the staged Vaultwarden DB fileset, "" when the snapshot has none
	NCDump    string   // a complete Nextcloud database dump, "" if the snapshot has none
	NCVolume  string   // which Nextcloud database volume the snapshot holds, "" if none
	Images    string   // the record of the app versions it was taken with, "" for a backup from before hsctl kept one
}

// PlanRestore checks an extracted snapshot BEFORE the stack is touched: a snapshot that can't be
// used must leave the running stack alone. It finds the staged databases wherever the backup was
// taken — the snapshot stores the repo's absolute path at backup time, which differs on a new
// machine or a moved repo — refuses a snapshot whose Vaultwarden volume came without its database
// (instead of restoring an empty password vault and calling it a success), and refuses one whose
// Nextcloud database only exists in the pre-Postgres-18 volume.
func PlanRestore(target string) (Plan, error) {
	p := Plan{VolsRoot: filepath.Join(target, "var", "lib", "docker", "volumes")}
	entries, err := os.ReadDir(p.VolsRoot)
	if err != nil {
		return p, fmt.Errorf("no volume data found in the snapshot (looked for %s): %w — nothing was changed", p.VolsRoot, err)
	}
	for _, e := range entries {
		if e.IsDir() && fsutil.Exists(filepath.Join(p.VolsRoot, e.Name(), "_data")) {
			p.Volumes = append(p.Volumes, e.Name())
		}
	}
	p.VWInLoop = slices.Contains(p.Volumes, layout.VaultwardenVolume)
	if staging := findStaging(target); staging != "" {
		if d := filepath.Join(staging, "vaultwarden"); fsutil.Exists(d) {
			p.VWStaging = d
		}
		// An older hsctl could leave a truncated dump behind; only a complete one is trusted.
		if d := filepath.Join(staging, NextcloudDumpFile); DumpComplete(d) {
			p.NCDump = d
		}
		if m := filepath.Join(staging, ImageManifestFile); fsutil.Exists(m) {
			p.Images = m
		}
	}
	for _, v := range layout.NextcloudDBVolumes {
		if slices.Contains(p.Volumes, v) {
			p.NCVolume = v
			break
		}
	}
	if p.NCVolume != "" && p.NCVolume != layout.NextcloudDBVolumes[0] {
		// A snapshot from before the move to Postgres 18: its database is in a volume this stack no
		// longer runs, in a format Postgres 18 can't open. Putting it back would report success
		// and leave Nextcloud on an empty database.
		return p, fmt.Errorf("the snapshot's Nextcloud database is in the old %s volume (Postgres before 18), "+
			"which this stack no longer uses — restore it by hand: see docs/backup-restore.md "+
			"(Manual restore); nothing was changed", p.NCVolume)
	}
	if len(p.Volumes) == 0 && p.VWStaging == "" {
		return p, fmt.Errorf("the snapshot holds no volume data under %s — nothing was changed", p.VolsRoot)
	}
	if p.VWInLoop && !fsutil.Exists(filepath.Join(p.VolsRoot, layout.VaultwardenVolume, "_data", "db.sqlite3")) &&
		(p.VWStaging == "" || !fsutil.Exists(filepath.Join(p.VWStaging, "db.sqlite3"))) {
		return p, fmt.Errorf("the snapshot has Vaultwarden's files but not its database (no db.sqlite3 in the " +
			"volume and no staged copy under backups/staging/vaultwarden) — refusing to restore an empty " +
			"password vault; nothing was changed")
	}
	return p, nil
}

// findStaging returns the directory under target that holds the backup's staged databases
// (…/backups/staging: Vaultwarden's SQLite fileset and Nextcloud's dump), wherever the repo lived
// when the backup was taken. The volume tree is skipped: it's big, and staging is never inside it.
func findStaging(target string) string {
	volsRoot := filepath.Join(target, "var", "lib", "docker", "volumes")
	suffix := string(filepath.Separator) + filepath.Join("backups", "staging")
	found := ""
	_ = filepath.WalkDir(target, func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil
		}
		if path == volsRoot {
			return filepath.SkipDir
		}
		if strings.HasSuffix(path, suffix) {
			found = path
			return fs.SkipAll
		}
		return nil
	})
	return found
}

// IntoVolumes is the automated disaster-recovery put-back: given an already-extracted snapshot
// under target, it writes every backed-up volume's data back into the live Docker volumes and
// brings the stack up — on the app versions the backup was taken with, loading the images it saved,
// unless currentImages keeps this server's own pins. DESTRUCTIVE — it stops the stack and wipes each
// target volume before copying. This is the one-command form of the manual steps Restore prints,
// including the Vaultwarden special case (its database is in staging, not in its volume).
func (s *Service) IntoVolumes(target string, currentImages bool) (err error) {
	plan, err := PlanRestore(target)
	if err != nil {
		return err
	}
	fmt.Fprintf(s.Out, "\nthe snapshot holds %d volume(s): %s\n", len(plan.Volumes), strings.Join(plan.Volumes, " "))

	// The app versions go with the data: Nextcloud won't start on data from a newer version, and
	// Postgres can't read another major's. The saved images load now, while the stack still runs;
	// the pins move only once the volumes are back, so a restore that fails before then leaves the
	// current data on the current versions.
	fmt.Fprintln(s.Out, "\n== app images ==")
	var imgs *ImageManifest
	if currentImages {
		fmt.Fprintln(s.Out, "keeping the images this server pins now (--current-images)")
	} else if imgs = s.backupImages(plan); imgs != nil {
		s.loadImages(plan, *imgs)
	}
	var problems []string // went wrong, but the stack can still come up restored

	fmt.Fprintln(s.Out, "\n== stopping the stack ==")
	if err := s.Stack.Down(false, false); err != nil {
		return fmt.Errorf("stack down: %w", err)
	}
	// From here on the stack is down. If anything fails, say exactly what was and wasn't put back,
	// and bring Caddy up so the dashboard — the only way in from the LAN — shows the failure.
	var done []string
	restoredAll, stackUp := false, false
	defer func() {
		if err == nil || stackUp {
			return
		}
		if restoredAll {
			fmt.Fprintln(s.Err, "\nEvery volume was restored, but the stack didn't fully start — see above.")
			return // Up always starts Caddy, even past a failed app
		}
		var left []string
		for _, v := range plan.Volumes {
			if !slices.Contains(done, v) {
				left = append(left, v)
			}
		}
		fmt.Fprintf(s.Err, "\nrestore failed part-way. Restored: %s. NOT restored: %s.\n", listOrNone(done), listOrNone(left))
		fmt.Fprintln(s.Err, "The apps are stopped. Bringing Caddy back up so the dashboard is reachable...")
		_ = s.Stack.StartProxy()
	}()

	for _, name := range plan.Volumes {
		if err := s.restoreVolume(name, filepath.Join(plan.VolsRoot, name, "_data")); err != nil {
			return fmt.Errorf("restore volume %s: %w", name, err)
		}
		done = append(done, name)
	}

	// Vaultwarden — handle both snapshot layouts:
	//   - NEW: the volume (attachments/keys, no DB) was restored by the loop above; overlay the
	//     consistent db.sqlite3 fileset from staging onto it.
	//   - OLD: the whole volume lives in staging (and isn't in the volume loop) — restore it whole.
	// With no staged copy, Vaultwarden was down at backup time and its DB is in the volume itself.
	switch {
	case plan.VWInLoop && plan.VWStaging != "" && fsutil.Exists(filepath.Join(plan.VWStaging, "db.sqlite3")):
		mp, err := s.Docker.VolumeMountpoint(layout.VaultwardenVolume)
		if err != nil {
			return fmt.Errorf("cannot overlay the Vaultwarden DB: %w", err)
		}
		// Overlay the whole staged fileset (main + -wal + -shm) so SQLite replays any WAL, owned like
		// the volume it lands in (the staged copy belongs to the admin, not the container).
		vuid, vgid, ok := fsutil.OwnerOf(mp)
		for _, suf := range dbFilesetSuffixes {
			f := filepath.Join(plan.VWStaging, "db.sqlite3"+suf)
			if !fsutil.Exists(f) {
				continue
			}
			dst := filepath.Join(mp, "db.sqlite3"+suf)
			if err := fsutil.CopyFile(f, dst, s.Err); err != nil {
				return fmt.Errorf("overlay vaultwarden db.sqlite3%s: %w", suf, err)
			}
			if ok {
				_ = os.Lchown(dst, vuid, vgid)
			}
		}
		fmt.Fprintln(s.Out, "  restored vaultwarden SQLite fileset (db.sqlite3 + WAL)")
	case !plan.VWInLoop && plan.VWStaging != "":
		// Older snapshot: staging holds the entire volume.
		if err := s.restoreVolume(layout.VaultwardenVolume, plan.VWStaging); err != nil {
			return fmt.Errorf("restore vaultwarden (legacy staging layout): %w", err)
		}
		done = append(done, layout.VaultwardenVolume)
	}

	// Before anything starts again — the Nextcloud database rebuild below is the first to.
	if imgs != nil {
		fmt.Fprintln(s.Out, "\n== app versions ==")
		problems = append(problems, s.pinImages(*imgs)...)
	}

	// Rebuild Nextcloud's database from the consistent dump (the raw volume was copied live) — only
	// into the volume the compose file actually runs on.
	if plan.NCDump != "" && plan.NCVolume == layout.NextcloudDBVolumes[0] {
		fmt.Fprintln(s.Out, "\n== nextcloud database ==")
		if err := s.restoreNextcloudDB(plan.NCDump, plan.NCVolume, filepath.Join(plan.VolsRoot, plan.NCVolume, "_data")); err != nil {
			return err
		}
	}

	restoredAll = true
	fmt.Fprintf(s.Out, "\nrestored %d volume(s). starting the stack...\n\n== starting the stack ==\n", len(done))
	if err := s.Stack.Up(); err != nil {
		return err
	}
	stackUp = true
	if len(problems) > 0 {
		return fmt.Errorf("every volume was restored and the stack is up, but:\n  %s", strings.Join(problems, "\n  "))
	}
	return nil
}

// listOrNone joins names for a message, or says "none".
func listOrNone(names []string) string {
	if len(names) == 0 {
		return "none"
	}
	return strings.Join(names, ", ")
}

// restoreVolume makes sure the named volume exists, wipes it, and copies src/. into it.
func (s *Service) restoreVolume(name, src string) error {
	if _, err := s.Docker.Output(s.Repo.Root, "volume", "create", name); err != nil { // no-op if it already exists
		return fmt.Errorf("create volume: %w", err)
	}
	mp, err := s.Docker.VolumeMountpoint(name)
	if err != nil {
		return err
	}
	if err := fsutil.WipeDirContents(mp); err != nil {
		return fmt.Errorf("wipe live volume: %w", err)
	}
	if err := fsutil.CopyContents(src, mp, s.Err); err != nil {
		return fmt.Errorf("copy restored data: %w", err)
	}
	fmt.Fprintf(s.Out, "  restored %s\n", name)
	return nil
}

// restoreNextcloudDB rebuilds Nextcloud's database from the snapshot's consistent dump, on top of
// the just-restored raw volume (which supplies the roles and the password Nextcloud's config.php
// expects). The raw volume was copied while Postgres ran, so its tables aren't guaranteed
// consistent; the dump is. Postgres is started on its own, the database is dropped and re-created
// from the dump, and the rest of the stack starts afterwards. If anything goes wrong, the raw
// volume is put back as it was in the snapshot and the restore carries on with that; only if that
// fails too — the database volume left wiped or half-imported — is it an error.
func (s *Service) restoreNextcloudDB(dump, volume, rawVolume string) error {
	why, cause := s.rebuildNextcloudDB(dump)
	if cause == nil {
		fmt.Fprintln(s.Out, "  rebuilt the nextcloud database from its consistent dump")
		return nil
	}
	fmt.Fprintf(s.Err, "  nextcloud db: couldn't rebuild from the dump (%s: %v) — using the raw volume copy instead\n", why, cause)
	_, _ = s.Docker.Combined(s.Repo.Path(layout.NextcloudFolder), "compose", "stop", layout.NextcloudDBSvc)
	if err := s.restoreVolume(volume, rawVolume); err != nil {
		return fmt.Errorf("nextcloud database: rebuilding it from the dump failed (%s: %v), and putting the "+
			"snapshot's raw database volume back failed too: %w", why, cause, err)
	}
	fmt.Fprintln(s.Err, "  nextcloud db: restored from the raw volume copy (taken while Postgres ran — check Nextcloud once it's up)")
	return nil
}

// rebuildNextcloudDB starts Postgres alone, drops Nextcloud's database and imports the dump. On
// failure it says which step failed and why.
func (s *Service) rebuildNextcloudDB(dump string) (step string, err error) {
	user, db := s.nextcloudDBNames()
	if err := s.Docker.EnsureNetwork(layout.EdgeNetwork); err != nil {
		return "network", err
	}
	if out, err := s.Docker.Combined(s.Repo.Path(layout.NextcloudFolder), "compose", "up", "-d", layout.NextcloudDBSvc); err != nil {
		return "start postgres", fmt.Errorf("%v: %s", err, strings.TrimSpace(out))
	}
	// NOT pg_isready alone: during first start Postgres may run a temporary server. Gate on a query.
	ready := func() bool {
		return s.Docker.Command(s.Repo.Root, "exec", layout.NextcloudDB, "psql", "-U", user, "-d", "postgres", "-tAc", "select 1").Run() == nil
	}
	if !docker.Wait(120*time.Second, ready) {
		return "postgres didn't become ready", fmt.Errorf("timed out after 2 minutes")
	}
	psql := func(sql string) error {
		out, err := s.Docker.Combined(s.Repo.Root, "exec", layout.NextcloudDB, "psql", "-U", user, "-d", "postgres",
			"-v", "ON_ERROR_STOP=1", "-c", sql)
		if err != nil {
			return fmt.Errorf("%v: %s", err, strings.TrimSpace(out))
		}
		return nil
	}
	if err := psql(fmt.Sprintf(`DROP DATABASE IF EXISTS "%s" WITH (FORCE)`, db)); err != nil {
		return "drop the copied database", err
	}
	if err := psql(fmt.Sprintf(`CREATE DATABASE "%s" OWNER "%s"`, db, user)); err != nil {
		return "create the database", err
	}
	f, err := os.Open(dump)
	if err != nil {
		return "open the dump", err
	}
	defer f.Close()
	imp := s.Docker.Command(s.Repo.Root, "exec", "-i", layout.NextcloudDB, "psql", "-q", "-U", user, "-d", db, "-v", "ON_ERROR_STOP=1")
	var errOut strings.Builder
	imp.Stdin, imp.Stdout, imp.Stderr = f, io.Discard, &errOut
	if err := imp.Run(); err != nil {
		return "import the dump", fmt.Errorf("%v: %s", err, strings.TrimSpace(errOut.String()))
	}
	return "", nil
}
