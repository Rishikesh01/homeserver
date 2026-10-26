// Package fsutil writes hsctl's files safely: atomically, and — when hsctl runs as root — owned by
// whoever owns the folder they're written into, so the admin can always read their own config.
package fsutil

import (
	"os"
	"path/filepath"
)

// WriteAtomic writes content to path atomically: it writes a temp file in the SAME directory,
// fsyncs it, then renames it over path. A rename is all-or-nothing on POSIX, so a crash or power
// loss mid-write can never leave a half-written secret file — a truncated .env or password is worse
// than a missing one, since `setup` skips (won't regenerate) a file that exists. On any failure the
// temp file is removed and path is left untouched. Run as root, the file takes the owner of its
// directory (see owner.go), so the admin can still read it.
func WriteAtomic(path, content string, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".hsctl-tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	remove := true
	defer func() {
		if remove {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	// The rename replaces the inode, so a root run (sudo, the dashboard service or the backup
	// timer) would otherwise hand even an admin-owned file to root. Give the temp file the owner
	// of its directory before it takes the real name.
	if os.Geteuid() == 0 {
		if uid, gid, ok := OwnerOf(filepath.Dir(path)); ok {
			if err := tmp.Chown(uid, gid); err != nil {
				tmp.Close()
				return err
			}
		}
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	remove = false // renamed into place; nothing to clean up
	return nil
}

// WritePrivate writes a 0600 file atomically (secrets and settings).
func WritePrivate(path, content string) error { return WriteAtomic(path, content, 0600) }

// WritePublic writes a 0644 file atomically.
func WritePublic(path, content string) error { return WriteAtomic(path, content, 0644) }

// Exists reports whether path exists.
func Exists(path string) bool { _, err := os.Stat(path); return err == nil }
