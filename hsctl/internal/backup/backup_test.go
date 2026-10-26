//go:build integration

package backup_test

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Rishikesh01/homeserver/hsctl/internal/backup"
	"github.com/Rishikesh01/homeserver/hsctl/internal/config"
	"github.com/Rishikesh01/homeserver/hsctl/internal/docker"
	"github.com/Rishikesh01/homeserver/hsctl/internal/itest"
	"github.com/Rishikesh01/homeserver/hsctl/internal/layout"
	"github.com/Rishikesh01/homeserver/hsctl/internal/paths"
	"github.com/Rishikesh01/homeserver/hsctl/internal/prompt"
	"github.com/Rishikesh01/homeserver/hsctl/internal/provision"
	"github.com/Rishikesh01/homeserver/hsctl/internal/restic"
	"github.com/Rishikesh01/homeserver/hsctl/internal/stack"
)

// fixture is the real stack — its own compose files and images, the Nextcloud app swapped for a
// stand-in (see itest.LightNextcloud) — running in a fresh checkout, with a backup service on it.
type fixture struct {
	t        *testing.T
	repo     paths.Repo
	svc      *backup.Service
	out      *bytes.Buffer
	dk       *docker.Client
	timerOn  bool
	ncUser   string
	ncDB     string
	vwVolume string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	itest.NeedRoot(t)
	itest.NeedDocker(t)
	itest.NeedRestic(t)
	repo := itest.Checkout(t)
	itest.LightNextcloud(t, repo)
	f := &fixture{t: t, repo: repo, out: &bytes.Buffer{}, ncUser: "nextcloud", ncDB: "nextcloud"}
	f.dk = docker.New(repo.Root)
	f.dk.Stdout, f.dk.Stderr = f.out, f.out
	store := config.NewStore(repo, config.LocalHost{})
	c := store.Loaded()
	c.DisabledApps = []string{"pihole", "stirling", "it-tools", "imagetools"}
	if err := store.Save(c); err != nil {
		t.Fatal(err)
	}
	prov := provision.New(repo, f.dk)
	prov.Out, prov.Err = f.out, f.out
	if _, err := prov.Generate(c, false); err != nil {
		t.Fatal(err)
	}
	st := stack.New(repo, f.dk, store, prov)
	st.Out, st.Err = f.out, f.out
	rs := restic.New(repo.Path(paths.ResticPassword), backup.CredentialsEnv(repo))
	rs.Stdout, rs.Stderr = f.out, f.out
	f.svc = backup.New(repo, f.dk, rs, st, &prompt.Terminal{Out: f.out, Interactive: func() bool { return false }})
	f.svc.Out, f.svc.Err = f.out, f.out
	f.svc.EnableTimer = func() { f.timerOn = true }
	if err := st.Up(); err != nil {
		t.Fatalf("up: %v\n%s", err, f.out.String())
	}
	f.waitReady()
	return f
}

// waitReady waits for Vaultwarden's database and a Postgres that answers queries.
func (f *fixture) waitReady() {
	f.t.Helper()
	ok := docker.Wait(2*time.Minute, func() bool {
		return f.dk.ContainerState(layout.Vaultwarden) == "running" && fileExists(f.volume(layout.VaultwardenVolume, "db.sqlite3")) &&
			f.dk.Quiet("", "exec", layout.NextcloudDB, "psql", "-U", f.ncUser, "-d", f.ncDB, "-tAc", "select 1") == nil
	})
	if !ok {
		f.t.Fatalf("the stack didn't come up:\n%s", f.out.String())
	}
}

// volume is a path inside a Docker volume.
func (f *fixture) volume(name string, elem ...string) string {
	mp, err := f.dk.VolumeMountpoint(name)
	if err != nil {
		f.t.Fatal(err)
	}
	return filepath.Join(append([]string{mp}, elem...)...)
}

// sqlite runs SQL on Vaultwarden's live database, the way another writer would.
func (f *fixture) sqlite(sql string) string {
	f.t.Helper()
	out, err := exec.Command("sqlite3", f.volume(layout.VaultwardenVolume, "db.sqlite3"), sql).CombinedOutput()
	if err != nil {
		f.t.Fatalf("sqlite3 %q: %v\n%s", sql, err, out)
	}
	return strings.TrimSpace(string(out))
}

