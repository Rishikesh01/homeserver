//go:build integration

package backup_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Rishikesh01/homeserver/hsctl/internal/backup"
	"github.com/Rishikesh01/homeserver/hsctl/internal/itest"
	"github.com/Rishikesh01/homeserver/hsctl/internal/paths"
)

// TestRefusals: each repository operation refuses — saying what to do — when it can't do its job
// properly, instead of writing a bad backup or a second repository.
func TestRefusals(t *testing.T) {
	itest.NeedRestic(t)
	itest.NeedDocker(t)
	cases := []struct {
		name    string
		prep    func(t *testing.T, repo paths.Repo, s *backup.Service)
		run     func(s *backup.Service) error
		wantErr string
	}{
		{"restic missing", func(t *testing.T, repo paths.Repo, s *backup.Service) { t.Setenv("PATH", t.TempDir()) },
			(*backup.Service).List, "restic not installed"},
		{"no password", initialized(false), backUp, "no backup password"},
		{"no repository yet", func(t *testing.T, repo paths.Repo, s *backup.Service) {
			itest.WriteFile(t, repo.Root, paths.ResticPassword, "pw-123456789\n")
			configure(t, s, filepath.Join(t.TempDir(), "restic"))
		}, (*backup.Service).List, "there's no backup repository at"},
		{"nothing to back up", initialized(true), backUp, "no stack data volumes found"},
		{"a broken retention policy", func(t *testing.T, repo paths.Repo, s *backup.Service) {
			initialized(true)(t, repo, s)
			itest.WriteFile(t, repo.Root, paths.BackupConf, itest.ReadFile(repo.Path(paths.BackupConf))+"RETENTION=7 daily\n")
		}, (*backup.Service).Forget, "fix it with: hsctl backup config --retention"},
		{"no replica", initialized(true), (*backup.Service).Replicate, "no off-site replica configured"},
		{"the backup disk isn't mounted", func(t *testing.T, repo paths.Repo, s *backup.Service) {
			initialized(true)(t, repo, s)
			guard := t.TempDir()
			if err := s.Configure(backup.Options{RequireMount: &guard}); err != nil {
				t.Fatal(err)
			}
		}, backUp, "backup disk is NOT mounted"},
		{"a second repository over an existing one", func(t *testing.T, repo paths.Repo, s *backup.Service) {
			initialized(true)(t, repo, s)
			if err := exec.Command("rm", repo.Path(paths.ResticPassword)).Run(); err != nil {
				t.Fatal(err)
			}
		}, (*backup.Service).Init, "put your saved password back first"},
		{"a repository on the system disk", func(t *testing.T, repo paths.Repo, s *backup.Service) {
			// The disk's empty mountpoint, with the guard switched off: init must still notice.
			itest.NeedRoot(t)
			disk := "/mnt/" + itest.Name("unplugged")
			if err := os.Mkdir(disk, 0755); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.RemoveAll(disk) })
			dest, off := disk+"/restic", ""
			if err := s.Configure(backup.Options{Repo: &dest, RequireMount: &off}); err != nil {
				t.Fatal(err)
			}
		}, (*backup.Service).Init, "Refusing to create the repository there"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := itest.Repo(t)
			s, out := newService(t, repo)
			tc.prep(t, repo, s)
			if err := tc.run(s); err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("err = %v, want %q\n%s", err, tc.wantErr, out.String())
			}
		})
	}
}

// backUp takes a backup the way the nightly timer does.
func backUp(s *backup.Service) error { return s.Run(backup.RunOptions{}) }

// configure points the backups at dest.
func configure(t *testing.T, s *backup.Service, dest string) {
	t.Helper()
	if err := s.Configure(backup.Options{Repo: &dest}); err != nil {
		t.Fatal(err)
	}
}

// initialized prepares a repository in a temp folder (keepPassword: and its password file).
func initialized(keepPassword bool) func(t *testing.T, repo paths.Repo, s *backup.Service) {
	return func(t *testing.T, repo paths.Repo, s *backup.Service) {
		t.Helper()
		configure(t, s, filepath.Join(t.TempDir(), "restic"))
		if err := s.Init(); err != nil {
			t.Fatal(err)
		}
		if !keepPassword {
			if err := exec.Command("rm", repo.Path(paths.ResticPassword)).Run(); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// TestInitPassword: init generates the password only when there isn't one, and a failed init never
// leaves a generated password behind (it would stand in for the real one later).
func TestInitPassword(t *testing.T) {
	itest.NeedRestic(t)
	cases := []struct {
		name      string
		saved     string
		dest      func(t *testing.T) string
		wantErr   bool
		wantSaved bool
	}{
		{"generated", "", func(t *testing.T) string { return filepath.Join(t.TempDir(), "restic") }, false, true},
		{"kept", "my-own-password-123", func(t *testing.T) string { return filepath.Join(t.TempDir(), "restic") }, false, true},
		{"removed after a failed init", "", func(t *testing.T) string { return "/proc/itest/restic" }, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := itest.Repo(t)
			if tc.saved != "" {
				itest.WriteFile(t, repo.Root, paths.ResticPassword, tc.saved+"\n")
			}
			s, out := newService(t, repo)
			configure(t, s, tc.dest(t))
			if err := s.Init(); (err != nil) != tc.wantErr {
				t.Fatalf("Init = %v\n%s", err, out.String())
			}
			saved := strings.TrimSpace(itest.ReadFile(repo.Path(paths.ResticPassword)))
			if (saved != "") != tc.wantSaved || (tc.saved != "" && saved != tc.saved) {
				t.Errorf("saved password = %q", saved)
			}
			if tc.wantErr && !strings.Contains(out.String(), "removed the password generated for this init") {
				t.Errorf("output:\n%s", out.String())
			}
		})
	}
}

// TestRequireRoot: the operations that read Docker's volume files say to use sudo.
func TestRequireRoot(t *testing.T) {
	if !itest.Unprivileged(t) {
		return
	}
	cases := []struct {
		name string
		run  func() error
		want string
	}{
		{"backup run", func() error { return backup.RequireRoot("backup run", "it reads Docker's volume files") }, "sudo hsctl backup run"},
		{"put-back", func() error {
			s, _ := newService(t, itest.Repo(t))
			return s.Restore(backup.RestoreOptions{IntoVolumes: true})
		}, "sudo hsctl backup restore --into-volumes"},
		{"self-test", func() error {
			s, _ := newService(t, itest.Repo(t))
			return s.Verify("", false)
		}, "sudo hsctl backup verify"},
	}
	for _, tc := range cases {
		if err := tc.run(); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v", tc.name, err)
		}
	}
}
