package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPrepareRestoreTarget proves the fix for the restore-contamination bug: the reused restore
// target must be emptied before `restic restore`, so leftovers from a prior extraction can't be
// copied into the live volumes. It also must refuse to wipe a dangerous (system) path.
func TestPrepareRestoreTarget(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "restore")

	// Simulate a previous restore leaving a whole volume tree behind.
	stale := filepath.Join(target, "var", "lib", "docker", "volumes", "old_vol", "_data")
	if err := os.MkdirAll(stale, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stale, "stale.txt"), []byte("leftover"), 0600); err != nil {
		t.Fatal(err)
	}

	if err := prepareRestoreTarget(target); err != nil {
		t.Fatalf("prepareRestoreTarget: %v", err)
	}
	if !fileExists(target) {
		t.Fatal("target directory should exist after prepare")
	}
	entries, err := os.ReadDir(target)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("target must be empty after prepare, has %d entries (stale data would contaminate the restore)", len(entries))
	}

	// Guard: never RemoveAll a blank or system path.
	for _, bad := range []string{"", ".", "/"} {
		if err := prepareRestoreTarget(bad); err == nil {
			t.Errorf("prepareRestoreTarget(%q) should be refused as unsafe", bad)
		}
	}
}

// TestPlanRestore: the snapshot is checked before the stack is touched, and Vaultwarden's staged
// database is found wherever the repo lived when the backup was taken.
func TestPlanRestore(t *testing.T) {
	type file struct{ path, body string }
	vol := func(name, f string) file { return file{"var/lib/docker/volumes/" + name + "/_data/" + f, "x"} }
	cases := []struct {
		name        string
		files       []file
		wantErr     string
		wantVols    []string
		wantStaging string // suffix, "" = none
	}{
		{"empty snapshot", nil, "no volume data", nil, ""},
		{"vault on a different machine's path",
			[]file{vol("vaultwarden_vw-data", "rsa_key.pem"), vol("nextcloud_nc-data", "a.txt"),
				{"home/olduser/homeserver/backups/staging/vaultwarden/db.sqlite3", "db"}},
			"", []string{"nextcloud_nc-data", "vaultwarden_vw-data"}, "home/olduser/homeserver/backups/staging/vaultwarden"},
		{"vault files without their database",
			[]file{vol("vaultwarden_vw-data", "rsa_key.pem")}, "not its database", nil, ""},
		{"vault stopped at backup time (db in the volume)",
			[]file{vol("vaultwarden_vw-data", "db.sqlite3")}, "", []string{"vaultwarden_vw-data"}, ""},
		{"legacy layout: whole vault in staging",
			[]file{vol("caddy_caddy-data", "x"), {"opt/homeserver/backups/staging/vaultwarden/db.sqlite3", "db"}},
			"", []string{"caddy_caddy-data"}, "opt/homeserver/backups/staging/vaultwarden"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			target := t.TempDir()
			for _, f := range tc.files {
				p := filepath.Join(target, f.path)
				if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, []byte(f.body), 0644); err != nil {
					t.Fatal(err)
				}
			}
			p, err := planRestore(target)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want it to mention %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if strings.Join(p.volumes, ",") != strings.Join(tc.wantVols, ",") {
				t.Errorf("volumes = %v, want %v", p.volumes, tc.wantVols)
			}
			if tc.wantStaging == "" && p.vwStaging != "" || tc.wantStaging != "" && !strings.HasSuffix(p.vwStaging, tc.wantStaging) {
				t.Errorf("staging = %q, want suffix %q", p.vwStaging, tc.wantStaging)
			}
		})
	}
}

// TestDumpComplete: only a pg_dump that ran to its completion footer is trusted on restore.
func TestDumpComplete(t *testing.T) {
	cases := []struct {
		name, body string
		want       bool
	}{
		{"complete", "CREATE TABLE x();\n--\n" + pgDumpFooter + "\n--\n\n", true},
		{"cut short", "CREATE TABLE x();\nCOPY x FROM stdin;\n1\n", false},
		{"empty", "", false},
		{"footer far from the end", pgDumpFooter + "\n" + strings.Repeat("x", 4096), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), nextcloudDumpFile)
			if err := os.WriteFile(p, []byte(tc.body), 0600); err != nil {
				t.Fatal(err)
			}
			if got := dumpComplete(p); got != tc.want {
				t.Errorf("dumpComplete = %v, want %v", got, tc.want)
			}
		})
	}
}
