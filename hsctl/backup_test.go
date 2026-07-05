package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestPrepareRestoreTarget proves the fix for the restore-contamination bug: the reused restore
// target must be emptied before `restic restore`, so leftovers from a prior extraction can't be
// copied into the live volumes. It also must refuse to wipe a dangerous (system) path.
func TestPrepareRestoreTarget(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "restore")

	// Simulate a previous restore leaving a whole volume tree behind.
	stale := filepath.Join(target, "var", "lib", "docker", "volumes", "old_vol", "_data")
	if err := os.MkdirAll(stale, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stale, "stale.txt"), []byte("leftover"), 0600); err != nil {
		t.Fatal(err)
	}

	if err := prepareRestoreTarget(target); err != nil {
		t.Fatalf("prepareRestoreTarget: %v", err)
	}
	if !fileExists(target) {
		t.Fatal("target directory should exist after prepare")
	}
	entries, err := os.ReadDir(target)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("target must be empty after prepare, has %d entries (stale data would contaminate the restore)", len(entries))
	}

	// Guard: never RemoveAll a blank or system path.
	for _, bad := range []string{"", ".", "/"} {
		if err := prepareRestoreTarget(bad); err == nil {
			t.Errorf("prepareRestoreTarget(%q) should be refused as unsafe", bad)
		}
	}
}
