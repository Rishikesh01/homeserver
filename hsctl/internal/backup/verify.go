package backup

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/Rishikesh01/homeserver/hsctl/internal/docker"
	"github.com/Rishikesh01/homeserver/hsctl/internal/fsutil"
	"github.com/Rishikesh01/homeserver/hsctl/internal/secret"
)

// verifyRestoreIntoVolume proves the destructive put-back primitive that `restore --into-volumes`
// relies on (restoreOneVolume): it must REPLACE a live volume's contents with the snapshot's —
// old data gone, restored data present. Uses a throwaway volume; never touches the real stack.
func (s *Service) verifyRestoreIntoVolume(keep bool) error {
	work, err := os.MkdirTemp("", "hsctl-verify-into-")
	if err != nil {
		return err
	}
	if !keep {
		defer os.RemoveAll(work)
	}
	id := strings.ToLower(secret.Password(8))
	vol := "hsctl-verify-into-" + id
	if _, err := s.Docker.Output(s.Repo.Root, "volume", "create", vol); err != nil {
		return fmt.Errorf("create test volume: %w", err)
	}
	if !keep {
		defer func() { _, _ = s.Docker.Output(s.Repo.Root, "volume", "rm", "-f", vol) }()
	}
	mp, err := s.Docker.VolumeMountpoint(vol)
	if err != nil {
		return err
	}
	// Seed the live volume with STALE data that must be gone after the restore.
	if err := os.WriteFile(filepath.Join(mp, "stale.txt"), []byte(secret.Password(16)), 0644); err != nil {
		return fmt.Errorf("seed stale file: %w", err)
	}
	// A fake "snapshot" source holding the data we expect to land in the volume.
	src := filepath.Join(work, "src")
	if err := os.MkdirAll(src, 0700); err != nil {
		return err
	}
	token := secret.Password(24)
	if err := os.WriteFile(filepath.Join(src, "restored.txt"), []byte(token), 0644); err != nil {
		return err
	}
	if err := s.restoreVolume(vol, src); err != nil {
		return fmt.Errorf("restoreOneVolume: %w", err)
	}
	got, err := os.ReadFile(filepath.Join(mp, "restored.txt"))
	if err != nil {
		return fmt.Errorf("restored file missing: %w", err)
	}
	if strings.TrimSpace(string(got)) != token {
		return fmt.Errorf("restored token %q != %q", strings.TrimSpace(string(got)), token)
	}
	if fsutil.Exists(filepath.Join(mp, "stale.txt")) {
		return fmt.Errorf("stale file survived — volume was not wiped before the restore")
	}
	fmt.Fprintln(s.Out, "  seeded stale data -> restoreOneVolume -> stale gone, restored data present")
	return nil
}