// psql runs SQL on Nextcloud's database.
func (f *fixture) psql(sql string) string {
	f.t.Helper()
	out, err := f.dk.Combined("", "exec", layout.NextcloudDB, "psql", "-U", f.ncUser, "-d", f.ncDB, "-tAc", sql)
	if err != nil {
		f.t.Fatalf("psql %q: %v\n%s", sql, err, out)
	}
	return strings.TrimSpace(out)
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

// markers reads every piece of data the test puts in the stack.
func (f *fixture) markers() string {
	return strings.Join([]string{
		"vw-db=" + f.sqlite("SELECT group_concat(v) FROM itest"),
		"vw-file=" + itest.ReadFile(f.volume(layout.VaultwardenVolume, "attachments", "itest.txt")),
		"nc-db=" + f.psql("SELECT string_agg(v, ',') FROM itest"),
		"nc-file=" + itest.ReadFile(f.volume("nextcloud_nc-data", "itest.txt")),
		fmt.Sprintf("nc-extra=%v", fileExists(f.volume("nextcloud_nc-data", "extra.txt"))),
	}, " ")
}

// TestBackupAndRestore backs a real, running stack up, changes all of its data, and restores it into
// the volumes: every database and file comes back as it was — Vaultwarden's SQLite (written live, in
// WAL mode), Nextcloud's Postgres (rebuilt from its dump), plain volume files — and what was added
// since is gone. Along the way: the repository check and prune, the off-site replica, an extraction
// that leaves the stack alone, the self-test and a password change. The steps run in order.
func TestBackupAndRestore(t *testing.T) {
	f := newFixture(t)
	s := f.svc
	dest := filepath.Join(t.TempDir(), "restic")
	replica := filepath.Join(t.TempDir(), "replica")
	extract := filepath.Join(t.TempDir(), "extract")
	const before = "vw-db=before vw-file=before nc-db=before nc-file=before nc-extra=false"
	steps := []struct {
		name    string
		run     func() error
		wantOut []string
		check   func() error
	}{
		{"seed the data", func() error {
			f.sqlite("CREATE TABLE itest(v TEXT); INSERT INTO itest VALUES('before');")
			itest.WriteFile(t, f.volume(layout.VaultwardenVolume), "attachments/itest.txt", "before")
			f.psql("CREATE TABLE itest(v text); INSERT INTO itest VALUES ('before')")
			itest.WriteFile(t, f.volume("nextcloud_nc-data"), "itest.txt", "before")
			return nil
		}, nil, func() error {
			if got := f.markers(); got != before {
				return fmt.Errorf("markers: %s", got)
			}
			return nil
		}},
		{"choose a destination", func() error { return s.Configure(backup.Options{Repo: &dest}) },
			[]string{"saved backup.conf (repo=" + dest + ")"}, nil},
		{"create the repository", s.Init, []string{"generated the backup password", "created restic repository"}, func() error {
			if !f.timerOn {
				return fmt.Errorf("the nightly timer wasn't switched on")
			}
			if itest.Owner(t, f.repo.Path(paths.ResticPassword)) != "1000:1000" {
				return fmt.Errorf("the password file isn't the admin's")
			}
			return nil
		}},
		{"the stack's volumes are found", nil, nil, func() error {
			vols, err := s.Volumes()
			if err != nil {
				return err
			}
			for _, v := range []string{"caddy_caddy-data", "nextcloud_db-data-v18", "nextcloud_nc-data", "vaultwarden_vw-data"} {
				if !slices.Contains(vols, v) {
					return fmt.Errorf("%s missing from %v", v, vols)
				}
			}
			if slices.Contains(vols, "nextcloud_redis-data") {
				return fmt.Errorf("the Redis cache is backed up: %v", vols)
			}
			return nil
		}},
		{"back up", s.Run, []string{"staged the SQLite fileset", "volumes to back up"}, func() error {
			if f.dk.ContainerState(layout.Vaultwarden) != "running" {
				return fmt.Errorf("Vaultwarden was left stopped")
			}
			if !backup.DumpComplete(f.repo.Path(paths.Staging, backup.NextcloudDumpFile)) {
				return fmt.Errorf("no complete Nextcloud dump in staging")
			}
			if fr := s.Freshness(); !fr.Known || fr.Stale || fr.Failed {
				return fmt.Errorf("freshness %+v", fr)
			}
			if snaps, err := s.Snapshots(); err != nil || !strings.Contains(snaps, "homeserver") {
				return fmt.Errorf("snapshots: %v\n%s", err, snaps)
			}
			if stats, err := s.Stats(); err != nil || !strings.Contains(stats, "Total Size") {
				return fmt.Errorf("stats: %v\n%s", err, stats)
			}
			return nil
		}},
		{"change everything", func() error {
			f.sqlite("UPDATE itest SET v='after'")
			if err := os.Remove(f.volume(layout.VaultwardenVolume, "attachments", "itest.txt")); err != nil {
				return err
			}
			f.psql("UPDATE itest SET v='after'")
			itest.WriteFile(t, f.volume("nextcloud_nc-data"), "itest.txt", "after")
			itest.WriteFile(t, f.volume("nextcloud_nc-data"), "extra.txt", "added since")
			return nil
		}, nil, func() error {
			if got := f.markers(); got != "vw-db=after vw-file= nc-db=after nc-file=after nc-extra=true" {
				return fmt.Errorf("markers: %s", got)
			}
			return nil
		}},
		{"a restore nobody confirmed changes nothing", func() error {
			return s.Restore(backup.RestoreOptions{IntoVolumes: true})
		}, []string{"aborted — the snapshot is still extracted"}, func() error {
			if got := f.markers(); !strings.HasPrefix(got, "vw-db=after") {
				return fmt.Errorf("markers: %s", got)
			}
			return nil
		}},
		{"restore into the volumes", func() error { return s.Restore(backup.RestoreOptions{IntoVolumes: true, Yes: true}) },
			[]string{"restored vaultwarden SQLite fileset", "rebuilt the nextcloud database from its consistent dump",
				"removed the extracted copy"}, func() error {
				f.waitReady()
				if got := f.markers(); got != before {
					return fmt.Errorf("markers after the restore: %s", got)
				}
				if fileExists(f.repo.Path(paths.RestoreDir)) {
					return fmt.Errorf("the plaintext extraction was left behind")
				}
				return nil
			}},
		{"check the repository", func() error { return s.Check("100%") }, []string{"the backup repository is intact"}, nil},
		{"prune", s.Forget, nil, nil},
		{"replicate off-site", func() error {
			if err := s.Configure(backup.Options{Replica: &replica}); err != nil {
				return err
			}
			return s.Replicate()
		}, []string{"replica repo not initialized yet — creating it", "copying snapshots to the replica"}, func() error {
			if s.ReplicaAge() == "" {
				return fmt.Errorf("no replica stamp")
			}
			return nil
		}},
		{"the next backup goes off-site too", s.Run, []string{"== off-site replica =="}, nil},
		{"extract without touching the stack", func() error {
			return s.Restore(backup.RestoreOptions{Target: extract})
		}, []string{"delete it when you're done"}, func() error {
			if !fileExists(filepath.Join(extract, "var/lib/docker/volumes/nextcloud_nc-data/_data/itest.txt")) {
				return fmt.Errorf("nothing extracted to %s", extract)
			}
			return nil
		}},
		{"a chosen target that isn't empty is refused for a put-back", func() error {
			err := s.Restore(backup.RestoreOptions{Target: extract, IntoVolumes: true, Yes: true})
			if err == nil || !strings.Contains(err.Error(), "isn't empty") {
				return fmt.Errorf("Restore = %v", err)
			}
			return nil
		}, nil, nil},
		{"self-test", func() error { return s.Verify("", false) }, []string{"0 failed, 0 skipped", "Backups verified end-to-end"}, nil},
		{"change the password", func() error {
			return s.SetPassword(s.Settings(), "a-brand-new-password-1", f.out)
		}, []string{"Changed the backup password"}, func() error {
			return s.List()
		}},
	}
	for _, st := range steps {
		f.out.Reset()
		if st.run != nil {
			if err := st.run(); err != nil {
				t.Fatalf("%s: %v\n%s", st.name, err, f.out.String())
			}
		}
		for _, w := range st.wantOut {
			if !strings.Contains(f.out.String(), w) {
				t.Fatalf("%s: output lacks %q:\n%s", st.name, w, f.out.String())
			}
		}
		if st.check != nil {
			if err := st.check(); err != nil {
				t.Fatalf("%s: %v\n%s", st.name, err, f.out.String())
			}
		}
	}
}
