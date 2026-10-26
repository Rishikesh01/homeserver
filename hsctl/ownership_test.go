package main

import (
	"os"
	"path/filepath"
	"testing"
)

// adminUID is the stand-in for the repo owner in the root-only tests below.
const adminUID, adminGID = 1000, 1000

// newAdminRepo returns a temp dir owned by the stand-in admin. Root only.
func newAdminRepo(t *testing.T) string {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("needs root: run inside a container (see Makefile test-root)")
	}
	repo := t.TempDir()
	if err := os.Chown(repo, adminUID, adminGID); err != nil {
		t.Fatal(err)
	}
	return repo
}

func uidOf(t *testing.T, path string) int {
	t.Helper()
	uid, _, ok := ownerOf(path)
	if !ok {
		t.Fatalf("stat %s failed", path)
	}
	return uid
}

// TestRootWritesTakeDirOwner covers every way hsctl writes into the repo as root: each new file or
// directory must belong to the repo's owner, and replacing a root-owned file must hand it back.
func TestRootWritesTakeDirOwner(t *testing.T) {
	cases := []struct {
		name  string
		write func(repo string) (string, error)
	}{
		{"atomic write of a new file", func(repo string) (string, error) {
			p := filepath.Join(repo, "setup.conf")
			return p, writeFile0600(p, "SERVER_IP=1.2.3.4\n")
		}},
		{"atomic write replacing a root-owned file", func(repo string) (string, error) {
			p := filepath.Join(repo, ".ui-password")
			if err := os.WriteFile(p, []byte("old\n"), 0600); err != nil {
				return "", err
			}
			return p, writeFile0600(p, "new\n")
		}},
		{"nested directories", func(repo string) (string, error) {
			p := filepath.Join(repo, "backups", "staging", "vaultwarden")
			return p, mkdirAllOwned(p, 0700)
		}},
		{"streamed file", func(repo string) (string, error) {
			p := filepath.Join(repo, "dump.sql")
			f, err := createOwned(p, 0600)
			if err != nil {
				return "", err
			}
			return p, f.Close()
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := newAdminRepo(t)
			p, err := tc.write(repo)
			if err != nil {
				t.Fatal(err)
			}
			for q := p; q != repo; q = filepath.Dir(q) {
				if got := uidOf(t, q); got != adminUID {
					t.Errorf("%s owned by uid %d, want %d", q, got, adminUID)
				}
			}
		})
	}
}

// TestRootWriteOutsideRepoStaysRoot: a file root writes into a root-owned directory (a temp dir,
// /etc) must stay root's — the rule follows the directory, not a fixed user.
func TestRootWriteOutsideRepoStaysRoot(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root")
	}
	p := filepath.Join(t.TempDir(), "x")
	if err := writeFile0600(p, "x"); err != nil {
		t.Fatal(err)
	}
	if got := uidOf(t, p); got != 0 {
		t.Errorf("owned by uid %d, want 0", got)
	}
}

// TestRepairOwnership hands back what an older root run left root-owned, and only that.
func TestRepairOwnership(t *testing.T) {
	cases := []struct {
		name      string
		rootFiles []string // created root-owned
		other     []string // created root-owned but not hsctl's
		want      []string
	}{
		{"nothing to fix", nil, nil, nil},
		{"config files", []string{"setup.conf", "vaultwarden/.env"}, nil, []string{"setup.conf", "vaultwarden/.env"}},
		{"leaves unrelated files", []string{".ui-password"}, []string{"notes.txt"}, []string{".ui-password"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := newAdminRepo(t)
			if err := mkdirAllOwned(filepath.Join(repo, "vaultwarden"), 0755); err != nil {
				t.Fatal(err)
			}
			for _, rel := range append(append([]string{}, tc.rootFiles...), tc.other...) {
				if err := os.WriteFile(filepath.Join(repo, rel), []byte("x"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			got := repairOwnership(repo)
			if len(got) != len(tc.want) {
				t.Fatalf("fixed %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("fixed %v, want %v", got, tc.want)
				}
			}
			for _, rel := range tc.other {
				if uidOf(t, filepath.Join(repo, rel)) != 0 {
					t.Errorf("%s was handed back but isn't hsctl's", rel)
				}
			}
		})
	}
}
