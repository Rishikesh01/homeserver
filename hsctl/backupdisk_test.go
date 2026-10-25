package main

import "testing"

// TestGuardFor: a local destination on an external-disk mountpoint gets that mountpoint as its
// guard; anything else gets none.
func TestGuardFor(t *testing.T) {
	cases := []struct{ repo, want string }{
		{"/mnt/backup/restic", "/mnt/backup"},
		{"/mnt/restic", "/mnt/restic"},
		{"/media/alice/disk/restic", "/media/alice/disk"},
		{"/run/media/alice/disk/r", "/run/media/alice/disk"},
		{"/home/alice/homeserver/backups/restic", ""},
		{"sftp:user@nas:/backups", ""},
		{"/mnt", ""},
	}
	for _, tc := range cases {
		t.Run(tc.repo, func(t *testing.T) {
			if got := guardFor(tc.repo); got != tc.want {
				t.Errorf("guardFor = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestFollowGuard: a guard derived from the old destination follows the new one; a guard the
// admin chose stays put.
func TestFollowGuard(t *testing.T) {
	cases := []struct {
		name, guard, oldRepo, newRepo, want string
		changed                             bool
	}{
		{"none yet, external disk", "", "/srv/x", "/mnt/usb/restic", "/mnt/usb", true},
		{"derived guard follows", "/mnt/old", "/mnt/old/restic", "/mnt/new/restic", "/mnt/new", true},
		{"derived guard dropped for sftp", "/mnt/old", "/mnt/old/restic", "sftp:nas:/b", "", true},
		{"admin's own guard stays", "/srv/backupdisk", "/srv/backupdisk/r", "/mnt/new/r", "/srv/backupdisk", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, changed := followGuard(tc.guard, tc.oldRepo, tc.newRepo)
			if got != tc.want || changed != tc.changed {
				t.Errorf("= %q, %v; want %q, %v", got, changed, tc.want, tc.changed)
			}
		})
	}
}

// TestRequireBackupMount: a guarded path on the system disk is refused, a missing one too.
func TestRequireBackupMount(t *testing.T) {
	repo := t.TempDir()
	cases := []struct {
		name  string
		guard string
		ok    bool
	}{
		{"no guard", "", true},
		{"guard on the same disk as the repo", repo, false},
		{"guard path missing", repo + "/nope", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := requireBackupMount(repo, backupCfg{RequireMount: tc.guard})
			if (err == nil) != tc.ok {
				t.Errorf("err = %v, want ok=%v", err, tc.ok)
			}
		})
	}
}
