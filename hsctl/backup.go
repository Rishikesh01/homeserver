package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// Backups use restic: encrypted, deduplicated, snapshotted, many backends (local
// path, USB, SFTP to another host, S3/Backblaze B2). What we protect:
//   - a fresh Postgres dump (consistent DB) + every data volume's files
//   - the per-service .env + setup.conf (so a restore can rebuild the stack)
// Volume files live under /var/lib/docker/volumes, so `backup run` needs root
// (run via sudo, or from the root-owned systemd timer).

// backupVolumeSkip: volumes we deliberately DON'T back up — pure caches that a
// service rebuilds on its own at restart. Nothing is lost by skipping them, and
// they're churny/large. Everything else owned by one of our services is included.
var backupVolumeSkip = map[string]bool{
	"nextcloud_redis-data": true, // Nextcloud's Redis cache + file locks; rebuilt on start
}

// backupVolumesFor returns every Docker volume that belongs to one of our services
// (a volume is named "<compose-project>_<name>", and the project is the service's
// dir name), minus the skip-list above. Discovering volumes instead of hardcoding
// them means a newly added service's data is protected automatically — no list to
// keep in sync, so we can't silently miss a service again.
func backupVolumesFor(repo string) []string {
	out, err := dockerOut(repo, "volume", "ls", "--format", "{{.Name}}")
	if err != nil {
		fmt.Fprintln(os.Stderr, "warning: could not list docker volumes:", err)
		return nil
	}
	ours := map[string]bool{}
	for _, s := range services {
		ours[s] = true
	}
	var vols []string
	for _, name := range strings.Split(out, "\n") {
		name = strings.TrimSpace(name)
		if name == "" || backupVolumeSkip[name] {
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
	return vols
}

const (
	backupConfFile = "backup.conf"
	resticPassFile = ".restic-password"
	defaultRetention = "--keep-daily 7 --keep-weekly 4 --keep-monthly 6"
)

type backupCfg struct {
	Repo          string // restic repository (e.g. /mnt/usb/restic, sftp:user@host:/path, b2:bucket:path)
	Retention     string
	ResticVersion string // pinned restic version; `backup verify` fails if the installed one differs
}

// backupRepoDir resolves the homeserver repo and refuses to proceed if we're not
// actually in it. Without this, repoDir() silently falls back to the cwd, so a
// `hsctl backup run` from the wrong directory would generate a NEW .restic-password
// and a phantom repo under <cwd>/backups — making you think you're backed up when
// the snapshots (and their key) are scattered somewhere unexpected.
func backupRepoDir() (string, error) {
	repo := repoDir()
	if !isRepo(repo) {
		return "", fmt.Errorf("not in the homeserver repo (resolved to %s)\n"+
			"run backups from the repo directory, or set HOMESERVER_DIR:\n"+
			"  cd /path/to/homeserver && sudo hsctl backup run\n"+
			"  # or: sudo HOMESERVER_DIR=/path/to/homeserver hsctl backup run", repo)
	}
	return repo, nil
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
		c.ResticVersion = kv["RESTIC_VERSION"] // empty = no version pinned yet
	}
	return c
}

func (c backupCfg) save(repo string) error {
	s := fmt.Sprintf(
		"# hsctl backup config. RESTIC_REPO: local path, sftp:user@host:/path, b2:bucket:path, s3:...\n"+
			"RESTIC_REPO=%s\nRETENTION=%s\n", c.Repo, c.Retention)
	if c.ResticVersion != "" {
		s += "# Pinned restic version. `backup verify` FAILS if the installed restic differs, so a\n" +
			"# system upgrade that swaps restic out is caught. Re-baseline: backup config --pin-restic\n" +
			"RESTIC_VERSION=" + c.ResticVersion + "\n"
	}
	return writeFile0600(filepath.Join(repo, backupConfFile), s)
}

// backupCmd builds the `hsctl backup` command tree.
func backupCmd() *cobra.Command {
	b := &cobra.Command{Use: "backup", Short: "Encrypted backups (restic)"}

	cfgCmd := &cobra.Command{Use: "config", Short: "Set the backup destination / retention / password",
		Args: cobra.NoArgs, RunE: runBackupConfig}
	cfgCmd.Flags().String("repo", "", "restic repository (local path / sftp: / b2: / s3:)")
	cfgCmd.Flags().String("retention", "", "restic forget policy")
	cfgCmd.Flags().String("password", "", "set the restic repo password (stored in .restic-password)")
	cfgCmd.Flags().Bool("pin-restic", false, "record the installed restic version as the pinned baseline")

	// withRestic wraps a run that needs restic + a loaded config.
	withRestic := func(run func(repo string, cfg backupCfg) error) func(*cobra.Command, []string) error {
		return func(*cobra.Command, []string) error {
			if err := requireRestic(); err != nil {
				return err
			}
			repo, err := backupRepoDir()
			if err != nil {
				return err
			}
			return run(repo, loadBackupCfg(repo))
		}
	}

	initCmd := &cobra.Command{Use: "init", Short: "Create the encrypted restic repo (first time)", Args: cobra.NoArgs,
		RunE: withRestic(func(repo string, cfg backupCfg) error {
			ensureResticPassword(repo)
			// Record the restic version we're initialising with, so `backup verify` can
			// later catch a restic that drifted (e.g. after a system upgrade).
			if cfg.ResticVersion == "" {
				if v, err := resticVersion(); err == nil {
					cfg.ResticVersion = v
					_ = cfg.save(repo)
					fmt.Printf("pinned restic version baseline: %s\n", v)
				}
			}
			return resticRun(repo, cfg, "init")
		})}
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

	verifyCmd := &cobra.Command{Use: "verify", Aliases: []string{"selftest", "test"},
		Short: "Self-test backup+restore on a throwaway Docker volume (never touches live data)",
		Args:  cobra.NoArgs, RunE: runBackupVerify}
	verifyCmd.Flags().String("image", "", "container image for the test fixture (default: a local image)")
	verifyCmd.Flags().Bool("keep", false, "keep the test volume + temp repo afterwards (for debugging)")

	b.AddCommand(cfgCmd, initCmd, runCmd, listCmd, restoreCmd, verifyCmd, forgetCmd)
	return b
}

func runBackupConfig(cmd *cobra.Command, _ []string) error {
	repo, err := backupRepoDir()
	if err != nil {
		return err
	}
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
	if pin, _ := f.GetBool("pin-restic"); pin {
		v, err := resticVersion()
		if err != nil {
			return fmt.Errorf("--pin-restic: %w", err)
		}
		cfg.ResticVersion = v
		fmt.Printf("pinned restic version: %s\n", v)
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
	if cur, err := resticVersion(); err == nil && cfg.ResticVersion != "" && cur != cfg.ResticVersion {
		fmt.Fprintf(os.Stderr, "warning: restic %s differs from pinned %s — the `apt-mark hold restic` "+
			"pin may have been overridden by a system upgrade. Run `hsctl backup verify` to re-test.\n",
			cur, cfg.ResticVersion)
	}
	staging := filepath.Join(repo, "backups", "staging")
	if err := os.MkdirAll(staging, 0700); err != nil {
		return err
	}
	dumpNextcloudDB(repo, staging) // best-effort; warns inside

	vols := backupVolumesFor(repo)
	fmt.Printf("volumes to back up (%d): %s\n", len(vols), strings.Join(vols, " "))
	var paths []string
	for _, v := range vols {
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
	repo, err := backupRepoDir()
	if err != nil {
		return err
	}
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
	fmt.Printf("  2. copy each volume's files back, e.g.  cp -a %s/var/lib/docker/volumes/<name>/_data/. \\\n", target)
	fmt.Println("       /var/lib/docker/volumes/<name>/_data/   (this restores the Postgres DB volume too)")
	fmt.Println("  3. hsctl up")
	fmt.Println("  A consistent SQL dump is also in backups/staging/nextcloud-db.sql — only needed as a")
	fmt.Println("  fallback (import into a fresh nextcloud-db) if the restored DB volume won't start.")
	return nil
}

// runBackupVerify is a self-contained backup+restore test. It NEVER touches the live
// stack or the real repo: it spins up a throwaway Docker volume, writes a random token
// into it from a container, backs that volume up into a temporary isolated restic repo,
// wipes the volume, restores from the repo, and has a container read the token back —
// asserting it matches. Exercises the real restic backup/restore path end to end, and is
// safe to run anytime (e.g. after upgrades) to confirm backups actually work.
//
// Needs root for the same reason `backup run` does: restic reads the volume's files under
// /var/lib/docker/volumes (root-only).
func runBackupVerify(cmd *cobra.Command, _ []string) error {
	if err := requireRestic(); err != nil {
		return err
	}
	repo, err := backupRepoDir()
	if err != nil {
		return err
	}
	image, _ := cmd.Flags().GetString("image")
	if image == "" {
		// Default to any image already on the box, so we never need to pull. We override
		// its entrypoint below, so even app images (postgres, redis, …) work as plain sh.
		if img := firstLocalImage(repo); img != "" {
			image = img
		} else {
			image = "busybox"
		}
	}
	keep, _ := cmd.Flags().GetBool("keep")
	cfg := loadBackupCfg(repo)

	// --- restic version pin: catch a restic that drifted from the pinned one ----------
	cur, err := resticVersion()
	if err != nil {
		return err
	}
	fmt.Printf("verify: installed restic %s\n", cur)
	switch {
	case cfg.ResticVersion == "":
		fmt.Println("verify: WARNING — no pinned restic version recorded yet.")
		fmt.Println("        Lock today's in with:  sudo hsctl backup config --pin-restic")
		fmt.Println("        (so a later system upgrade that swaps restic out is caught here.)")
	case cur != cfg.ResticVersion:
		return fmt.Errorf("restic version drift: installed %s but backup.conf pins %s.\n"+
			"a system upgrade likely changed restic — re-pin it (sudo apt-mark hold restic) and\n"+
			"reinstall %s; or, if intentional and re-tested: sudo hsctl backup config --pin-restic",
			cur, cfg.ResticVersion, cfg.ResticVersion)
	default:
		fmt.Printf("verify: matches the pinned version (%s)\n", cfg.ResticVersion)
	}

	// --- service-shaped sub-tests, each fully isolated (own throwaway volume/containers +
	//     temp repo). The important ones are Vaultwarden (your passwords) and the Nextcloud
	//     Postgres DB (the pg_dump path that the -T bug used to break).
	tests := []struct {
		name string
		run  func() error
	}{
		{"restic volume round-trip", func() error { return verifyVolumeRoundTrip(repo, image, keep) }},
		{"Vaultwarden — passwords (boots from a restored volume)", func() error { return verifyVaultwarden(repo, keep) }},
		{"Nextcloud — database (pg_dump -> restic -> import)", func() error { return verifyNextcloudDB(repo, keep) }},
	}
	failed := 0
	for _, t := range tests {
		fmt.Printf("\n========== %s ==========\n", t.name)
		if err := t.run(); err != nil {
			fmt.Printf("FAIL: %s\n  %v\n", t.name, err)
			failed++
		} else {
			fmt.Printf("PASS: %s\n", t.name)
		}
	}
	fmt.Printf("\n========== verify: %d/%d passed ==========\n", len(tests)-failed, len(tests))
	if failed > 0 {
		return fmt.Errorf("%d backup self-test(s) FAILED — see above", failed)
	}
	fmt.Println("Backups verified end-to-end: restic round-trips data, Vaultwarden boots from a")
	fmt.Println("restored volume with its DB intact, and the Nextcloud DB dump restores cleanly.")
	return nil
}

// newIsolatedRepo sets up a fresh restic repo under work with its own password and returns
// a runner bound to it — so each sub-test stays isolated from the real backup repo.
func newIsolatedRepo(work string) (func(args ...string) error, error) {
	repoPath := filepath.Join(work, "repo")
	passFile := filepath.Join(work, "pass")
	if err := writeFile0600(passFile, genPassword(24)+"\n"); err != nil {
		return nil, err
	}
	return func(args ...string) error {
		c := exec.Command("restic", args...)
		c.Env = resticEnv(repoPath, passFile)
		c.Stdout, c.Stderr = os.Stdout, os.Stderr
		return c.Run()
	}, nil
}

// verifyVolumeRoundTrip: write a random token into a throwaway volume from a container,
// back it up, wipe the volume, restore, and have a container read the token back.
func verifyVolumeRoundTrip(repo, image string, keep bool) error {
	work, err := os.MkdirTemp("", "hsctl-verify-vol-")
	if err != nil {
		return err
	}
	if !keep {
		defer os.RemoveAll(work)
	}
	restic, err := newIsolatedRepo(work)
	if err != nil {
		return err
	}
	token := genPassword(32)
	vol := "hsctl-verify-" + strings.ToLower(token[:8])
	if _, err := dockerOut(repo, "volume", "create", vol); err != nil {
		return fmt.Errorf("create test volume: %w", err)
	}
	if !keep {
		defer func() { _, _ = dockerOut(repo, "volume", "rm", "-f", vol) }()
	}
	fmt.Printf("  volume=%s image=%s\n", vol, image)
	// --entrypoint sh bypasses each image's own entrypoint, so any image works as a shell.
	if err := dockerRun(repo, "run", "--rm", "--entrypoint", "sh", "-v", vol+":/d", image, "-c",
		"printf %s "+token+" > /d/proof.txt"); err != nil {
		return fmt.Errorf("write fixture via container (image %q ok?): %w", image, err)
	}
	mp, err := dockerOut(repo, "volume", "inspect", "-f", "{{.Mountpoint}}", vol)
	if err != nil || mp == "" {
		return fmt.Errorf("inspect test volume: %w", err)
	}
	if err := restic("init"); err != nil {
		return err
	}
	if err := restic("backup", mp); err != nil {
		return err
	}
	if err := dockerRun(repo, "run", "--rm", "--entrypoint", "sh", "-v", vol+":/d", image, "-c", "rm -f /d/proof.txt"); err != nil {
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
	read := dockerCmd(repo, "run", "--rm", "--entrypoint", "cat", "-v", vol+":/d", image, "/d/proof.txt")
	read.Stderr = os.Stderr
	out, err := read.Output()
	if err != nil {
		return fmt.Errorf("read back via container: %w", err)
	}
	if got := strings.TrimSpace(string(out)); got != token {
		return fmt.Errorf("restored token %q != original %q", got, token)
	}
	fmt.Println("  wrote token -> backed up -> wiped -> restored -> container read it back")
	return nil
}

// verifyVaultwarden boots a throwaway Vaultwarden (the real image) against a fresh volume so
// it writes its actual SQLite DB, backs the volume up, wipes it, restores, checks the DB is
// byte-identical, then proves a fresh Vaultwarden BOOTS from the restored volume and stays up.
func verifyVaultwarden(repo string, keep bool) error {
	image := containerImage(repo, "vaultwarden")
	if image == "" {
		fmt.Println("  SKIP: vaultwarden container not found (can't determine its image)")
		return nil
	}
	work, err := os.MkdirTemp("", "hsctl-verify-vw-")
	if err != nil {
		return err
	}
	if !keep {
		defer os.RemoveAll(work)
	}
	restic, err := newIsolatedRepo(work)
	if err != nil {
		return err
	}
	id := strings.ToLower(genPassword(8))
	vol := "hsctl-verify-vw-" + id
	c1, c2 := "hsctl-verify-vw-a-"+id, "hsctl-verify-vw-b-"+id
	rm := func(c string) { _ = dockerCmd(repo, "rm", "-f", c).Run() } // quiet: ignore "already gone"
	if _, err := dockerOut(repo, "volume", "create", vol); err != nil {
		return fmt.Errorf("create volume: %w", err)
	}
	if !keep {
		defer func() { _, _ = dockerOut(repo, "volume", "rm", "-f", vol) }()
	}
	fmt.Printf("  image=%s volume=%s\n", image, vol)

	if _, err := dockerOut(repo, "run", "-d", "--name", c1, "-v", vol+":/data", image); err != nil {
		return fmt.Errorf("start vaultwarden: %w", err)
	}
	if !keep {
		defer rm(c1)
	}
	mp, err := dockerOut(repo, "volume", "inspect", "-f", "{{.Mountpoint}}", vol)
	if err != nil {
		return err
	}
	dbPath := filepath.Join(mp, "db.sqlite3")
	if !dockerWait(60*time.Second, func() bool { return fileExists(dbPath) }) {
		return fmt.Errorf("vaultwarden did not create db.sqlite3 within 60s")
	}
	if _, err := dockerOut(repo, "stop", c1); err != nil { // clean shutdown -> consistent sqlite
		return fmt.Errorf("stop vaultwarden: %w", err)
	}
	h1, err := sha256File(dbPath)
	if err != nil {
		return err
	}

	if err := restic("init"); err != nil {
		return err
	}
	if err := restic("backup", mp); err != nil {
		return err
	}
	if err := wipeDirContents(mp); err != nil {
		return fmt.Errorf("wipe volume: %w", err)
	}
	restoreDir := filepath.Join(work, "restore")
	if err := restic("restore", "latest", "--target", restoreDir); err != nil {
		return err
	}
	if err := copyContents(filepath.Join(restoreDir, mp), mp); err != nil {
		return fmt.Errorf("copy restored files back: %w", err)
	}
	h2, err := sha256File(dbPath)
	if err != nil {
		return fmt.Errorf("restored db.sqlite3 missing: %w", err)
	}
	if h1 != h2 {
		return fmt.Errorf("restored Vaultwarden DB differs from original (sha256 %s != %s)", h2, h1)
	}
	fmt.Println("  Vaultwarden DB restored byte-for-byte")

	if _, err := dockerOut(repo, "run", "-d", "--name", c2, "-v", vol+":/data", image); err != nil {
		return fmt.Errorf("restart vaultwarden on restored data: %w", err)
	}
	if !keep {
		defer rm(c2)
	}
	if !dockerWait(60*time.Second, func() bool {
		return containerState(repo, c2) == "running" && fileExists(dbPath)
	}) {
		return fmt.Errorf("vaultwarden failed to boot from the restored volume")
	}
	time.Sleep(3 * time.Second) // ensure it doesn't crash-loop right after opening the DB
	if st := containerState(repo, c2); st != "running" {
		return fmt.Errorf("vaultwarden exited after booting from restored data (state=%s)", st)
	}
	fmt.Println("  fresh Vaultwarden booted from the restored volume and stayed up")
	return nil
}

// verifyNextcloudDB tests the Nextcloud DB path: seed a row in a throwaway Postgres (the
// real image), dump it with the SAME pg_dump the backup uses, store the dump via restic,
// destroy the DB, then restore the dump into a brand-new Postgres and check the row is back.
func verifyNextcloudDB(repo string, keep bool) error {
	image := containerImage(repo, "nextcloud-db")
	if image == "" {
		fmt.Println("  SKIP: nextcloud-db container not found (can't determine its Postgres image)")
		return nil
	}
	work, err := os.MkdirTemp("", "hsctl-verify-db-")
	if err != nil {
		return err
	}
	if !keep {
		defer os.RemoveAll(work)
	}
	restic, err := newIsolatedRepo(work)
	if err != nil {
		return err
	}
	id := strings.ToLower(genPassword(8))
	c1, c2 := "hsctl-verify-db-a-"+id, "hsctl-verify-db-b-"+id
	token := genPassword(24)
	rm := func(c string) { _ = dockerCmd(repo, "rm", "-f", c).Run() } // quiet: ignore "already gone"
	env := []string{"-e", "POSTGRES_USER=nextcloud", "-e", "POSTGRES_PASSWORD=" + genPassword(16), "-e", "POSTGRES_DB=nextcloud"}
	start := func(name string) error {
		_, err := dockerOut(repo, append(append([]string{"run", "-d", "--name", name}, env...), image)...)
		return err
	}
	ready := func(name string) bool {
		// NOT pg_isready: during first init Postgres runs a temporary server that pg_isready
		// reports as ready before the real `nextcloud` DB exists. Gate on a real query.
		// dockerCmd (not dockerOut) so the expected "not ready yet" errors stay quiet.
		out, err := dockerCmd(repo, "exec", name, "psql", "-U", "nextcloud", "-d", "nextcloud", "-tAc", "select 1").Output()
		return err == nil && strings.TrimSpace(string(out)) == "1"
	}
	psql := func(name, sql string) (string, error) {
		return dockerOut(repo, "exec", name, "psql", "-U", "nextcloud", "-d", "nextcloud", "-tAc", sql)
	}
	fmt.Printf("  image=%s\n", image)

	if err := start(c1); err != nil {
		return fmt.Errorf("start postgres: %w", err)
	}
	if !keep {
		defer rm(c1)
	}
	if !dockerWait(60*time.Second, func() bool { return ready(c1) }) {
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
	dump := dockerCmd(repo, "exec", c1, "pg_dump", "-U", "nextcloud", "nextcloud")
	dump.Stdout, dump.Stderr = dumpF, os.Stderr
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
	if !fileExists(restoredDump) {
		return fmt.Errorf("restored dump missing at %s", restoredDump)
	}
	if err := start(c2); err != nil {
		return fmt.Errorf("start fresh postgres: %w", err)
	}
	if !keep {
		defer rm(c2)
	}
	if !dockerWait(60*time.Second, func() bool { return ready(c2) }) {
		return fmt.Errorf("fresh postgres did not become ready within 60s")
	}
	f, err := os.Open(restoredDump)
	if err != nil {
		return err
	}
	defer f.Close()
	imp := dockerCmd(repo, "exec", "-i", c2, "psql", "-U", "nextcloud", "-d", "nextcloud")
	imp.Stdin, imp.Stderr = f, os.Stderr
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
	fmt.Println("  seeded row -> pg_dump -> restic -> restore -> imported into fresh DB -> row matches")
	return nil
}

// dockerWait polls check() once a second until it's true or the timeout elapses.
func dockerWait(timeout time.Duration, check func() bool) bool {
	deadline := time.Now().Add(timeout)
	for {
		if check() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(time.Second)
	}
}

// sha256File returns the hex SHA-256 of a file's contents.
func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// wipeDirContents removes everything inside dir but keeps dir itself (it's a mountpoint).
func wipeDirContents(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

// copyContents copies the contents of src into dst, preserving ownership/permissions.
func copyContents(src, dst string) error {
	c := exec.Command("cp", "-a", src+"/.", dst+"/")
	c.Stderr = os.Stderr
	return c.Run()
}

// dumpNextcloudDB writes a consistent SQL dump (preferred over the raw volume on restore).
func dumpNextcloudDB(repo, staging string) {
	f, err := os.Create(filepath.Join(staging, "nextcloud-db.sql"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "db dump: cannot create file:", err)
		return
	}
	defer f.Close()
	// No -T: that's docker-compose syntax; plain `docker exec` rejects it. We don't
	// need a TTY here — pg_dump's stdout is redirected to the dump file below.
	c := dockerCmd(repo, "exec", "nextcloud-db", "pg_dump", "-U", "nextcloud", "nextcloud")
	c.Stdout, c.Stderr = f, os.Stderr
	if err := c.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "db dump: skipped (is nextcloud-db up?):", err)
	}
}

// resticEnv builds the environment to point restic at a given repository + password file.
func resticEnv(repository, passFile string) []string {
	return append(os.Environ(),
		"RESTIC_REPOSITORY="+repository,
		"RESTIC_PASSWORD_FILE="+passFile)
}

func resticRun(repo string, cfg backupCfg, args ...string) error {
	c := exec.Command("restic", args...)
	c.Env = resticEnv(cfg.Repo, filepath.Join(repo, resticPassFile))
	c.Stdout, c.Stderr, c.Stdin = os.Stdout, os.Stderr, os.Stdin
	return c.Run()
}

func resticInstalled() bool { _, err := exec.LookPath("restic"); return err == nil }

// resticOutput captures combined output (for the UI to display snapshots).
func resticOutput(repo string, cfg backupCfg, args ...string) (string, error) {
	c := exec.Command("restic", args...)
	c.Env = resticEnv(cfg.Repo, filepath.Join(repo, resticPassFile))
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

// resticVersion returns the installed restic version, e.g. "0.16.4", parsed from
// `restic version` ("restic 0.16.4 compiled with go1.22.2 on linux/amd64").
func resticVersion() (string, error) {
	out, err := exec.Command("restic", "version").Output()
	if err != nil {
		return "", fmt.Errorf("running `restic version`: %w", err)
	}
	if f := strings.Fields(string(out)); len(f) >= 2 && f[0] == "restic" {
		return f[1], nil
	}
	return "", fmt.Errorf("could not parse restic version from %q", strings.TrimSpace(string(out)))
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
