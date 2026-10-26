//go:build integration

package envfile_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Rishikesh01/homeserver/hsctl/internal/envfile"
	"github.com/Rishikesh01/homeserver/hsctl/internal/itest"
)

// TestRead: values are read the way docker compose reads a .env — an unquoted value stops at " #",
// a quoted one keeps everything between its quotes; comments and junk lines are skipped.
func TestRead(t *testing.T) {
	cases := []struct{ line, key, want string }{
		{"SERVER_IP=192.168.1.10          # this host's LAN IP", "SERVER_IP", "192.168.1.10"},
		{"VW_SIGNUPS_ALLOWED=false\t# invitation-only", "VW_SIGNUPS_ALLOWED", "false"},
		{"PASS=abc#def", "PASS", "abc#def"},
		{`QUOTED="a # b"   # comment`, "QUOTED", "a # b"},
		{`SINGLE='x y'`, "SINGLE", "x y"},
		{`OPEN="unterminated`, "OPEN", `"unterminated`},
		{"VW_ADMIN_TOKEN=$$argon2id$$v=19$$m=65540", "VW_ADMIN_TOKEN", "$$argon2id$$v=19$$m=65540"},
		{"  SPACED = value  ", "SPACED", "value"},
		{"EMPTY=", "EMPTY", ""},
		{"# COMMENTED=1", "COMMENTED", ""},
		{"no equals sign", "no equals sign", ""},
	}
	for _, tc := range cases {
		t.Run(tc.key, func(t *testing.T) {
			p := itest.WriteFile(t, t.TempDir(), ".env", "# header\n\n"+tc.line+"\n")
			kv, err := envfile.Read(p)
			if err != nil {
				t.Fatal(err)
			}
			if got := kv[tc.key]; got != tc.want {
				t.Errorf("%s = %q, want %q", tc.key, got, tc.want)
			}
		})
	}
	if _, err := envfile.Read(filepath.Join(t.TempDir(), "missing")); !os.IsNotExist(err) {
		t.Errorf("a missing file must be reported as such, got %v", err)
	}
}

// TestEdit: every edit changes only its own lines — other keys, comments and order survive — and
// reports whether it changed anything.
func TestEdit(t *testing.T) {
	const orig = "SERVER_IP=192.168.0.150\n" +
		"VAULT_UPSTREAM=host.docker.internal:8082\n" +
		"# a comment stays\n" +
		"CLOUD_UPSTREAM=host.docker.internal:8081\n" +
		"VAULT_HTTPS=8443\n"
	cases := []struct {
		name    string
		missing bool // start without the file
		edit    func(path string) (bool, error)
		want    string // the whole file afterwards ("" = must not exist)
		changed bool
		wantErr bool
	}{
		{"set replaces in place", false, func(p string) (bool, error) {
			return true, envfile.Set(p, "VAULT_HTTPS", "9443")
		}, "SERVER_IP=192.168.0.150\nVAULT_UPSTREAM=host.docker.internal:8082\n# a comment stays\n" +
			"CLOUD_UPSTREAM=host.docker.internal:8081\nVAULT_HTTPS=9443\n", true, false},
		{"set appends a new key", false, func(p string) (bool, error) {
			return true, envfile.Set(p, "NEW", "x")
		}, orig + "NEW=x\n", true, false},
		{"set on a missing file fails", true, func(p string) (bool, error) {
			return false, envfile.Set(p, "NEW", "x")
		}, "", false, true},
		{"upsert creates a missing file", true, func(p string) (bool, error) {
			return true, envfile.Upsert(p, "ITTOOLS_IMAGE", "a:1")
		}, "ITTOOLS_IMAGE=a:1\n", true, false},
		{"upsert replaces in an existing file", false, func(p string) (bool, error) {
			return true, envfile.Upsert(p, "SERVER_IP", "10.0.0.2")
		}, "SERVER_IP=10.0.0.2\nVAULT_UPSTREAM=host.docker.internal:8082\n# a comment stays\n" +
			"CLOUD_UPSTREAM=host.docker.internal:8081\nVAULT_HTTPS=8443\n", true, false},
		{"remove keys", false, func(p string) (bool, error) {
			return envfile.RemoveKeys(p, "VAULT_UPSTREAM", "CLOUD_UPSTREAM", "PIHOLE_UPSTREAM")
		}, "SERVER_IP=192.168.0.150\n# a comment stays\nVAULT_HTTPS=8443\n", true, false},
		{"remove keys that aren't there", false, func(p string) (bool, error) {
			return envfile.RemoveKeys(p, "PIHOLE_UPSTREAM")
		}, orig, false, false},
		{"remove from a missing file", true, func(p string) (bool, error) {
			return envfile.RemoveKeys(p, "X")
		}, "", false, false},
		{"remove matching values", false, func(p string) (bool, error) {
			return envfile.RemoveMatching(p, func(_, v string) bool { return v == "host.docker.internal:8081" })
		}, "SERVER_IP=192.168.0.150\nVAULT_UPSTREAM=host.docker.internal:8082\n# a comment stays\nVAULT_HTTPS=8443\n", true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), ".env")
			if !tc.missing {
				itest.WriteFile(t, filepath.Dir(p), ".env", orig)
			}
			changed, err := tc.edit(p)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, want error %v", err, tc.wantErr)
			}
			if changed != tc.changed {
				t.Errorf("changed = %v, want %v", changed, tc.changed)
			}
			got, readErr := os.ReadFile(p)
			if tc.want == "" {
				if readErr == nil {
					t.Errorf("the file must not exist, has %q", got)
				}
				return
			}
			if string(got) != tc.want {
				t.Errorf("file =\n%q\nwant\n%q", got, tc.want)
			}
			if fi, err := os.Stat(p); err != nil || fi.Mode().Perm() != 0600 {
				t.Errorf("an edited .env must stay private (0600): %v %v", fi.Mode(), err)
			}
		})
	}
}
