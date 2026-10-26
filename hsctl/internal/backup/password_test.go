//go:build integration

package backup_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Rishikesh01/homeserver/hsctl/internal/backup"
	"github.com/Rishikesh01/homeserver/hsctl/internal/itest"
	"github.com/Rishikesh01/homeserver/hsctl/internal/paths"
	"github.com/Rishikesh01/homeserver/hsctl/internal/restic"
)

// TestSetPassword drives every way a backup password gets set against real restic repositories:
// before init, putting a saved one back, changing it (primary and replica together), and refusing
// one that would lock the backups away.
func TestSetPassword(t *testing.T) {
	itest.NeedRestic(t)
	const old, newer = "the-original-password", "a-brand-new-password"
	pwFile := func(t *testing.T, pw string) string {
		return itest.WriteFile(t, t.TempDir(), "pw", pw+"\n")
	}
	initRepo := func(t *testing.T, path, pw string) {
		t.Helper()
		if out, err := restic.New(pwFile(t, pw), nil).Output(path, "init"); err != nil {
			t.Fatalf("init: %v %s", err, out)
		}
	}
	opens := func(t *testing.T, path, pw string) bool {
		_, err := restic.New(pwFile(t, pw), nil).Output(path, "cat", "config")
		return err == nil
	}
	cases := []struct {
		name     string
		repoPass string // "" = no repository yet
		saved    string // the saved password file's content, "" = none
		replica  bool
		set      string
		wantErr  string
		wantOut  string
		opensNew bool // afterwards the repository (and replica) open with set
	}{
		{"before init: too short", "", "", false, "short", "at least 12", "", false},
		{"before init", "", "", false, newer, "", "creates the repository with it", false},
		{"putting the saved password back", old, "", false, old, "", "", true},
		{"the wrong password, nothing saved", old, "", false, newer, "doesn't open", "", false},
		{"change it", old, old, false, newer, "", "Changed the backup password", true},
		{"change it on the replica too", old, old, true, newer, "", "Changed the backup password", true},
		{"neither opens it", old, "not-the-password", false, newer, "neither", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := itest.Repo(t)
			cfg := backup.Settings{Repo: filepath.Join(t.TempDir(), "primary"), Retention: backup.DefaultRetention}
			if tc.repoPass != "" {
				initRepo(t, cfg.Repo, tc.repoPass)
			}
			if tc.replica {
				cfg.ReplicaRepo = filepath.Join(t.TempDir(), "replica")
				initRepo(t, cfg.ReplicaRepo, tc.repoPass)
			}
			if tc.saved != "" {
				itest.WriteFile(t, repo.Root, paths.ResticPassword, tc.saved+"\n")
			}
			s, _ := newService(t, repo)
			var out bytes.Buffer
			err := s.SetPassword(cfg, tc.set, &out)
			saved := strings.TrimSpace(itest.ReadFile(repo.Path(paths.ResticPassword)))
			if (err == nil) != (tc.wantErr == "") || (err != nil && !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("SetPassword = %v, want %q", err, tc.wantErr)
			}
			if err != nil {
				if saved != tc.saved {
					t.Errorf("a refused password changed the saved one to %q", saved)
				}
				return
			}
			if saved != tc.set || !strings.Contains(out.String(), tc.wantOut) {
				t.Errorf("saved %q, output %q", saved, out.String())
			}
			if tc.opensNew {
				for _, r := range []string{cfg.Repo, cfg.ReplicaRepo} {
					if r != "" && !opens(t, r, tc.set) {
						t.Errorf("%s doesn't open with the new password", r)
					}
					if r != "" && tc.set != tc.repoPass && opens(t, r, tc.repoPass) {
						t.Errorf("%s still opens with the old password", r)
					}
				}
			}
		})
	}
}

// TestRequirePassword: every command but init needs the saved password, and says how to get one.
func TestRequirePassword(t *testing.T) {
	cases := []struct {
		name    string
		saved   bool
		wantErr string
	}{
		{"saved", true, ""},
		{"missing", false, "Put your saved password back"},
	}
	for _, tc := range cases {
		repo := itest.Repo(t)
		if tc.saved {
			itest.WriteFile(t, repo.Root, paths.ResticPassword, "pw\n")
		}
		s, _ := newService(t, repo)
		if err := s.RequirePassword(); (err == nil) != (tc.wantErr == "") || (err != nil && !strings.Contains(err.Error(), tc.wantErr)) {
			t.Errorf("%s: %v", tc.name, err)
		}
	}
}

// TestReadPasswordArg: `--password -` reads one line from stdin, so the password stays off the
// command line.
func TestReadPasswordArg(t *testing.T) {
	cases := []struct {
		name, arg, stdin, want string
		wantErr                bool
	}{
		{"given", "on-the-line", "", "on-the-line", false},
		{"from stdin", "-", "from stdin pw\nmore\n", "from stdin pw", false},
		{"windows line end", "-", "pw-crlf\r\n", "pw-crlf", false},
		{"no newline", "-", "last-line", "last-line", false},
		{"nothing", "-", "", "", true},
	}
	orig := os.Stdin
	defer func() { os.Stdin = orig }()
	for _, tc := range cases {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.WriteString(tc.stdin)
		w.Close()
		os.Stdin = r
		got, err := backup.ReadPasswordArg(tc.arg)
		r.Close()
		if got != tc.want || (err != nil) != tc.wantErr {
			t.Errorf("%s: ReadPasswordArg = %q, %v", tc.name, got, err)
		}
	}
}
