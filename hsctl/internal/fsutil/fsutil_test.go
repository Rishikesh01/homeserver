//go:build integration

package fsutil_test

import (
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Rishikesh01/homeserver/hsctl/internal/fsutil"
	"github.com/Rishikesh01/homeserver/hsctl/internal/itest"
)

// admin is the stand-in for the person who cloned the repo, in the tests that run as root.
const admin = "1000:1000"

// adminDir returns a temp folder owned by the stand-in admin, as a checkout would be.
func adminDir(t *testing.T) string {
	t.Helper()
	itest.NeedRoot(t)
	d := t.TempDir()
	itest.Chown(t, d, 1000, 1000)
	return d
}

// TestWriteAtomic: content and mode are exact, an existing file is replaced (not appended to), no
// temp file is left behind — and a root write takes the owner of the folder it lands in, so the
// admin can still read their own config after `sudo hsctl …`, a dashboard button or the timer.
func TestWriteAtomic(t *testing.T) {
	cases := []struct {
		name      string
		dir       func(t *testing.T) string
		existing  string // written root-owned first, if set
		write     func(path string) error
		wantPerm  os.FileMode
		wantOwner string // "" = don't check
		want      string
	}{
		{"private file", func(t *testing.T) string { return t.TempDir() }, "",
			func(p string) error { return fsutil.WritePrivate(p, "SECRET=one\n") }, 0600, "", "SECRET=one\n"},
		{"public file", func(t *testing.T) string { return t.TempDir() }, "",
			func(p string) error { return fsutil.WritePublic(p, "x") }, 0644, "", "x"},
		{"replaces, never appends", func(t *testing.T) string { return t.TempDir() }, "SECRET=one\nOLD=1\n",
			func(p string) error { return fsutil.WritePrivate(p, "SECRET=two\n") }, 0600, "", "SECRET=two\n"},
		{"root writing into the admin's folder", adminDir, "",
			func(p string) error { return fsutil.WritePrivate(p, "SERVER_IP=1.2.3.4\n") }, 0600, admin, "SERVER_IP=1.2.3.4\n"},
		{"root replacing a root-owned file in the admin's folder", adminDir, "old\n",
			func(p string) error { return fsutil.WritePrivate(p, "new\n") }, 0600, admin, "new\n"},
		{"root writing into a root folder stays root's", func(t *testing.T) string {
			itest.NeedRoot(t)
			return t.TempDir()
		}, "", func(p string) error { return fsutil.WritePrivate(p, "x") }, 0600, "0:0", "x"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := tc.dir(t)
			p := filepath.Join(dir, ".env")
			if tc.existing != "" {
				if err := os.WriteFile(p, []byte(tc.existing), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := tc.write(p); err != nil {
				t.Fatal(err)
			}
			if got := itest.ReadFile(p); got != tc.want {
				t.Errorf("content = %q, want %q", got, tc.want)
			}
			if fi, _ := os.Stat(p); fi.Mode().Perm() != tc.wantPerm {
				t.Errorf("mode = %v, want %v", fi.Mode().Perm(), tc.wantPerm)
			}
			if tc.wantOwner != "" {
				if got := itest.Owner(t, p); got != tc.wantOwner {
					t.Errorf("owner = %s, want %s", got, tc.wantOwner)
				}
			}
			entries, _ := os.ReadDir(dir)
			for _, e := range entries {
				if strings.HasPrefix(e.Name(), ".hsctl-tmp-") {
					t.Errorf("leftover temp file: %s", e.Name())
				}
			}
		})
	}
}

// TestOwnedCreates: every other way root creates something in the repo — nested folders, a file
// streamed into place, a tree a root child process wrote — ends up the admin's, and nothing follows
// a symlink planted where a file should be.
func TestOwnedCreates(t *testing.T) {
	cases := []struct {
		name    string
		create  func(t *testing.T, repo string) (paths []string, err error)
		wantErr bool
	}{
		{"nested folders", func(t *testing.T, repo string) ([]string, error) {
			p := filepath.Join(repo, "backups", "staging", "vaultwarden")
			return []string{filepath.Dir(filepath.Dir(p)), filepath.Dir(p), p}, fsutil.MkdirAllOwned(p, 0700)
		}, false},
		{"streamed file", func(t *testing.T, repo string) ([]string, error) {
			p := filepath.Join(repo, "dump.sql")
			f, err := fsutil.CreateOwned(p, 0600)
			if err != nil {
				return nil, err
			}
			return []string{p}, f.Close()
		}, false},
		{"streamed file refuses a planted symlink", func(t *testing.T, repo string) ([]string, error) {
			target := filepath.Join(t.TempDir(), "elsewhere")
			p := filepath.Join(repo, "dump.sql")
			if err := os.Symlink(target, p); err != nil {
				t.Fatal(err)
			}
			_, err := fsutil.CreateOwned(p, 0600)
			if fsutil.Exists(target) {
				t.Error("CreateOwned followed the symlink")
			}
			return nil, err
		}, true},
		{"a tree root wrote, handed back", func(t *testing.T, repo string) ([]string, error) {
			tree := filepath.Join(repo, "backups", "restic")
			if err := os.MkdirAll(filepath.Join(tree, "data", "00"), 0700); err != nil {
				t.Fatal(err)
			}
			itest.WriteFile(t, tree, "config", "x")
			fsutil.HandBackTree(filepath.Join(repo, "backups"), repo)
			return []string{filepath.Join(repo, "backups"), tree, filepath.Join(tree, "data", "00"), filepath.Join(tree, "config")}, nil
		}, false},
		{"inherit owner of one file", func(t *testing.T, repo string) ([]string, error) {
			p := itest.WriteFile(t, repo, "WELCOME.txt", "hi")
			fsutil.InheritOwner(p)
			return []string{p}, nil
		}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := adminDir(t)
			got, err := tc.create(t, repo)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, want error %v", err, tc.wantErr)
			}
			for _, p := range got {
				if o := itest.Owner(t, p); o != admin {
					t.Errorf("%s owned by %s, want %s", p, o, admin)
				}
			}
		})
	}
}

