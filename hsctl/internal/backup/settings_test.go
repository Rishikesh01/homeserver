package backup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Rishikesh01/homeserver/hsctl/internal/docker"
	"github.com/Rishikesh01/homeserver/hsctl/internal/paths"
	"github.com/Rishikesh01/homeserver/hsctl/internal/restic"
)

// newSvc is the backup service for a repo at root, with the real Docker and restic clients.
func newSvc(t *testing.T, root string) *Service {
	t.Helper()
	repo := paths.Repo{Root: root}
	s := New(repo, docker.New(root), restic.New(repo.Path(paths.ResticPassword), CredentialsEnv(repo)), nil, nil)
	s.Out, s.Err = os.Stderr, os.Stderr
	return s
}

// TestSettingsRoundTrip: what SaveSettings writes, Settings reads back — including clearing.
func TestSettingsRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		in   Settings
	}{
		{"replica and alerts", Settings{Repo: "/mnt/backup/restic", Retention: DefaultRetention, ReplicaRepo: "sftp:nas:/b", NotifyURL: "https://ntfy.sh/x", RequireMount: "/mnt/backup"}},
		{"cleared", Settings{Repo: "/mnt/backup/restic", Retention: DefaultRetention}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newSvc(t, t.TempDir())
			if err := s.SaveSettings(tc.in); err != nil {
				t.Fatal(err)
			}
			if got := s.Settings(); got != tc.in {
				t.Errorf("round-trip = %+v, want %+v", got, tc.in)
			}
		})
	}
}

// TestCredentialsEnv passes .backup-env's credentials to restic, and copes with no file.
func TestCredentialsEnv(t *testing.T) {
	cases := []struct {
		name, file string
		want       []string
	}{
		{"credentials", "AWS_ACCESS_KEY_ID=abc\nAWS_SECRET_ACCESS_KEY=def\n", []string{"AWS_ACCESS_KEY_ID=abc", "AWS_SECRET_ACCESS_KEY=def"}},
		{"no file", "", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := paths.Repo{Root: t.TempDir()}
			if tc.file != "" {
				if err := os.WriteFile(repo.Path(paths.BackupEnv), []byte(tc.file), 0600); err != nil {
					t.Fatal(err)
				}
			}
			env := strings.Join(CredentialsEnv(repo)(), "\n")
			for _, w := range tc.want {
				if !strings.Contains(env, w) {
					t.Errorf("env lacks %q", w)
				}
			}
		})
	}
}

// TestReplicate proves the replica copy for real with two local restic repos: it creates the
// replica on the first run, copies the snapshot, and a second run is a clean no-op; without a
// replica configured it says how to set one.
func TestReplicate(t *testing.T) {
	root := t.TempDir()
	s := newSvc(t, root)
	if !s.Restic.Installed() {
		t.Skip("restic not installed")
	}
	if err := os.WriteFile(filepath.Join(root, paths.ResticPassword), []byte("test-password\n"), 0600); err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(root, "data")
	if err := os.MkdirAll(data, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(data, "proof.txt"), []byte("replicate me"), 0644); err != nil {
		t.Fatal(err)
	}
	cfg := Settings{Repo: filepath.Join(root, "primary"), ReplicaRepo: filepath.Join(root, "replica")}
	if err := s.Restic.Run(cfg.Repo, "init", "-q"); err != nil {
		t.Fatal(err)
	}
	if err := s.Restic.Run(cfg.Repo, "backup", "-q", data); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name    string
		cfg     Settings
		wantErr string
	}{
		{"first copy creates the replica", cfg, ""},
		{"second copy is a no-op", cfg, ""},
		{"unconfigured", Settings{Repo: cfg.Repo}, "no off-site replica configured"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := s.replicate(tc.cfg)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			out, err := s.Restic.Output(cfg.ReplicaRepo, "snapshots")
			if err != nil || !strings.Contains(out, "1 snapshots") {
				t.Errorf("replica snapshots: %v\n%s", err, out)
			}
		})
	}
}
