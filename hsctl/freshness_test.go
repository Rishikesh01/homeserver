package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFreshness(t *testing.T) {
	now := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	if f := freshness(time.Time{}, false, now); f.Known || f.Stale {
		t.Errorf("no stamp should be unknown and not stale: %+v", f)
	}
	f := freshness(now.Add(-2*time.Hour), true, now)
	if !f.Known || f.Stale || f.Age != "2 hours ago" {
		t.Errorf("fresh backup: %+v", f)
	}
	f = freshness(now.Add(-9*24*time.Hour), true, now)
	if !f.Stale || f.Age != "9 days ago" {
		t.Errorf("stale backup: %+v", f)
	}
	// Exactly at the boundary is not yet stale; just past it is.
	if f := freshness(now.Add(-backupStaleAfter), true, now); f.Stale {
		t.Errorf("boundary should not be stale: %+v", f)
	}
	if f := freshness(now.Add(-backupStaleAfter-time.Minute), true, now); !f.Stale {
		t.Errorf("past boundary should be stale: %+v", f)
	}
}

func TestHumanAge(t *testing.T) {
	for _, c := range []struct {
		d    time.Duration
		want string
	}{
		{30 * time.Minute, "less than an hour ago"},
		{90 * time.Minute, "1 hour ago"},
		{5 * time.Hour, "5 hours ago"},
		{47 * time.Hour, "47 hours ago"},
		{3 * 24 * time.Hour, "3 days ago"},
	} {
		if got := humanAge(c.d); got != c.want {
			t.Errorf("humanAge(%v) = %q, want %q", c.d, got, c.want)
		}
	}
}

func TestLastBackupStampRoundTrip(t *testing.T) {
	repo := t.TempDir()
	if _, ok := lastBackupTime(repo); ok {
		t.Fatal("fresh repo should have no stamp")
	}
	stampLastBackup(repo)
	got, ok := lastBackupTime(repo)
	if !ok {
		t.Fatal("stamp not readable after write")
	}
	if d := time.Since(got); d < 0 || d > time.Minute {
		t.Errorf("stamp time implausible: %v (%v old)", got, d)
	}
	// A corrupt stamp reads as "no backup recorded", not a crash.
	if err := os.WriteFile(filepath.Join(repo, lastBackupFile), []byte("garbage\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, ok := lastBackupTime(repo); ok {
		t.Error("corrupt stamp should be treated as unknown")
	}
}

func TestStatDiskSpace(t *testing.T) {
	ds := statDiskSpace("/")
	if !ds.OK || ds.Pct < 0 || ds.Pct > 100 || ds.Used == "" || ds.Total == "" {
		t.Errorf("implausible root disk space: %+v", ds)
	}
	if ds := statDiskSpace("/nonexistent-path-xyz"); ds.OK {
		t.Errorf("missing path should not be OK: %+v", ds)
	}
}
