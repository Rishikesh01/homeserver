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
