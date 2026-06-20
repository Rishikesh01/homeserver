package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIsRemoteRepo(t *testing.T) {
	cases := map[string]bool{
		"/mnt/restic":                          false,
		"/home/rish/homeserver/backups/restic": false,
		"./backups/restic":                     false,
		"":                                     false,
		"sftp:user@home:/mnt/restic/restic":    true,
		"sftp:resticpush@cloudbox:/mnt/restic": true,
		"s3:s3.amazonaws.com/bucket":           true,
		"b2:my-bucket:path":                    true,
		"rest:https://host:8000/":              true,
		"rclone:remote:path":                   true,
	}
	for repo, want := range cases {
		if got := isRemoteRepo(repo); got != want {
			t.Errorf("isRemoteRepo(%q) = %v, want %v", repo, got, want)
		}
	}
}

func TestRequireBackupMount(t *testing.T) {
	// A remote repo has no local mount to guard — even with REQUIRE_MOUNT set to a path
	// that is not a mount, it must NOT error (the guard is for local HDDs only).
	if err := requireBackupMount(backupCfg{Repo: "sftp:u@h:/r", RequireMount: "/definitely/not/mounted"}); err != nil {
		t.Errorf("remote repo should bypass the mount guard, got: %v", err)
	}
	// No REQUIRE_MOUNT set => no check (the default local repo lives on the root disk).
	if err := requireBackupMount(backupCfg{Repo: "/mnt/restic", RequireMount: ""}); err != nil {
		t.Errorf("empty RequireMount should be a no-op, got: %v", err)
	}
	// A local repo whose REQUIRE_MOUNT path doesn't exist must fail closed.
	missing := filepath.Join(t.TempDir(), "nope-not-mounted")
	if err := requireBackupMount(backupCfg{Repo: "/mnt/restic", RequireMount: missing}); err == nil {
		t.Errorf("a missing mount path should error, got nil")
	}
}

func TestEnsureResticPasswordStrict(t *testing.T) {
	// Missing key on a LOCAL repo: must refuse and point at `backup init` (never mint).
	repo := t.TempDir()
	err := ensureResticPasswordStrict(repo, backupCfg{Repo: filepath.Join(repo, "backups", "restic")})
	if err == nil {
		t.Fatal("missing key on local repo should error, got nil")
	}
	if !strings.Contains(err.Error(), "backup init") {
		t.Errorf("local error should mention `backup init`, got: %v", err)
	}
	if fileExists(filepath.Join(repo, resticPassFile)) {
		t.Error("strict variant must NOT create .restic-password")
	}

	// Missing key on a REMOTE repo: must refuse with the can't-regenerate-a-remote-key warning.
	err = ensureResticPasswordStrict(repo, backupCfg{Repo: "sftp:u@home:/mnt/restic/restic"})
	if err == nil || !strings.Contains(err.Error(), "remote") {
		t.Errorf("missing key on remote repo should error mentioning 'remote', got: %v", err)
	}

	// Key present: must succeed without minting anything new.
	if err := os.WriteFile(filepath.Join(repo, resticPassFile), []byte("secret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := ensureResticPasswordStrict(repo, backupCfg{Repo: "/mnt/restic"}); err != nil {
		t.Errorf("present key should pass, got: %v", err)
	}
}

func TestAssertCaptureConsistent(t *testing.T) {
	// mk builds a staging dir: optionally with a staged Vaultwarden db.sqlite3, and with a
	// nextcloud-db.sql of ncSize bytes (ncSize < 0 = no dump file at all).
	mk := func(withVWDB bool, ncSize int) string {
		s := t.TempDir()
		if withVWDB {
			_ = os.MkdirAll(filepath.Join(s, "vaultwarden"), 0700)
			_ = os.WriteFile(filepath.Join(s, "vaultwarden", "db.sqlite3"), []byte("sqlite"), 0600)
		}
		if ncSize >= 0 {
			_ = os.WriteFile(filepath.Join(s, "nextcloud-db.sql"), make([]byte, ncSize), 0600)
		}
		return s
	}
	cases := []struct {
		name        string
		staging     string
		vwRunning   bool
		vwExclude   string
		ncDBRunning bool
		wantErr     bool
	}{
		{"VW running, staging failed (torn)", mk(false, -1), true, "", false, true},
		{"VW running, exclude set but db not staged", mk(false, -1), true, "/v/db.sqlite3*", false, true},
		{"VW running, db staged", mk(true, -1), true, "/v/db.sqlite3*", false, false},
		{"VW down, nothing staged", mk(false, -1), false, "", false, false},
		{"NC DB running, no dump", mk(false, -1), false, "", true, true},
		{"NC DB running, tiny dump", mk(false, 10), false, "", true, true},
		{"NC DB running, valid dump", mk(false, 128), false, "", true, false},
		{"NC DB down, no dump", mk(false, -1), false, "", false, false},
	}
	for _, c := range cases {
		err := assertCaptureConsistent(c.staging, c.vwRunning, c.vwExclude, c.ncDBRunning)
		if (err != nil) != c.wantErr {
			t.Errorf("%s: got err=%v, wantErr=%v", c.name, err, c.wantErr)
		}
	}
}
