package fsutil

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// File ownership when hsctl runs as root. Plenty of hsctl runs are root: `sudo hsctl …`, every
// dashboard button (the UI is a root systemd service that re-execs hsctl), and the nightly backup
// timer. A file root creates is root-owned, and the config files are 0600 — so after one root run
// the admin could no longer read or edit their own .env files, `hsctl secrets show` found nothing,
// and `docker compose` (run as the admin, via the docker group) failed with "permission denied".
//
// The rule: when root writes a file or creates a directory, it takes the owner of the directory it
// lives in. Inside the repo that's the admin who cloned it, whichever way root got involved — sudo,
// the systemd service or the timer (only sudo sets SUDO_UID, so we don't rely on it). Anything
// written into a root-owned directory (/etc/systemd, a throwaway temp dir) stays root's. Modes are
// unchanged, so secrets stay 0600: readable by their owner and by root. The admin is in the docker
// group, which is root-equivalent anyway, so handing them their files costs nothing.

// OwnerOf returns the uid/gid that owns path (without following a final symlink).
func OwnerOf(path string) (uid, gid int, ok bool) {
	fi, err := os.Lstat(path)
	if err != nil {
		return 0, 0, false
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, false
	}
	return int(st.Uid), int(st.Gid), true
}

// InheritOwner chowns path to the owner of its parent directory when we're running as root. As a
// normal user it's a no-op: the file is already ours, and we couldn't chown it anyway. Best-effort —
// failing to hand a file back to the admin must never fail the write that created it.
func InheritOwner(path string) {
	if os.Geteuid() != 0 {
		return
	}
	if uid, gid, ok := OwnerOf(filepath.Dir(path)); ok {
		_ = os.Lchown(path, uid, gid)
	}
}

// MkdirAllOwned is os.MkdirAll, except that every directory it creates while running as root takes
// the owner of the existing directory above it (see InheritOwner). Directories that already exist
// are left alone.
func MkdirAllOwned(path string, perm os.FileMode) error {
	path = filepath.Clean(path)
	var missing []string // deepest first
	for p := path; ; p = filepath.Dir(p) {
		if _, err := os.Lstat(p); err == nil {
			break
		}
		missing = append(missing, p)
		if filepath.Dir(p) == p {
			break
		}
	}
	if err := os.MkdirAll(path, perm); err != nil {
		return err
	}
	for i := len(missing) - 1; i >= 0; i-- { // parents first, so each child inherits the fixed owner
		InheritOwner(missing[i])
	}
	return nil
}

// CreateOwned is os.Create for a file hsctl writes into the repo: as root, the new file takes the
// owner of its directory before anything is written to it. It never follows a symlink at path.
func CreateOwned(path string, perm os.FileMode) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC|syscall.O_NOFOLLOW, perm)
	if err != nil {
		return nil, err
	}
	InheritOwner(path)
	return f, nil
}

// HandBackTree gives every root-owned entry under dir to the owner of owner (a directory) — for
// trees a root child process wrote (restic writing a repo that lives inside the homeserver repo).
// It never follows symlinks, and is a no-op unless we're root and owner belongs to someone else.
func HandBackTree(dir, owner string) {
	if os.Geteuid() != 0 {
		return
	}
	uid, gid, ok := OwnerOf(owner)
	if !ok || uid == 0 {
		return
	}
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if u, _, ok := OwnerOf(p); ok && u == 0 {
			_ = os.Lchown(p, uid, gid)
		}
		return nil
	})
}

// Within reports whether path is dir or lies inside it (lexically; both are cleaned first).
func Within(path, dir string) bool {
	rel, err := filepath.Rel(filepath.Clean(dir), filepath.Clean(path))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// RepairOwnership hands back to the owner of root any of the given root-relative files that an
// older hsctl, run as root, left root-owned. It only runs as root, only touches the paths given,
// and only when the directory holding a file belongs to someone else (the repo's owner) — the
// same rule new writes follow. It returns the paths it fixed.
func RepairOwnership(root string, rels []string) []string {
	if os.Geteuid() != 0 {
		return nil
	}
	var fixed []string
	for _, rel := range rels {
		p := filepath.Join(root, rel)
		uid, _, ok := OwnerOf(p)
		if !ok || uid != 0 {
			continue
		}
		puid, pgid, ok := OwnerOf(filepath.Dir(p))
		if !ok || puid == 0 {
			continue
		}
		if os.Lchown(p, puid, pgid) == nil {
			fixed = append(fixed, rel)
		}
	}
	return fixed
}

// Unreadable reports whether path exists but this user can't open it — the signature of a file a
// root run left behind, which must not be mistaken for "missing".
func Unreadable(path string) bool {
	f, err := os.Open(path)
	if err == nil {
		f.Close()
		return false
	}
	return errors.Is(err, fs.ErrPermission)
}

// UnreadableFiles lists the given root-relative files that exist but can't be read.
func UnreadableFiles(root string, rels ...string) []string {
	var bad []string
	for _, rel := range rels {
		if Unreadable(filepath.Join(root, rel)) {
			bad = append(bad, rel)
		}
	}
	return bad
}

// ErrUnreadable explains files we can't read and how to get them back, instead of the misleading
// "not set up yet" a silent read failure used to produce.
func ErrUnreadable(root string, rels []string) error {
	return fmt.Errorf("can't read %s — owned by another user (usually root, from an older hsctl run "+
		"with sudo or from the dashboard).\nGive them back to yourself with:\n"+
		"  cd %s && sudo chown \"$USER\": %s\n"+
		"(or run any hsctl command once with sudo — it repairs these files)",
		strings.Join(rels, ", "), root, strings.Join(rels, " "))
}

// RequireReadable fails with ErrUnreadable when any of the given files can't be read.
func RequireReadable(root string, rels ...string) error {
	if bad := UnreadableFiles(root, rels...); len(bad) > 0 {
		return ErrUnreadable(root, bad)
	}
	return nil
}
