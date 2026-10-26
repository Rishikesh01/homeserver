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

// TestBackupFreshnessForFailedRun: a failed run after the last good backup — including one that
// never got as far as restic — is what the dashboard shows; an older failure is not.
func TestBackupFreshnessForFailedRun(t *testing.T) {
	cases := []struct {
		name      string
		good      time.Duration // how long ago the last good backup was (0 = never)
		failed    time.Duration // how long ago the last run failed (0 = no failed run)
		wantFail  bool
		wantKnown bool
	}{
		{"never backed up, never tried", 0, 0, false, false},
		{"good last night", 3 * time.Hour, 0, false, true},
		{"failed after a good one", 30 * time.Hour, 2 * time.Hour, true, true},
		{"failure before the last good one", 2 * time.Hour, 30 * time.Hour, false, true},
		{"failed, never succeeded", 0, time.Hour, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := t.TempDir()
			if err := os.MkdirAll(filepath.Join(repo, "backups"), 0755); err != nil {
				t.Fatal(err)
			}
			if tc.good > 0 {
				stamp := time.Now().Add(-tc.good).Format(time.RFC3339)
				if err := os.WriteFile(filepath.Join(repo, lastBackupFile), []byte(stamp+"\n"), 0644); err != nil {
					t.Fatal(err)
				}
			}
			if tc.failed > 0 {
				rec := `{"time":"` + time.Now().Add(-tc.failed).Format(time.RFC3339) + `","ok":false,"error":"disk not mounted\nmore"}`
				if err := os.WriteFile(filepath.Join(repo, lastRunFile), []byte(rec), 0644); err != nil {
					t.Fatal(err)
				}
			}
			f := backupFreshnessFor(repo)
			if f.Failed != tc.wantFail || f.Known != tc.wantKnown {
				t.Errorf("got %+v, want failed=%v known=%v", f, tc.wantFail, tc.wantKnown)
			}
			if f.Failed && f.FailedErr != "disk not mounted" {
				t.Errorf("FailedErr = %q, want the error's first line", f.FailedErr)
			}
		})
	}
}
