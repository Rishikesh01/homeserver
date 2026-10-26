package backup

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/Rishikesh01/homeserver/hsctl/internal/fsutil"
	"github.com/Rishikesh01/homeserver/hsctl/internal/paths"
)

// Backup health, as the admin sees it. A nightly backup that fails is silent by nature — it runs
// at 03:30 under systemd — so every run records how it went (backups/.last-run), whatever started
// it and however early it failed (an unmounted disk, a missing password, a broken prune). The
// dashboard reads that record, and an optional NOTIFY_URL in backup.conf gets a message when a run
// fails. The last successful snapshot is stamped separately (backups/.last-backup), so the
// dashboard can warn about staleness without opening the restic repo — which may live on a
// spun-down or unmounted disk — on every page load.

// LastRun is one recorded `backup run` outcome.
type LastRun struct {
	Time  time.Time `json:"time"`
	OK    bool      `json:"ok"`
	Error string    `json:"error,omitempty"`
}

// RecordRun saves the outcome of a backup run and, when it failed, sends the optional notification.
// Best-effort: recording must never turn a good backup into a failed command.
func (s *Service) RecordRun(runErr error) {
	r := LastRun{Time: time.Now(), OK: runErr == nil}
	if runErr != nil {
		r.Error = runErr.Error()
	}
	if b, err := json.Marshal(r); err == nil {
		_ = fsutil.MkdirAllOwned(s.Repo.Path(paths.Backups), 0755)
		_ = fsutil.WritePublic(s.Repo.Path(paths.LastRun), string(b)+"\n")
	}
	if runErr != nil {
		s.notify(s.Settings().NotifyURL, "Homeserver backup FAILED", runErr.Error())
	}
}

func (s *Service) lastRun() (LastRun, bool) {
	b, err := os.ReadFile(s.Repo.Path(paths.LastRun))
	if err != nil {
		return LastRun{}, false
	}
	var r LastRun
	if json.Unmarshal(b, &r) != nil || r.Time.IsZero() {
		return LastRun{}, false
	}
	return r, true
}

// notify POSTs a plain-text message to url — the shape ntfy, Gotify-style relays and most webhook
// bridges accept (ntfy shows the Title header as the title). Opt-in: url empty does nothing.
func (s *Service) notify(url, title, body string) {
	if url == "" {
		return
	}
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		fmt.Fprintln(s.Err, "notify:", err)
		return
	}
	req.Header.Set("Title", title)
	req.Header.Set("Content-Type", "text/plain; charset=utf-8")
	resp, err := s.HTTP.Do(req)
	if err != nil {
		fmt.Fprintln(s.Err, "notify: couldn't send the failure notification:", err)
		return
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		fmt.Fprintf(s.Err, "notify: %s answered %s\n", url, resp.Status)
	}
}

// Timer is the nightly timer's state, for the Backups page.
type Timer struct {
	Installed bool
	Enabled   bool
	Next      string // when it runs next, "" if unknown
}

// Timer is the nightly backup timer's state.
func (s *Service) Timer() Timer { return s.TimerState() }

// SystemdTimer reads hsctl-backup.timer's state from systemd.
func SystemdTimer() Timer {
	var t Timer
	out, err := exec.Command("systemctl", "is-enabled", "hsctl-backup.timer").Output()
	state := strings.TrimSpace(string(out))
	if state == "" || state == "not-found" || (err != nil && state != "disabled") {
		return t
	}
	t.Installed = true
	t.Enabled = state == "enabled"
	if out, err := exec.Command("systemctl", "show", "hsctl-backup.timer",
		"-p", "NextElapseUSecRealtime", "--value").Output(); err == nil {
		t.Next = strings.TrimSpace(string(out))
	}
	return t
}

// Freshness is the dashboard's view of how recent the last backup is.
type Freshness struct {
	Known bool   // a stamp exists
	Age   string // "3 days ago"
	Stale bool   // older than StaleAfter — time to warn
	// The most recent run, when it FAILED after the last good one (a run that refused to start —
	// disk not mounted, say — counts).
	Failed    bool
	FailedAge string
	FailedErr string
}

// StaleAfter is when the dashboard starts flagging the backup as overdue. Backups run nightly, so
// a day and a half without one means at least one night was missed.
const StaleAfter = 36 * time.Hour

// Freshness reports how recent the last backup is, and the last run if it failed since.
func (s *Service) Freshness() Freshness {
	t, ok := s.stampTime(paths.LastBackup)
	f := FreshnessAt(t, ok, time.Now())
	if r, ok := s.lastRun(); ok && !r.OK && r.Time.After(t) {
		f.Failed, f.FailedAge, f.FailedErr = true, HumanAge(time.Since(r.Time)), FirstLine(r.Error)
	}
	return f
}

// FreshnessAt is the pure core of Freshness.
func FreshnessAt(t time.Time, known bool, now time.Time) Freshness {
	if !known {
		return Freshness{}
	}
	d := now.Sub(t)
	return Freshness{Known: true, Age: HumanAge(d), Stale: d > StaleAfter}
}

// FirstLine returns s up to its first newline (a long error's headline).
func FirstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return line
}

// HumanAge renders a duration as "less than an hour ago" / "5 hours ago" / "3 days ago".
func HumanAge(d time.Duration) string {
	switch h := int(d.Hours()); {
	case h < 1:
		return "less than an hour ago"
	case h == 1:
		return "1 hour ago"
	case h < 48:
		return fmt.Sprintf("%d hours ago", h)
	default:
		return fmt.Sprintf("%d days ago", h/24)
	}
}

// stamp writes the current time into a repo-relative stamp file.
func (s *Service) stamp(rel string) {
	path := s.Repo.Path(rel)
	_ = fsutil.MkdirAllOwned(s.Repo.Path(paths.Backups), 0755)
	_ = fsutil.WritePublic(path, time.Now().Format(time.RFC3339)+"\n")
}

func (s *Service) stampTime(rel string) (time.Time, bool) {
	b, err := os.ReadFile(s.Repo.Path(rel))
	if err != nil {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(string(b)))
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// ReplicaAge says when the off-site replica last got a copy, "" if never.
func (s *Service) ReplicaAge() string {
	t, ok := s.stampTime(paths.LastReplica)
	if !ok {
		return ""
	}
	return HumanAge(time.Since(t))
}
