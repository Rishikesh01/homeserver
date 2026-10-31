//go:build integration

package backup_test

import (
	"fmt"
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
		wantImages  bool
	}{
		{"empty snapshot", nil, "no volume data", nil, "", false, false},
		{"vault on a different machine's path",
			[]file{vol("vaultwarden_vw-data", "rsa_key.pem"), vol("nextcloud_nc-data", "a.txt"),
				{"home/olduser/homeserver/backups/staging/vaultwarden/db.sqlite3", "db"}},
			"", []string{"nextcloud_nc-data", "vaultwarden_vw-data"}, "home/olduser/homeserver/backups/staging/vaultwarden", false, false},
		{"vault files without their database", []file{vol("vaultwarden_vw-data", "rsa_key.pem")}, "not its database", nil, "", false, false},
		{"vault stopped at backup time (database in the volume)",
			[]file{vol("vaultwarden_vw-data", "db.sqlite3")}, "", []string{"vaultwarden_vw-data"}, "", false, false},
		{"the dump without Vaultwarden's staging",
			[]file{vol("nextcloud_db-data-v18", "PG_VERSION"), {"srv/hs/backups/staging/nextcloud-db.sql", dump}},
			"", []string{"nextcloud_db-data-v18"}, "", true, false},
		{"a truncated dump isn't trusted",
			[]file{vol("nextcloud_db-data-v18", "PG_VERSION"), {"srv/hs/backups/staging/nextcloud-db.sql", "CREATE TABLE x();\n"}},
			"", []string{"nextcloud_db-data-v18"}, "", false, false},
		{"database only in the pre-18 volume", []file{vol("nextcloud_db-data", "PG_VERSION")}, "old nextcloud_db-data volume", nil, "", false, false},
		{"legacy layout: the whole vault in staging",
			[]file{vol("caddy_caddy-data", "x"), {"opt/homeserver/backups/staging/vaultwarden/db.sqlite3", "db"}},
			"", []string{"caddy_caddy-data"}, "opt/homeserver/backups/staging/vaultwarden", false, false},
		{"the app versions it was taken with",
			[]file{vol("caddy_caddy-data", "x"), {"srv/hs/backups/staging/" + backup.ImageManifestFile, `{"images":[]}`}},
			"", []string{"caddy_caddy-data"}, "", false, true},
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
			if (p.Images != "") != tc.wantImages {
				t.Errorf("image record = %q, want one: %v", p.Images, tc.wantImages)
			}
		})
	}
}

// TestReadImageManifest: a snapshot's record of its app versions is used only when every entry is
// what a backup writes — a restore puts its values into .env files and loads the files it names.
func TestReadImageManifest(t *testing.T) {
	entry := func(folder, varName, ref, archive string) string {
		return fmt.Sprintf(`{"platform":"linux/amd64","images":[{"folder":%q,"var":%q,"ref":%q,"archive":%q}]}`, folder, varName, ref, archive)
	}
	cases := []struct {
		name, body string
		wantErr    string
	}{
		{"a backup's record", entry("stirling", "STIRLING_IMAGE", "ghcr.io/stirling-tools/stirling-pdf:3.2.2", "ghcr.io_stirling-tools_stirling-pdf_3.2.2.tar"), ""},
		{"by digest, not saved", entry("nextcloud", "NEXTCLOUD_IMAGE", "nextcloud:35.0.2-apache@sha256:0123abcd", ""), ""},
		{"a bare pin", entry("caddy", "", "caddy:2.11.7-alpine", ""), ""},
		{"not JSON", "images: []", "images.json"},
		{"a folder outside the checkout", entry("../etc", "X_IMAGE", "busybox:1.37.0", ""), "isn't a service folder"},
		{"a variable that isn't one", entry("caddy", "CADDY_IMAGE=evil", "busybox:1.37.0", ""), "isn't a variable name"},
		{"a reference that would add a line to .env", entry("caddy", "CADDY_IMAGE", "busybox:1.37.0\nVW_ADMIN_TOKEN=x", ""), "isn't an image reference"},
		{"an archive outside the images folder", entry("caddy", "CADDY_IMAGE", "busybox:1.37.0", "../../etc/shadow"), "isn't a saved image's file name"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := itest.WriteFile(t, t.TempDir(), backup.ImageManifestFile, tc.body)
			m, err := backup.ReadImageManifest(p)
			if (err == nil) != (tc.wantErr == "") || (err != nil && !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("ReadImageManifest = %v, want %q", err, tc.wantErr)
			}
			if err == nil && (m.Platform != "linux/amd64" || len(m.Images) != 1) {
				t.Errorf("manifest = %+v", m)
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
