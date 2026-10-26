//go:build integration

package backup_test

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Rishikesh01/homeserver/hsctl/internal/backup"
	"github.com/Rishikesh01/homeserver/hsctl/internal/itest"
	"github.com/Rishikesh01/homeserver/hsctl/internal/paths"
)

// TestFreshnessAt: how old the last backup is, and when that's overdue (36 hours: a missed night).
func TestFreshnessAt(t *testing.T) {
	now := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name  string
		ago   time.Duration
		known bool
		age   string
		stale bool
	}{
		{"never", 0, false, "", false},
		{"two hours", 2 * time.Hour, true, "2 hours ago", false},
		{"nine days", 9 * 24 * time.Hour, true, "9 days ago", true},
		{"at the boundary", backup.StaleAfter, true, "36 hours ago", false},
		{"just past it", backup.StaleAfter + time.Minute, true, "36 hours ago", true},
	}
	for _, tc := range cases {
		f := backup.FreshnessAt(now.Add(-tc.ago), tc.known, now)
		if f.Known != tc.known || f.Age != tc.age || f.Stale != tc.stale {
			t.Errorf("%s: %+v", tc.name, f)
		}
	}
	ages := map[time.Duration]string{30 * time.Minute: "less than an hour ago", 90 * time.Minute: "1 hour ago",
		5 * time.Hour: "5 hours ago", 47 * time.Hour: "47 hours ago", 3 * 24 * time.Hour: "3 days ago"}
	for d, want := range ages {
		if got := backup.HumanAge(d); got != want {
			t.Errorf("HumanAge(%v) = %q, want %q", d, got, want)
		}
	}
	if backup.FirstLine("disk not mounted\nmore") != "disk not mounted" {
		t.Error("FirstLine")
	}
}

// TestRecordRun: every run's outcome is recorded — a failure after the last good backup is what the
// dashboard shows, an older one isn't — and a failure is posted to the notification URL.
func TestRecordRun(t *testing.T) {
	type post struct{ title, body string }
	posts := make(chan post, 10)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		posts <- post{r.Header.Get("Title"), string(b)}
	}))
	defer srv.Close()
	cases := []struct {
		name      string
		good      time.Duration // how long ago the last good backup was (0 = never)
		runErr    error
		notify    bool
		wantFail  bool
		wantKnown bool
		wantPost  bool
	}{
		{"never backed up, never tried", 0, nil, false, false, false, false},
		{"good run", 3 * time.Hour, nil, true, false, true, false},
		{"failed after a good one", 30 * time.Hour, errors.New("disk not mounted\nmore"), true, true, true, true},
		{"failed, never succeeded, no URL", 0, errors.New("disk not mounted"), false, true, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := itest.Repo(t)
			if tc.good > 0 {
				itest.WriteFile(t, repo.Root, paths.LastBackup, time.Now().Add(-tc.good).Format(time.RFC3339)+"\n")
			}
			if tc.notify {
				itest.WriteFile(t, repo.Root, paths.BackupConf, "NOTIFY_URL="+srv.URL+"\n")
			}
			s, _ := newService(t, repo)
			if tc.runErr != nil || tc.notify {
				s.RecordRun(tc.runErr)
			}
			f := s.Freshness()
			if f.Failed != tc.wantFail || f.Known != tc.wantKnown || (f.Failed && (f.FailedErr != "disk not mounted" || f.FailedAge == "")) {
				t.Errorf("freshness %+v", f)
			}
			select {
			case p := <-posts:
				if !tc.wantPost || p.title != "Homeserver backup FAILED" || !strings.HasPrefix(p.body, "disk not mounted") {
					t.Errorf("posted %+v", p)
				}
			case <-time.After(200 * time.Millisecond):
				if tc.wantPost {
					t.Error("no notification")
				}
			}
		})
	}
	t.Run("corrupt stamps read as unknown", func(t *testing.T) {
		repo := itest.Repo(t)
		itest.WriteFile(t, repo.Root, paths.LastBackup, "garbage\n")
		itest.WriteFile(t, repo.Root, paths.LastRun, "{not json")
		itest.WriteFile(t, repo.Root, paths.LastReplica, "garbage\n")
		s, _ := newService(t, repo)
		if f := s.Freshness(); f.Known || f.Failed || s.ReplicaAge() != "" {
			t.Errorf("%+v %q", f, s.ReplicaAge())
		}
	})
}

// TestTimer reads the nightly timer's state from systemd.
func TestTimer(t *testing.T) {
	cases := []struct {
		enabled string
		want    backup.Timer
	}{
		{"not-found", backup.Timer{}},
		{"disabled", backup.Timer{Installed: true, Next: "Mon 2026-10-26 03:30:00 UTC"}},
		{"enabled", backup.Timer{Installed: true, Enabled: true, Next: "Mon 2026-10-26 03:30:00 UTC"}},
	}
	for _, tc := range cases {
		sc := itest.FakeSystemctl(t)
		sc.SetEnabled(tc.enabled)
		s, _ := newService(t, itest.Repo(t))
		s.TimerState = backup.SystemdTimer
		if got := s.Timer(); got != tc.want {
			t.Errorf("%s: Timer = %+v, want %+v", tc.enabled, got, tc.want)
		}
	}
}
