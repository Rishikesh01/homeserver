package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Backup health, as the admin sees it. A nightly backup that fails is silent by nature — it
// runs at 03:30 under systemd — so every run records how it went (backups/.last-run), whatever
// started it and however early it failed (an unmounted disk, a missing password, a broken
// prune). The dashboard reads that record, and an optional NOTIFY_URL in backup.conf gets a
// message when a run fails.

// lastRunFile records the outcome of the most recent `backup run`, relative to the repo.
const lastRunFile = "backups/.last-run"

// lastRun is one recorded `backup run` outcome.
type lastRun struct {
	Time  time.Time `json:"time"`
	OK    bool      `json:"ok"`
	Error string    `json:"error,omitempty"`
}

// recordLastRun saves the outcome of a backup run and, when it failed, sends the optional
// notification. Best-effort: recording must never turn a good backup into a failed command.
func recordLastRun(repo string, runErr error) {
	r := lastRun{Time: time.Now(), OK: runErr == nil}
	if runErr != nil {
		r.Error = runErr.Error()
	}
	if b, err := json.Marshal(r); err == nil {
		_ = mkdirAllOwned(filepath.Join(repo, "backups"), 0755)
		_ = writeFile0644(filepath.Join(repo, lastRunFile), string(b)+"\n")
	}
	if runErr != nil {
		notifyFailure(loadBackupCfg(repo).NotifyURL, "Homeserver backup FAILED", runErr.Error())
	}
}

func readLastRun(repo string) (lastRun, bool) {
	b, err := os.ReadFile(filepath.Join(repo, lastRunFile))
	if err != nil {
		return lastRun{}, false
	}
	var r lastRun
	if json.Unmarshal(b, &r) != nil || r.Time.IsZero() {
		return lastRun{}, false
	}
	return r, true
}

// notifyFailure POSTs a plain-text message to url — the shape ntfy, Gotify-style relays and most
// webhook bridges accept (ntfy shows the Title header as the title). Opt-in: url empty does nothing.
func notifyFailure(url, title, body string) {
	if url == "" {
		return
	}
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		fmt.Fprintln(os.Stderr, "notify:", err)
		return
	}
	req.Header.Set("Title", title)
	req.Header.Set("Content-Type", "text/plain; charset=utf-8")
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintln(os.Stderr, "notify: couldn't send the failure notification:", err)
		return
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		fmt.Fprintf(os.Stderr, "notify: %s answered %s\n", url, resp.Status)
	}
}

// backupTimer is the nightly timer's state, for the Backups page.
type backupTimer struct {
	Installed bool
	Enabled   bool
	Next      string // when it runs next, "" if unknown
}

func backupTimerState() backupTimer {
	var t backupTimer
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
