package envfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestReadKVInlineComments: values are read the way docker compose reads a .env — an unquoted
// value stops at " #", a quoted one keeps everything between its quotes.
func TestReadKVInlineComments(t *testing.T) {
	cases := []struct{ line, key, want string }{
		{"SERVER_IP=192.168.1.10          # this host's LAN IP", "SERVER_IP", "192.168.1.10"},
		{"VW_SIGNUPS_ALLOWED=false\t# invitation-only", "VW_SIGNUPS_ALLOWED", "false"},
		{"PASS=abc#def", "PASS", "abc#def"},
		{`QUOTED="a # b"   # comment`, "QUOTED", "a # b"},
		{`SINGLE='x y'`, "SINGLE", "x y"},
		{"VW_ADMIN_TOKEN=$$argon2id$$v=19$$m=65540", "VW_ADMIN_TOKEN", "$$argon2id$$v=19$$m=65540"},
		{"EMPTY=", "EMPTY", ""},
	}
	for _, tc := range cases {
		t.Run(tc.key, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "f")
			if err := os.WriteFile(p, []byte("# header\n"+tc.line+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			kv, err := Read(p)
			if err != nil {
				t.Fatal(err)
			}
			if got := kv[tc.key]; got != tc.want {
				t.Errorf("%s = %q, want %q", tc.key, got, tc.want)
			}
		})
	}
}
func TestUpsertEnvKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	// Missing file: created — it-tools/imagetools have no .env until their first update.
	if err := Upsert(path, "ITTOOLS_IMAGE", "a:1"); err != nil {
		t.Fatal(err)
	}
	// Existing file: other keys survive, the key is replaced in place.
	if err := Upsert(path, "OTHER", "x"); err != nil {
		t.Fatal(err)
	}
	if err := Upsert(path, "ITTOOLS_IMAGE", "a:2"); err != nil {
		t.Fatal(err)
	}
	kv, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if kv["ITTOOLS_IMAGE"] != "a:2" || kv["OTHER"] != "x" {
		t.Errorf("readKV = %v, want ITTOOLS_IMAGE=a:2 and OTHER=x", kv)
	}
}

func TestRemoveEnvKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	orig := "SERVER_IP=192.168.0.150\n" +
		"VAULT_UPSTREAM=host.docker.internal:8082\n" +
		"HOME_UPSTREAM=host.docker.internal:8088\n" +
		"# a comment stays\n" +
		"CLOUD_UPSTREAM=host.docker.internal:8081\n" +
		"VAULT_HTTPS=8443\n"
	if err := os.WriteFile(path, []byte(orig), 0600); err != nil {
		t.Fatal(err)
	}

	changed, err := RemoveKeys(path, "VAULT_UPSTREAM", "CLOUD_UPSTREAM", "PIHOLE_UPSTREAM")
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected changed=true when keys are present")
	}
	got, _ := os.ReadFile(path)
	for _, gone := range []string{"VAULT_UPSTREAM", "CLOUD_UPSTREAM"} {
		if strings.Contains(string(got), gone) {
			t.Errorf("%s should have been removed, file is:\n%s", gone, got)
		}
	}
	// The dashboard upstream (still a host process), other settings, and comments must survive.
	for _, keep := range []string{
		"SERVER_IP=192.168.0.150", "HOME_UPSTREAM=host.docker.internal:8088",
		"# a comment stays", "VAULT_HTTPS=8443",
	} {
		if !strings.Contains(string(got), keep) {
			t.Errorf("%q should have been preserved, file is:\n%s", keep, got)
		}
	}

	// Second run is a no-op (nothing left to remove).
	if changed, err := RemoveKeys(path, "VAULT_UPSTREAM", "CLOUD_UPSTREAM"); err != nil || changed {
		t.Errorf("second run should be a no-op, got changed=%v err=%v", changed, err)
	}
	// Missing file is a silent no-op, not an error.
	if changed, err := RemoveKeys(filepath.Join(dir, "absent.env"), "X"); err != nil || changed {
		t.Errorf("missing file should be a no-op, got changed=%v err=%v", changed, err)
	}
}