// TestRepairOwnership hands back what an older root run left root-owned — only hsctl's own files,
// and only where the folder holding them belongs to someone else.
func TestRepairOwnership(t *testing.T) {
	owned := []string{"setup.conf", "vaultwarden/.env", ".ui-password"}
	cases := []struct {
		name      string
		rootFiles []string // created root-owned
		other     []string // root-owned, but not hsctl's
		rootDir   bool     // the checkout itself is root's
		want      []string
	}{
		{"nothing to fix", nil, nil, false, nil},
		{"config files", []string{"setup.conf", "vaultwarden/.env"}, nil, false, []string{"setup.conf", "vaultwarden/.env"}},
		{"leaves unrelated files", []string{".ui-password"}, []string{"notes.txt"}, false, []string{".ui-password"}},
		{"a root-owned checkout keeps root's files", []string{"setup.conf"}, nil, true, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := adminDir(t)
			if err := fsutil.MkdirAllOwned(filepath.Join(repo, "vaultwarden"), 0755); err != nil {
				t.Fatal(err)
			}
			if tc.rootDir {
				itest.Chown(t, repo, 0, 0)
			}
			for _, rel := range append(append([]string{}, tc.rootFiles...), tc.other...) {
				if err := os.WriteFile(filepath.Join(repo, rel), []byte("x"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if got := fsutil.RepairOwnership(repo, owned); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("fixed %v, want %v", got, tc.want)
			}
			for _, rel := range tc.want {
				if o := itest.Owner(t, filepath.Join(repo, rel)); o != admin {
					t.Errorf("%s owned by %s after the repair", rel, o)
				}
			}
			for _, rel := range tc.other {
				if o := itest.Owner(t, filepath.Join(repo, rel)); o != "0:0" {
					t.Errorf("%s was handed back but isn't hsctl's", rel)
				}
			}
		})
	}
}

// TestUnreadable runs as a normal user: a file that exists but can't be opened (left root-owned by
// an older run) is reported as unreadable — with how to get it back — never mistaken for missing.
func TestUnreadable(t *testing.T) {
	if !itest.Unprivileged(t) {
		return
	}
	repo := t.TempDir()
	itest.WriteFile(t, repo, "setup.conf", "x")
	itest.WriteFile(t, repo, "vaultwarden/.env", "x")
	itest.WriteFile(t, repo, "pihole/.env", "x")
	for _, rel := range []string{"setup.conf", "vaultwarden/.env"} {
		if err := os.Chmod(filepath.Join(repo, rel), 0); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		rel        string
		unreadable bool
	}{
		{"setup.conf", true},
		{"vaultwarden/.env", true},
		{"pihole/.env", false},
		{"missing.conf", false},
	}
	for _, tc := range cases {
		if got := fsutil.Unreadable(filepath.Join(repo, tc.rel)); got != tc.unreadable {
			t.Errorf("Unreadable(%s) = %v, want %v", tc.rel, got, tc.unreadable)
		}
	}
	all := []string{"setup.conf", "vaultwarden/.env", "pihole/.env", "missing.conf"}
	if got := fsutil.UnreadableFiles(repo, all...); !reflect.DeepEqual(got, []string{"setup.conf", "vaultwarden/.env"}) {
		t.Errorf("UnreadableFiles = %v", got)
	}
	err := fsutil.RequireReadable(repo, all...)
	if err == nil || !strings.Contains(err.Error(), "setup.conf, vaultwarden/.env") || !strings.Contains(err.Error(), "sudo chown") {
		t.Errorf("RequireReadable should name the files and the fix: %v", err)
	}
	if err := fsutil.RequireReadable(repo, "pihole/.env", "missing.conf"); err != nil {
		t.Errorf("readable or missing files must pass: %v", err)
	}
}

// TestWithin is the lexical containment check the restore and hand-back rules lean on.
func TestWithin(t *testing.T) {
	cases := []struct {
		path, dir string
		want      bool
	}{
		{"/srv/hs/backups/restic", "/srv/hs", true},
		{"/srv/hs", "/srv/hs", true},
		{"/srv/hs/../other", "/srv/hs", false},
		{"/srv/hsx", "/srv/hs", false},
		{"/mnt/backup", "/srv/hs", false},
		{"/srv/hs/./a/", "/srv/hs/", true},
	}
	for _, tc := range cases {
		if got := fsutil.Within(tc.path, tc.dir); got != tc.want {
			t.Errorf("Within(%s, %s) = %v, want %v", tc.path, tc.dir, got, tc.want)
		}
	}
}

// TestCopy: the restore's copies keep modes, owners and links (cp -a), the wipe empties a folder
// but keeps it (it's a mountpoint), and the checksum is the file's.
func TestCopy(t *testing.T) {
	itest.NeedRoot(t)
	src := t.TempDir()
	itest.WriteFile(t, src, "db.sqlite3", "data")
	itest.WriteFile(t, src, "attachments/a.bin", "attachment")
	if err := os.Symlink("db.sqlite3", filepath.Join(src, "link")); err != nil {
		t.Fatal(err)
	}
	itest.Chown(t, src, 1000, 1000)
	cases := []struct {
		name  string
		run   func(dst string) error
		check func(t *testing.T, dst string)
	}{
		{"one file", func(dst string) error {
			return fsutil.CopyFile(filepath.Join(src, "db.sqlite3"), filepath.Join(dst, "db.sqlite3"), io.Discard)
		}, func(t *testing.T, dst string) {
			p := filepath.Join(dst, "db.sqlite3")
			if itest.ReadFile(p) != "data" || itest.Owner(t, p) != admin {
				t.Errorf("copy = %q owned by %s", itest.ReadFile(p), itest.Owner(t, p))
			}
		}},
		{"a whole tree", func(dst string) error { return fsutil.CopyContents(src, dst, io.Discard) },
			func(t *testing.T, dst string) {
				if itest.ReadFile(filepath.Join(dst, "attachments/a.bin")) != "attachment" {
					t.Error("nested file not copied")
				}
				if l, err := os.Readlink(filepath.Join(dst, "link")); err != nil || l != "db.sqlite3" {
					t.Errorf("symlink not kept: %q %v", l, err)
				}
				if o := itest.Owner(t, filepath.Join(dst, "attachments")); o != admin {
					t.Errorf("owner not kept: %s", o)
				}
			}},
		{"wipe keeps the folder", func(dst string) error {
			if err := fsutil.CopyContents(src, dst, io.Discard); err != nil {
				return err
			}
			return fsutil.WipeDirContents(dst)
		}, func(t *testing.T, dst string) {
			entries, err := os.ReadDir(dst)
			if err != nil || len(entries) != 0 {
				t.Errorf("after the wipe: %v %v", entries, err)
			}
		}},
		{"checksum", func(string) error { return nil }, func(t *testing.T, _ string) {
			sum, err := fsutil.SHA256File(filepath.Join(src, "db.sqlite3"))
			// sha256("data")
			if err != nil || sum != "3a6eb0790f39ac87c94f3856b2dd2c5d110e6811602261a9a923d3bb23adc8b7" {
				t.Errorf("SHA256File = %s %v", sum, err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dst := t.TempDir()
			if err := tc.run(dst); err != nil {
				t.Fatal(err)
			}
			tc.check(t, dst)
		})
	}
}