// Verify is a self-contained backup+restore test. It NEVER touches the live
// stack or the real repo: it spins up a throwaway Docker volume, writes a random token
// into it from a container, backs that volume up into a temporary isolated restic repo,
// wipes the volume, restores from the repo, and has a container read the token back —
// asserting it matches. Exercises the real restic backup/restore path end to end, and is
// safe to run anytime (e.g. after upgrades) to confirm backups actually work.
//
// Needs root for the same reason `backup run` does: restic reads the volume's files under
// /var/lib/docker/volumes (root-only).
func (s *Service) Verify(image string, keep bool) error {
	if err := s.Restic.Require(); err != nil {
		return err
	}
	if err := RequireRoot("backup verify", "it reads Docker's volume files"); err != nil {
		return err
	}
	if image == "" {
		// Default to any image already on the box, so we never need to pull. We override
		// its entrypoint below, so even app images (postgres, redis, …) work as plain sh.
		if img := s.Docker.FirstLocalImage(); img != "" {
			image = img
		} else {
			image = "busybox"
		}
	}
	cfg := s.Settings()

	// --- restic version pin: catch a restic that drifted from the pinned one ----------
	cur, err := s.Restic.Version()
	if err != nil {
		return err
	}
	fmt.Fprintf(s.Out, "verify: installed restic %s\n", cur)
	switch {
	case cfg.ResticVersion == "":
		fmt.Fprintln(s.Out, "verify: WARNING — no pinned restic version recorded yet.")
		fmt.Fprintln(s.Out, "        Lock today's in with:  sudo hsctl backup config --pin-restic")
		fmt.Fprintln(s.Out, "        (so a later system upgrade that swaps restic out is caught here.)")
	case cur != cfg.ResticVersion:
		return fmt.Errorf("restic version drift: installed %s but backup.conf pins %s.\n"+
			"a system upgrade likely changed restic — re-pin it (sudo apt-mark hold restic) and\n"+
			"reinstall %s; or, if intentional and re-tested: sudo hsctl backup config --pin-restic",
			cur, cfg.ResticVersion, cfg.ResticVersion)
	default:
		fmt.Fprintf(s.Out, "verify: matches the pinned version (%s)\n", cfg.ResticVersion)
	}

	// --- service-shaped sub-tests, each fully isolated (own throwaway volume/containers +
	//     temp repo). The important ones are Vaultwarden (your passwords) and the Nextcloud
	//     Postgres DB (the pg_dump path that the -T bug used to break).
	tests := []struct {
		name string
		run  func() error
	}{
		{"restic volume round-trip", func() error { return s.verifyVolumeRoundTrip(image, keep) }},
		{"restore --into-volumes put-back (throwaway volume)", func() error { return s.verifyRestoreIntoVolume(keep) }},
		{"Vaultwarden — passwords (boots from a restored volume)", func() error { return s.verifyVaultwarden(keep) }},
		{"Vaultwarden — WAL not dropped (db+wal+shm fileset)", func() error { return s.verifyVaultwardenWAL(keep) }},
		{"Nextcloud — database (pg_dump -> restic -> import)", func() error { return s.verifyNextcloudDB(keep) }},
	}
	failed, skips := 0, 0
	for _, t := range tests {
		fmt.Fprintf(s.Out, "\n========== %s ==========\n", t.name)
		err := t.run()
		var sk skipErr
		switch {
		case errors.As(err, &sk):
			fmt.Fprintf(s.Out, "SKIPPED: %s\n  %s\n", t.name, sk.why)
			skips++
		case err != nil:
			fmt.Fprintf(s.Out, "FAIL: %s\n  %v\n", t.name, err)
			failed++
		default:
			fmt.Fprintf(s.Out, "PASS: %s\n", t.name)
		}
	}
	passed := len(tests) - failed - skips
	fmt.Fprintf(s.Out, "\n========== verify: %d passed, %d failed, %d skipped ==========\n", passed, failed, skips)
	if failed > 0 {
		return fmt.Errorf("%d backup self-test(s) FAILED — see above", failed)
	}
	if skips > 0 {
		fmt.Fprintln(s.Out, "Not everything was tested — see the SKIPPED checks above for what they need.")
		return nil
	}
	fmt.Fprintln(s.Out, "Backups verified end-to-end: restic round-trips data, Vaultwarden boots from a")
	fmt.Fprintln(s.Out, "restored volume with its DB intact, and the Nextcloud DB dump restores cleanly.")
	fmt.Fprintln(s.Out, "(This tests the backup MECHANISM on throwaway data. To check your real backups, run: sudo hsctl backup check)")
	return nil
}

// skipErr marks a self-test that couldn't run — reported as SKIPPED, never counted as a pass.
type skipErr struct{ why string }

func (e skipErr) Error() string { return "skipped: " + e.why }

func skipped(why string) error { return skipErr{why} }

// newIsolatedRepo sets up a fresh restic repo under work with its own password and returns
// a runner bound to it — so each sub-test stays isolated from the real backup repo.
func (s *Service) newIsolatedRepo(work string) (func(args ...string) error, error) {
	repoPath := filepath.Join(work, "repo")
	passFile := filepath.Join(work, "pass")
	if err := fsutil.WritePrivate(passFile, secret.Password(24)+"\n"); err != nil {
		return nil, err
	}
	return func(args ...string) error {
		c := exec.Command("restic", args...)
		c.Env = append(os.Environ(), "RESTIC_REPOSITORY="+repoPath, "RESTIC_PASSWORD_FILE="+passFile)
		c.Stdout, c.Stderr = s.Out, s.Err
		return c.Run()
	}, nil
}

