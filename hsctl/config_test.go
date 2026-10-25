package main

import (
	"os"
	"path/filepath"
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
			kv, err := readKV(p)
			if err != nil {
				t.Fatal(err)
			}
			if got := kv[tc.key]; got != tc.want {
				t.Errorf("%s = %q, want %q", tc.key, got, tc.want)
			}
		})
	}
}

// TestSetupConfExampleParses: the shipped example must pre-seed cleanly — every value it sets is
// a plain value, never a comment glued onto it.
func TestSetupConfExampleParses(t *testing.T) {
	kv, err := readKV(filepath.Join("..", "setup.conf.example"))
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range kv {
		for _, c := range v {
			if c == '#' || c == ' ' {
				t.Errorf("%s=%q carries a comment or space", k, v)
				break
			}
		}
	}
	if kv["VW_SIGNUPS_ALLOWED"] != "true" {
		t.Errorf("VW_SIGNUPS_ALLOWED = %q, want true", kv["VW_SIGNUPS_ALLOWED"])
	}
}

// TestGenerateForceRotatesInPlace: --force rotates the secrets that are safe to rotate, inside the
// existing files, and never rewrites the admin's own custom.list or caddy/.env.
func TestGenerateForceRotatesInPlace(t *testing.T) {
	repo := setupTestRepo(t)
	c := Config{ServerIP: "10.0.0.5", TZ: "Etc/UTC", ACMEEmail: "a@b", UIPort: 8088, PiholeDNSBind: "0.0.0.0"}
	if _, err := c.Generate(repo, false); err != nil {
		t.Fatal(err)
	}
	list := filepath.Join(repo, "pihole/custom.list")
	if err := os.WriteFile(list, []byte("192.168.1.2 nas.lan\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := setEnvKey(filepath.Join(repo, "pihole/.env"), "MY_NOTE", "keep"); err != nil {
		t.Fatal(err)
	}
	before := map[string]map[string]string{}
	for _, f := range []string{"vaultwarden/.env", "pihole/.env", "caddy/.env"} {
		before[f], _ = readKV(filepath.Join(repo, f))
	}
	res, err := c.Generate(repo, true)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		file, key string
		rotated   bool
	}{
		{"vaultwarden/.env", "VW_ADMIN_TOKEN", true},
		{"pihole/.env", "PIHOLE_PASSWORD", true},
		{"pihole/.env", "MY_NOTE", false},
		{"caddy/.env", "SERVER_IP", false},
	}
	for _, tc := range cases {
		t.Run(tc.file+" "+tc.key, func(t *testing.T) {
			after, _ := readKV(filepath.Join(repo, tc.file))
			if got := after[tc.key] != before[tc.file][tc.key]; got != tc.rotated {
				t.Errorf("rotated = %v, want %v", got, tc.rotated)
			}
		})
	}
	if b, _ := os.ReadFile(list); string(b) != "192.168.1.2 nas.lan\n" {
		t.Errorf("custom.list was rewritten: %q", b)
	}
	if len(res.Secrets) < 2 {
		t.Errorf("rotated logins not reported: %v", res.Secrets)
	}
}
