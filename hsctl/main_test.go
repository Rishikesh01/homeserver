//go:build integration

package main

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Rishikesh01/homeserver/hsctl/internal/itest"
)

// TestCLI runs the real hsctl: the commands are wired to their services, refuse to run outside a
// checkout, and catch the mistakes the services can't see (a bool flag's value after a space).
func TestCLI(t *testing.T) {
	bin := itest.Hsctl(t)
	repo := itest.Repo(t)
	cases := []struct {
		name    string
		dir     string // "" = the checkout
		args    []string
		wantErr bool
		want    string
	}{
		{"version", "", []string{"--version"}, false, "hsctl version"},
		{"every command is there", "", []string{"--help"}, false, "apps        List apps"},
		{"backup commands", "", []string{"backup", "--help"}, false, "replicate   Copy all snapshots"},
		{"outside a checkout", t.TempDir(), []string{"status"}, true, "not in the homeserver repo"},
		{"a bool flag's value after a space", "", []string{"setup", "--vw-signups", "false"}, true, "give a true/false flag its value with '='"},
		{"a stray argument", "", []string{"setup", "extra"}, true, `unexpected argument "extra"`},
		{"unknown app", "", []string{"apps", "enable", "nosuch"}, true, `unknown app "nosuch"`},
		{"logins of a fresh checkout", "", []string{"secrets", "show"}, false, "No logins found"},
		{"from a subfolder of the checkout", filepath.Join(repo.Root, "caddy"), []string{"secrets", "show"}, false, "No logins found"},
		{"dashboard password", "", []string{"secrets", "set-ui-password", "short"}, true, "at least 8 characters"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command(bin, tc.args...)
			cmd.Dir = repo.Root
			cmd.Env = append(cmd.Environ(), "HOMESERVER_DIR=")
			if tc.dir != "" {
				cmd.Dir = tc.dir
			}
			out, err := cmd.CombinedOutput()
			if (err != nil) != tc.wantErr || !strings.Contains(string(out), tc.want) {
				t.Errorf("hsctl %s: %v\n%s", strings.Join(tc.args, " "), err, out)
			}
		})
	}
}