// verifyVolumeRoundTrip: write a random token into a throwaway volume from a container,
// back it up, wipe the volume, restore, and have a container read the token back.
func (s *Service) verifyVolumeRoundTrip(image string, keep bool) error {
	work, err := os.MkdirTemp("", "hsctl-verify-vol-")
	if err != nil {
		return err
	}
	if !keep {
		defer os.RemoveAll(work)
	}
	restic, err := s.newIsolatedRepo(work)
	if err != nil {
		return err
	}
	token := secret.Password(32)
	vol := "hsctl-verify-" + strings.ToLower(token[:8])
	if _, err := s.Docker.Output(s.Repo.Root, "volume", "create", vol); err != nil {
		return fmt.Errorf("create test volume: %w", err)
	}
	if !keep {
		defer func() { _, _ = s.Docker.Output(s.Repo.Root, "volume", "rm", "-f", vol) }()
	}
	fmt.Fprintf(s.Out, "  volume=%s image=%s\n", vol, image)
	// --entrypoint sh bypasses each image's own entrypoint, so any image works as a shell.
	if err := s.Docker.Run(s.Repo.Root, "run", "--rm", "--entrypoint", "sh", "-v", vol+":/d", image, "-c", "printf %s "+token+" > /d/proof.txt"); err != nil {
		return fmt.Errorf("write fixture via container (image %q ok?): %w", image, err)
	}
	mp, err := s.Docker.VolumeMountpoint(vol)
	if err != nil {
		return err
	}
	if err := restic("init"); err != nil {
		return err
	}
	if err := restic("backup", mp); err != nil {
		return err
	}
	if err := s.Docker.Run(s.Repo.Root, "run", "--rm", "--entrypoint", "sh", "-v", vol+":/d", image, "-c", "rm -f /d/proof.txt"); err != nil {
		return fmt.Errorf("wipe test volume: %w", err)
	}
	restoreDir := filepath.Join(work, "restore")
	if err := restic("restore", "latest", "--target", restoreDir); err != nil {
		return err
	}
	data, err := os.ReadFile(filepath.Join(restoreDir, mp, "proof.txt"))
	if err != nil {
		return fmt.Errorf("restored file missing: %w", err)
	}
	if err := os.WriteFile(filepath.Join(mp, "proof.txt"), data, 0644); err != nil {
		return fmt.Errorf("copy restored file back: %w", err)
	}
	read := s.Docker.Command(s.Repo.Root, "run", "--rm", "--entrypoint", "cat", "-v", vol+":/d", image, "/d/proof.txt")
	read.Stderr = s.Err
	out, err := read.Output()
	if err != nil {
		return fmt.Errorf("read back via container: %w", err)
	}
	if got := strings.TrimSpace(string(out)); got != token {
		return fmt.Errorf("restored token %q != original %q", got, token)
	}
	fmt.Fprintln(s.Out, "  wrote token -> backed up -> wiped -> restored -> container read it back")
	return nil
}

