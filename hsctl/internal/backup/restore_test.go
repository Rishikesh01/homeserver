//go:build integration

package backup_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Rishikesh01/homeserver/hsctl/internal/backup"
	"github.com/Rishikesh01/homeserver/hsctl/internal/itest"
)

// TestPrepareTarget: the reused extraction folder is emptied before restic writes into it — restic
// only adds files, so leftovers would be copied into the live volumes — and a blank or system path
// is never wiped.
func TestPrepareTarget(t *testing.T) {
	cases := []struct {
		name    string
		target  func(t *testing.T) string
		wantErr bool
	}{
		{"leftovers wiped", func(t *testing.T) string {
			target := filepath.Join(t.TempDir(), "restore")
			itest.WriteFile(t, target, "var/lib/docker/volumes/old_vol/_data/stale.txt", "leftover")
			return target
		}, false},
		{"created", func(t *testing.T) string { return filepath.Join(t.TempDir(), "a", "restore") }, false},
		{"blank", func(*testing.T) string { return "" }, true},
		{"current folder", func(*testing.T) string { return "." }, true},
		{"root", func(*testing.T) string { return "/" }, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			target := tc.target(t)
			err := backup.PrepareTarget(target)
			if (err != nil) != tc.wantErr {
				t.Fatalf("PrepareTarget(%q) = %v", target, err)
			}
			if err != nil {
				return
			}
			entries, err := os.ReadDir(target)
			if err != nil || len(entries) != 0 {
				t.Errorf("target not an empty folder: %v %v", entries, err)
			}
		})
	}
}

// TestPlanRestore: an extracted snapshot is checked before the stack is touched, and its staged
// databases are found wherever the repo lived when the backup was taken.
func TestPlanRestore(t *testing.T) {
	type file struct{ path, body string }
	vol := func(name, f string) file { return file{"var/lib/docker/volumes/" + name + "/_data/" + f, "x"} }
	dump := "CREATE TABLE x();\n" + backup.PgDumpFooter + "\n"
	cases := []struct {
		name        string
		files       []file
		wantErr     string
		wantVols    []string
		wantStaging string // suffix, "" = none
		wantDump    bool
	}{
		{"empty snapshot", nil, "no volume data", nil, "", false},
		{"vault on a different machine's path",
			[]file{vol("vaultwarden_vw-data", "rsa_key.pem"), vol("nextcloud_nc-data", "a.txt"),
				{"home/olduser/homeserver/backups/staging/vaultwarden/db.sqlite3", "db"}},
			"", []string{"nextcloud_nc-data", "vaultwarden_vw-data"}, "home/olduser/homeserver/backups/staging/vaultwarden", false},
		{"vault files without their database", []file{vol("vaultwarden_vw-data", "rsa_key.pem")}, "not its database", nil, "", false},
		{"vault stopped at backup time (database in the volume)",
			[]file{vol("vaultwarden_vw-data", "db.sqlite3")}, "", []string{"vaultwarden_vw-data"}, "", false},
		{"the dump without Vaultwarden's staging",
			[]file{vol("nextcloud_db-data-v18", "PG_VERSION"), {"srv/hs/backups/staging/nextcloud-db.sql", dump}},
			"", []string{"nextcloud_db-data-v18"}, "", true},
		{"a truncated dump isn't trusted",
			[]file{vol("nextcloud_db-data-v18", "PG_VERSION"), {"srv/hs/backups/staging/nextcloud-db.sql", "CREATE TABLE x();\n"}},
			"", []string{"nextcloud_db-data-v18"}, "", false},
		{"database only in the pre-18 volume", []file{vol("nextcloud_db-data", "PG_VERSION")}, "old nextcloud_db-data volume", nil, "", false},
		{"legacy layout: the whole vault in staging",
			[]file{vol("caddy_caddy-data", "x"), {"opt/homeserver/backups/staging/vaultwarden/db.sqlite3", "db"}},
			"", []string{"caddy_caddy-data"}, "opt/homeserver/backups/staging/vaultwarden", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			target := t.TempDir()
			for _, f := range tc.files {
				itest.WriteFile(t, target, f.path, f.body)
			}
			p, err := backup.PlanRestore(target)
			if (err == nil) != (tc.wantErr == "") || (err != nil && !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("PlanRestore = %v, want %q", err, tc.wantErr)
			}
			if err != nil {
				if !strings.Contains(err.Error(), "nothing was changed") {
					t.Errorf("a refusal must say nothing was changed: %v", err)
				}
				return
			}
			if strings.Join(p.Volumes, ",") != strings.Join(tc.wantVols, ",") {
				t.Errorf("volumes = %v, want %v", p.Volumes, tc.wantVols)
			}
			if (tc.wantStaging == "" && p.VWStaging != "") || (tc.wantStaging != "" && !strings.HasSuffix(p.VWStaging, tc.wantStaging)) {
				t.Errorf("staging = %q, want suffix %q", p.VWStaging, tc.wantStaging)
			}
			if (p.NCDump != "") != tc.wantDump {
				t.Errorf("dump = %q, want one: %v", p.NCDump, tc.wantDump)
			}
		})
	}
}

// TestDumpComplete: only a pg_dump that ran to its completion footer is trusted.
func TestDumpComplete(t *testing.T) {
	cases := []struct {
		name, body string
		want       bool
	}{
		{"complete", "CREATE TABLE x();\n--\n" + backup.PgDumpFooter + "\n--\n\n", true},
		{"cut short", "CREATE TABLE x();\nCOPY x FROM stdin;\n1\n", false},
		{"empty", "", false},
		{"footer far from the end", backup.PgDumpFooter + "\n" + strings.Repeat("x", 4096), false},
	}
	for _, tc := range cases {
		p := itest.WriteFile(t, t.TempDir(), backup.NextcloudDumpFile, tc.body)
		if got := backup.DumpComplete(p); got != tc.want {
			t.Errorf("%s: DumpComplete = %v, want %v", tc.name, got, tc.want)
		}
	}
	if backup.DumpComplete(filepath.Join(t.TempDir(), "missing.sql")) {
		t.Error("a missing dump isn't complete")
	}
}
