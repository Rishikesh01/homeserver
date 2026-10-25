package main

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestSetResticPassword drives every way a backup password gets set against real restic
// repositories: before init, putting a saved one back, changing it (primary and replica together),
// and refusing one that would lock the backups away.
func TestSetResticPassword(t *testing.T) {
	if !resticInstalled() {
		t.Skip("restic not installed")
	}
	const old, newer = "the-original-password", "a-brand-new-password"
	// initRepo makes a repository at path with password pw.
	initRepo := func(t *testing.T, path, pw string) {
		t.Helper()
		pf := filepath.Join(t.TempDir(), "pw")
		if err := os.WriteFile(pf, []byte(pw+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		c := exec.Command("restic", "init", "-q")
		c.Env = resticEnv(path, pf)
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("init: %v %s", err, out)
		}
	}
	opens := func(t *testing.T, repo, path, pw string) bool {
		pf := filepath.Join(t.TempDir(), "pw")
		_ = os.WriteFile(pf, []byte(pw+"\n"), 0600)
		st, err := probePassword(repo, path, pf)
		return err == nil && st == repoOpens
	}
	cases := []struct {
		name     string
		repoPass string // "" = no repository yet
		saved    string // the saved password file's content, "" = none
		replica  bool
		set      string
		wantErr  string
		opensNew bool // afterwards the repository (and replica) open with set
	}{
		{"before init: too short", "", "", false, "short", "at least 12", false},
		{"before init", "", "", false, newer, "", false},
		{"putting the saved password back", old, "", false, old, "", true},
		{"the wrong password, nothing saved", old, "", false, newer, "doesn't open", false},
		{"change it", old, old, false, newer, "", true},
		{"change it on the replica too", old, old, true, newer, "", true},
		{"neither opens it", old, "not-the-password", false, newer, "neither", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := t.TempDir()
			cfg := backupCfg{Repo: filepath.Join(repo, "primary"), Retention: defaultRetention}
			if tc.repoPass != "" {
				initRepo(t, cfg.Repo, tc.repoPass)
			}
			if tc.replica {
				cfg.ReplicaRepo = filepath.Join(repo, "replica")
				initRepo(t, cfg.ReplicaRepo, tc.repoPass)
			}
			if tc.saved != "" {
				if err := os.WriteFile(filepath.Join(repo, resticPassFile), []byte(tc.saved+"\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			err := setResticPassword(repo, cfg, tc.set, io.Discard)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				if b, _ := os.ReadFile(filepath.Join(repo, resticPassFile)); strings.TrimSpace(string(b)) != tc.saved {
					t.Errorf("a refused password changed the saved one to %q", b)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if b, _ := os.ReadFile(filepath.Join(repo, resticPassFile)); strings.TrimSpace(string(b)) != tc.set {
				t.Errorf("saved password = %q, want %q", b, tc.set)
			}
			if tc.opensNew {
				for _, r := range []string{cfg.Repo, cfg.ReplicaRepo} {
					if r != "" && !opens(t, repo, r, tc.set) {
						t.Errorf("%s doesn't open with the new password", r)
					}
					if r != "" && tc.set != tc.repoPass && opens(t, repo, r, tc.repoPass) {
						t.Errorf("%s still opens with the old password", r)
					}
				}
			}
		})
	}
}