// verifyVaultwarden boots a throwaway Vaultwarden (the real image) against a fresh volume so it
// writes its actual SQLite DB, seeds a fake attachment, then exercises the SAME path `backup run`
// now uses: stop for a consistent DB, stage ONLY db.sqlite3, back up the volume LIVE excluding the
// DB, restore, overlay the staged DB, and prove the DB is byte-identical, the attachment survived
// (via the live-volume path), and a fresh Vaultwarden BOOTS from the reconstructed data and stays
// up. Mirrors stageVaultwardenDB + restoreIntoVolumes so the test can't pass on a path prod never runs.
func (s *Service) verifyVaultwarden(keep bool) error {
	image := s.Docker.ContainerImage("vaultwarden")
	if image == "" {
		return skipped("the vaultwarden container isn't there, so its image is unknown — start the stack (hsctl up) to include this check")
	}
	work, err := os.MkdirTemp("", "hsctl-verify-vw-")
	if err != nil {
		return err
	}
	if !keep {
		defer os.RemoveAll(work)
	}
	restic, err := s.newIsolatedRepo(work)
	if err != nil {
		return err
	}
	id := strings.ToLower(secret.Password(8))
	vol := "hsctl-verify-vw-" + id
	c1, c2 := "hsctl-verify-vw-a-"+id, "hsctl-verify-vw-b-"+id
	rm := func(c string) { _ = s.Docker.Command(s.Repo.Root, "rm", "-f", c).Run() } // quiet: ignore "already gone"
	if _, err := s.Docker.Output(s.Repo.Root, "volume", "create", vol); err != nil {
		return fmt.Errorf("create volume: %w", err)
	}
	if !keep {
		defer func() { _, _ = s.Docker.Output(s.Repo.Root, "volume", "rm", "-f", vol) }()
	}
	fmt.Fprintf(s.Out, "  image=%s volume=%s\n", image, vol)

	if _, err := s.Docker.Output(s.Repo.Root, "run", "-d", "--name", c1, "-v", vol+":/data", image); err != nil {
		return fmt.Errorf("start vaultwarden: %w", err)
	}
	if !keep {
		defer rm(c1)
	}
	mp, err := s.Docker.VolumeMountpoint(vol)
	if err != nil {
		return err
	}
	dbPath := filepath.Join(mp, "db.sqlite3")
	if !docker.Wait(60*time.Second, func() bool { return fsutil.Exists(dbPath) }) {
		return fmt.Errorf("vaultwarden did not create db.sqlite3 within 60s")
	}
	// Seed a fake attachment — it must survive via the LIVE volume backup (attachments aren't staged).
	attToken := secret.Password(24)
	attFile := filepath.Join(mp, "attachments", "proof.bin")
	if err := os.MkdirAll(filepath.Dir(attFile), 0700); err != nil {
		return err
	}
	if err := os.WriteFile(attFile, []byte(attToken), 0644); err != nil {
		return fmt.Errorf("seed attachment: %w", err)
	}
	if _, err := s.Docker.Output(s.Repo.Root, "stop", c1); err != nil { // clean shutdown -> consistent sqlite
		return fmt.Errorf("stop vaultwarden: %w", err)
	}
	h1, err := fsutil.SHA256File(dbPath)
	if err != nil {
		return err
	}

	// Mirror production (stageVaultwardenDB + restoreIntoVolumes): stage the DB FILESET, back up
	// the volume LIVE excluding the DB fileset plus the staged fileset, then restore = volume
	// files + fileset overlay.
	stageVault := filepath.Join(work, "staging", "vaultwarden")
	if err := os.MkdirAll(stageVault, 0700); err != nil {
		return err
	}
	for _, suf := range dbFilesetSuffixes {
		f := dbPath + suf
		if fsutil.Exists(f) {
			if err := fsutil.CopyFile(f, filepath.Join(stageVault, "db.sqlite3"+suf), s.Err); err != nil {
				return fmt.Errorf("stage db fileset: %w", err)
			}
		}
	}
	if err := restic("init"); err != nil {
		return err
	}
	if err := restic("backup", "--exclude", filepath.Join(mp, "db.sqlite3*"), mp, stageVault); err != nil {
		return err
	}
	if err := fsutil.WipeDirContents(mp); err != nil {
		return fmt.Errorf("wipe volume: %w", err)
	}
	restoreDir := filepath.Join(work, "restore")
	if err := restic("restore", "latest", "--target", restoreDir); err != nil {
		return err
	}
	// Reconstruct: volume files (attachments/keys, no DB) then overlay the staged fileset.
	if err := fsutil.CopyContents(filepath.Join(restoreDir, mp), mp, s.Err); err != nil {
		return fmt.Errorf("restore volume files: %w", err)
	}
	for _, suf := range dbFilesetSuffixes {
		f := filepath.Join(restoreDir, stageVault, "db.sqlite3"+suf)
		if fsutil.Exists(f) {
			if err := fsutil.CopyFile(f, dbPath+suf, s.Err); err != nil {
				return fmt.Errorf("overlay staged db fileset: %w", err)
			}
		}
	}
	h2, err := fsutil.SHA256File(dbPath)
	if err != nil {
		return fmt.Errorf("restored db.sqlite3 missing: %w", err)
	}
	if h1 != h2 {
		return fmt.Errorf("restored Vaultwarden DB differs from original (sha256 %s != %s)", h2, h1)
	}
	if got, err := os.ReadFile(attFile); err != nil || strings.TrimSpace(string(got)) != attToken {
		return fmt.Errorf("attachment did not survive the live-volume backup path")
	}
	fmt.Fprintln(s.Out, "  Vaultwarden DB restored byte-for-byte; attachment survived the live-volume path")

	if _, err := s.Docker.Output(s.Repo.Root, "run", "-d", "--name", c2, "-v", vol+":/data", image); err != nil {
		return fmt.Errorf("restart vaultwarden on restored data: %w", err)
	}
	if !keep {
		defer rm(c2)
	}
	if !docker.Wait(60*time.Second, func() bool {
		return s.Docker.ContainerState(c2) == "running" && fsutil.Exists(dbPath)
	}) {
		return fmt.Errorf("vaultwarden failed to boot from the restored volume")
	}
	time.Sleep(3 * time.Second) // ensure it doesn't crash-loop right after opening the DB
	if st := s.Docker.ContainerState(c2); st != "running" {
		return fmt.Errorf("vaultwarden exited after booting from restored data (state=%s)", st)
	}
	fmt.Fprintln(s.Out, "  fresh Vaultwarden booted from the restored volume and stayed up")
	return nil
}

