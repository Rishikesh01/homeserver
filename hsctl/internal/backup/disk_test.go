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
)

// TestGuardFor: a local destination on an external-disk mountpoint gets that mountpoint as its
// guard; anything else gets none — and a derived guard follows the destination, an admin's stays.
func TestGuardFor(t *testing.T) {
	guards := []struct{ repo, want string }{
		{"/mnt/backup/restic", "/mnt/backup"},
		{"/mnt/restic", "/mnt/restic"},
		{"/media/alice/disk/restic", "/media/alice/disk"},
		{"/media/disk", "/media/disk"},
		{"/run/media/alice/disk/r", "/run/media/alice/disk"},
		{"/home/alice/homeserver/backups/restic", ""},
		{"sftp:user@nas:/backups", ""},
		{"/mnt", ""},
	}
	for _, tc := range guards {
		if got := backup.GuardFor(tc.repo); got != tc.want {
			t.Errorf("GuardFor(%s) = %q, want %q", tc.repo, got, tc.want)
		}
	}
	follows := []struct {
		guard, oldRepo, newRepo, want string
		changed                       bool
	}{
		{"", "/srv/x", "/mnt/usb/restic", "/mnt/usb", true},
		{"/mnt/old", "/mnt/old/restic", "/mnt/new/restic", "/mnt/new", true},
		{"/mnt/old", "/mnt/old/restic", "sftp:nas:/b", "", true},
		{"/srv/backupdisk", "/srv/backupdisk/r", "/mnt/new/r", "/srv/backupdisk", false},
	}
	for _, tc := range follows {
		got, changed := backup.FollowGuard(tc.guard, tc.oldRepo, tc.newRepo)
		if got != tc.want || changed != tc.changed {
			t.Errorf("FollowGuard(%q, %s, %s) = %q, %v", tc.guard, tc.oldRepo, tc.newRepo, got, changed)
		}
	}
	mismatch := []struct {
		cfg  backup.Settings
		want bool
	}{
		{backup.Settings{Repo: "/mnt/usb/restic", RequireMount: "/mnt/usb"}, false},
		{backup.Settings{Repo: "/srv/restic", RequireMount: "/mnt/usb"}, true},
		{backup.Settings{Repo: "/mnt/usb2/restic", RequireMount: "/mnt/usb"}, true},
		{backup.Settings{Repo: "sftp:nas:/b", RequireMount: "/mnt/usb"}, false},
		{backup.Settings{Repo: "/srv/restic"}, false},
	}
	for _, tc := range mismatch {
		if got := backup.GuardMismatch(tc.cfg) != ""; got != tc.want {
			t.Errorf("GuardMismatch(%+v) = %v", tc.cfg, got)
		}
	}
}

// TestRequireMount asks the real filesystems: a guard on its own mounted filesystem passes; a guard
// that's just a folder on the stack's disk, or missing, refuses (with how to mount it).
func TestRequireMount(t *testing.T) {
	itest.NeedRoot(t)
	disk := filepath.Join(t.TempDir(), "disk")
	if err := os.Mkdir(disk, 0755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("mount", "-t", "tmpfs", "tmpfs", disk).CombinedOutput(); err != nil {
		t.Skipf("can't mount here: %v %s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("umount", disk).Run() })
	repo := itest.Repo(t)
	s, _ := newService(t, repo)
	cases := []struct {
		name      string
		guard     string
		wantErr   string
		onSystem  bool
		checkPath string
	}{
		{"no guard", "", "", true, repo.Root},
		{"a mounted disk", disk, "", false, filepath.Join(disk, "restic", "not-yet")},
		{"a folder on the system disk", t.TempDir(), "backup disk is NOT mounted", true, repo.Root},
		{"missing", "/mnt/" + itest.Name("gone"), "is missing (is the disk plugged in?)", true, "/mnt/" + itest.Name("gone") + "/r"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := backup.Settings{RequireMount: tc.guard}
			err := s.RequireMount(cfg)
			if (err == nil) != (tc.wantErr == "") || (err != nil && (!strings.Contains(err.Error(), tc.wantErr) || !strings.Contains(err.Error(), "Mount for backups"))) {
				t.Errorf("RequireMount = %v, want %q", err, tc.wantErr)
			}
			if missing := s.DiskMissing(cfg); (missing != "") != (tc.wantErr != "") || strings.Contains(missing, "\n") {
				t.Errorf("DiskMissing = %q", missing)
			}
			if on, _ := s.OnSystemDisk(tc.checkPath); on != tc.onSystem {
				t.Errorf("OnSystemDisk(%s) = %v, want %v", tc.checkPath, on, tc.onSystem)
			}
		})
	}
}