// verifyVaultwardenWAL is the regression test for the data-loss bug where copying only
// db.sqlite3 (not the -wal) dropped writes still sitting in the WAL. It builds a WAL-mode DB
// with a row that stays UNCHECKPOINTED in db.sqlite3-wal (copying the files via `.shell` while
// the connection is still open, before any close-checkpoint), then proves: the main file ALONE
// loses the row, but the full fileset (db + -wal + -shm) preserves it. Needs sqlite3; skips if absent.
func (s *Service) verifyVaultwardenWAL(keep bool) error {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		return skipped("sqlite3 isn't installed (needed to build the WAL test database): sudo apt-get install -y sqlite3")
	}
	work, err := os.MkdirTemp("", "hsctl-verify-wal-")
	if err != nil {
		return err
	}
	if !keep {
		defer os.RemoveAll(work)
	}
	src, full, mainOnly := filepath.Join(work, "src"), filepath.Join(work, "full"), filepath.Join(work, "main")
	for _, d := range []string{src, full, mainOnly} {
		if err := os.MkdirAll(d, 0700); err != nil {
			return err
		}
	}
	db := filepath.Join(src, "db.sqlite3")
	// `.shell` runs mid-session (connection still open), so the row is still in -wal — exactly the
	// state docker stop can leave. We snapshot both a main-only copy and the full fileset there.
	script := fmt.Sprintf("PRAGMA journal_mode=WAL;\nPRAGMA wal_autocheckpoint=0;\n"+
		"CREATE TABLE m(v);\nINSERT INTO m VALUES('WAL-MARKER');\n"+
		".shell cp %s %s\n.shell cp %s %s %s %s/\n",
		db, filepath.Join(mainOnly, "db.sqlite3"),
		db, db+"-wal", db+"-shm", full)
	build := exec.Command("sqlite3", db)
	build.Stdin, build.Stderr = strings.NewReader(script), s.Err
	if err := build.Run(); err != nil {
		return fmt.Errorf("build WAL fixture (does sqlite3 allow .shell?): %w", err)
	}
	q := func(dir string) string {
		out, _ := exec.Command("sqlite3", filepath.Join(dir, "db.sqlite3"), "select v from m").Output()
		return strings.TrimSpace(string(out))
	}
	if q(mainOnly) == "WAL-MARKER" {
		return fmt.Errorf("fixture invalid: row was already checkpointed into the main file (not a WAL test)")
	}
	if got := q(full); got != "WAL-MARKER" {
		return fmt.Errorf("WAL row LOST by the fileset copy (got %q) — db.sqlite3-wal not preserved", got)
	}
	fmt.Fprintln(s.Out, "  uncheckpointed WAL row: lost by main-only copy, PRESERVED by the db+wal+shm fileset")
	return nil
}

// verifyNextcloudDB tests the Nextcloud DB path: seed a row in a throwaway Postgres (the
// real image), dump it with the SAME pg_dump the backup uses, store the dump via restic,
// destroy the DB, then restore the dump into a brand-new Postgres and check the row is back.
func (s *Service) verifyNextcloudDB(keep bool) error {
	image := s.Docker.ContainerImage("nextcloud-db")
	if image == "" {
		return skipped("the nextcloud-db container isn't there, so its Postgres image is unknown — start the stack (hsctl up) to include this check")
	}
	work, err := os.MkdirTemp("", "hsctl-verify-db-")
	if err != nil {
		return err
	}
	if !keep {
		defer os.RemoveAll(work)
	}
	restic, err := s.newIsolatedRepo(work)
	if err != nil {
		return err
	}
	id := strings.ToLower(secret.Password(8))
	c1, c2 := "hsctl-verify-db-a-"+id, "hsctl-verify-db-b-"+id
	token := secret.Password(24)
	rm := func(c string) { _ = s.Docker.Command(s.Repo.Root, "rm", "-f", c).Run() } // quiet: ignore "already gone"
	env := []string{"-e", "POSTGRES_USER=nextcloud", "-e", "POSTGRES_PASSWORD=" + secret.Password(16), "-e", "POSTGRES_DB=nextcloud"}
	start := func(name string) error {
		_, err := s.Docker.Output(s.Repo.Root, append(append([]string{"run", "-d", "--name", name}, env...), image)...)
		return err
	}
	ready := func(name string) bool {
		// NOT pg_isready: during first init Postgres runs a temporary server that pg_isready
		// reports as ready before the real `nextcloud` DB exists. Gate on a real query.
		// dockerCmd (not dockerOut) so the expected "not ready yet" errors stay quiet.
		out, err := s.Docker.Command(s.Repo.Root, "exec", name, "psql", "-U", "nextcloud", "-d", "nextcloud", "-tAc", "select 1").Output()
		return err == nil && strings.TrimSpace(string(out)) == "1"
	}
	psql := func(name, sql string) (string, error) {
		return s.Docker.Output(s.Repo.Root, "exec", name, "psql", "-U", "nextcloud", "-d", "nextcloud", "-tAc", sql)
	}
	fmt.Fprintf(s.Out, "  image=%s\n", image)

	if err := start(c1); err != nil {
		return fmt.Errorf("start postgres: %w", err)
	}
	if !keep {
		defer rm(c1)
	}
	if !docker.Wait(60*time.Second, func() bool { return ready(c1) }) {
		return fmt.Errorf("postgres did not become ready within 60s")
	}
	if _, err := psql(c1, "create table verify_marker(token text); insert into verify_marker values('"+token+"');"); err != nil {
		return fmt.Errorf("seed test data: %w", err)
	}

	// Dump with the SAME command the real backup uses, then store the dump through restic.
	staging := filepath.Join(work, "staging")
	if err := os.MkdirAll(staging, 0700); err != nil {
		return err
	}
	dumpPath := filepath.Join(staging, "nextcloud-db.sql")
	dumpF, err := os.Create(dumpPath)
	if err != nil {
		return err
	}
	dump := s.Docker.Command(s.Repo.Root, "exec", c1, "pg_dump", "-U", "nextcloud", "nextcloud")
	dump.Stdout, dump.Stderr = dumpF, s.Err
	derr := dump.Run()
	dumpF.Close()
	if derr != nil {
		return fmt.Errorf("pg_dump failed: %w", derr)
	}
	if err := restic("init"); err != nil {
		return err
	}
	if err := restic("backup", staging); err != nil {
		return err
	}

	rm(c1) // total loss: destroy the original DB entirely

	restoreDir := filepath.Join(work, "restore")
	if err := restic("restore", "latest", "--target", restoreDir); err != nil {
		return err
	}
	restoredDump := filepath.Join(restoreDir, dumpPath)
	if !fsutil.Exists(restoredDump) {
		return fmt.Errorf("restored dump missing at %s", restoredDump)
	}
	if err := start(c2); err != nil {
		return fmt.Errorf("start fresh postgres: %w", err)
	}
	if !keep {
		defer rm(c2)
	}
	if !docker.Wait(60*time.Second, func() bool { return ready(c2) }) {
		return fmt.Errorf("fresh postgres did not become ready within 60s")
	}
	f, err := os.Open(restoredDump)
	if err != nil {
		return err
	}
	defer f.Close()
	imp := s.Docker.Command(s.Repo.Root, "exec", "-i", c2, "psql", "-U", "nextcloud", "-d", "nextcloud")
	imp.Stdin, imp.Stderr = f, s.Err
	if err := imp.Run(); err != nil {
		return fmt.Errorf("import dump into fresh postgres: %w", err)
	}
	got, err := psql(c2, "select token from verify_marker")
	if err != nil {
		return fmt.Errorf("query restored data: %w", err)
	}
	if strings.TrimSpace(got) != token {
		return fmt.Errorf("restored DB token %q != original %q", strings.TrimSpace(got), token)
	}
	fmt.Fprintln(s.Out, "  seeded row -> pg_dump -> restic -> restore -> imported into fresh DB -> row matches")
	return nil
}
