package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Dashboard jobs. A dashboard button that changes something runs an hsctl command as a background
// job, not inside the HTTP request: its output goes to a log file, never down a pipe to the
// browser. When the output was piped into the response, closing the tab, a phone going to sleep,
// or Caddy restarting broke the pipe and killed the command at its next line of output — an
// aborted backup, a self-test that left its throwaway volumes behind, a password manager left
// stopped mid-backup. Now the job runs to the end regardless, and the page follows it by polling
// /admin/job, picking it up again after a reload.
//
// One job runs at a time, and the job itself takes the shared operation lock (oplock.go), so it
// can't overlap the backup timer or a terminal either. A job also outlives a dashboard restart: the
// unit uses KillMode=process, so restarting the dashboard leaves its jobs running (the new
// dashboard just can't show their output — the lock banner still says what's running).

// job is one dashboard-started command.
type job struct {
	ID      string
	Slug    string
	Title   string
	Started time.Time
	Ended   time.Time
	Done    bool
	OK      bool
	Err     string
	logPath string
}

// jobRunner tracks the running job and the recent ones.
type jobRunner struct {
	mu     sync.Mutex
	dir    string // where job logs go (created on first use)
	cur    *job   // the running job, if any
	recent map[string]*job
	order  []string // recent IDs, oldest first
	exe    string   // the binary to run; "" = this hsctl (tests substitute a script)
}

const jobsKept = 20

func newJobRunner() *jobRunner { return &jobRunner{recent: map[string]*job{}} }

// running returns the job in progress, or nil.
func (jr *jobRunner) running() *job {
	jr.mu.Lock()
	defer jr.mu.Unlock()
	if jr.cur != nil && !jr.cur.Done {
		j := *jr.cur
		return &j
	}
	return nil
}

func (jr *jobRunner) get(id string) (job, bool) {
	jr.mu.Lock()
	defer jr.mu.Unlock()
	j, ok := jr.recent[id]
	if !ok {
		return job{}, false
	}
	return *j, true
}

// start launches `hsctl <args>` in repo as a job titled title. It refuses while another job runs.
func (jr *jobRunner) start(repo, slug, title string, args []string) (job, error) {
	jr.mu.Lock()
	defer jr.mu.Unlock()
	if jr.cur != nil && !jr.cur.Done {
		return job{}, fmt.Errorf("%w: %s (started %s ago) — wait for it to finish",
			errBusy, jr.cur.Title, shortDuration(time.Since(jr.cur.Started)))
	}
	if jr.dir == "" {
		d, err := os.MkdirTemp("", "hsctl-jobs-")
		if err != nil {
			return job{}, err
		}
		jr.dir = d
	}
	exe := jr.exe
	if exe == "" {
		var err error
		if exe, err = os.Executable(); err != nil {
			return job{}, fmt.Errorf("cannot locate the hsctl binary: %w", err)
		}
	}
	j := &job{ID: strconv.FormatInt(time.Now().UnixNano(), 36), Slug: slug, Title: title, Started: time.Now()}
	j.logPath = filepath.Join(jr.dir, j.ID+".log")
	logf, err := os.OpenFile(j.logPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return job{}, err
	}
	fmt.Fprintf(logf, "$ hsctl %s\n\n", strings.Join(args, " "))

	cmd := exec.Command(exe, args...)
	cmd.Dir = repo
	cmd.Env = append(os.Environ(), "HOMESERVER_DIR="+repo)
	cmd.Stdout, cmd.Stderr = logf, logf // a file, not a pipe: nothing the browser does can break it
	if err := cmd.Start(); err != nil {
		logf.Close()
		return job{}, err
	}
	jr.cur = j
	jr.recent[j.ID] = j
	jr.order = append(jr.order, j.ID)
	for len(jr.order) > jobsKept {
		old := jr.order[0]
		jr.order = jr.order[1:]
		if o := jr.recent[old]; o != nil {
			_ = os.Remove(o.logPath)
		}
		delete(jr.recent, old)
	}
	uiLog.Info("job started", "slug", slug, "id", j.ID)

	go func() {
		werr := cmd.Wait()
		if werr != nil {
			fmt.Fprintf(logf, "\n[command exited with error: %v]\n", werr)
		} else {
			fmt.Fprintf(logf, "\n[done]\n")
		}
		logf.Close()
		jr.mu.Lock()
		j.Done, j.OK, j.Ended = true, werr == nil, time.Now()
		if werr != nil {
			j.Err = werr.Error()
		}
		jr.mu.Unlock()
		uiLog.Info("job finished", "slug", slug, "id", j.ID, "ok", werr == nil)
	}()
	return *j, nil
}

// shortDuration renders d as "45s" / "3m 05s" / "1h 02m".
func shortDuration(d time.Duration) string {
	d = d.Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm %02ds", int(d.Minutes()), int(d.Seconds())%60)
	default:
		return fmt.Sprintf("%dh %02dm", int(d.Hours()), int(d.Minutes())%60)
	}
}

// jobStatus is the JSON the page polls: the job's state plus its output from byte offset From.
type jobStatus struct {
	ID      string `json:"id"`
	Slug    string `json:"slug"`
	Title   string `json:"title"`
	Done    bool   `json:"done"`
	OK      bool   `json:"ok"`
	Error   string `json:"error,omitempty"`
	Out     string `json:"out"`
	Next    int64  `json:"next"`
	Elapsed string `json:"elapsed"`
}

// maxJobChunk caps the output returned per poll, so a huge log streams in pieces.
const maxJobChunk = 256 << 10

func (j job) status(from int64) jobStatus {
	st := jobStatus{ID: j.ID, Slug: j.Slug, Title: j.Title, Done: j.Done, OK: j.OK, Error: j.Err, Next: from}
	end := time.Now()
	if j.Done {
		end = j.Ended
	}
	st.Elapsed = shortDuration(end.Sub(j.Started))
	if f, err := os.Open(j.logPath); err == nil {
		defer f.Close()
		if _, err := f.Seek(from, io.SeekStart); err == nil {
			b, _ := io.ReadAll(io.LimitReader(f, maxJobChunk))
			st.Out = string(b)
			st.Next = from + int64(len(b))
		}
	}
	// Not done until the reader has caught up with the log, so the page shows the last lines.
	if fi, err := os.Stat(j.logPath); err == nil && st.Next < fi.Size() {
		st.Done = false
	}
	return st
}

// jobs returns the dashboard's job runner, creating it on first use.
func (s *uiServer) jobs() *jobRunner {
	s.jobsOnce.Do(func() {
		if s.jobRun == nil {
			s.jobRun = newJobRunner()
		}
	})
	return s.jobRun
}

// startJob starts a dashboard job and answers with its ID as JSON ({"id": …}) for the page to
// follow, or with 409 when another operation — a dashboard job, the backup timer, a terminal —
// is already running (the job would only fail on the shared lock a moment later).
func (s *uiServer) startJob(w http.ResponseWriter, r *http.Request, slug, title string, args []string) (job, bool) {
	if holder := opLockBusy(s.repo); holder != "" {
		http.Error(w, "Another hsctl operation is running: "+holder+" — try again once it finishes.", http.StatusConflict)
		return job{}, false
	}
	j, err := s.jobs().start(s.repo, slug, title, args)
	if err != nil {
		code := http.StatusInternalServerError
		if errors.Is(err, errBusy) {
			code = http.StatusConflict
		}
		http.Error(w, err.Error(), code)
		return job{}, false
	}
	uiLog.Info("job requested", "slug", slug, "job", j.ID, "from", remoteIP(r))
	return j, true
}

// handleJob reports a job's progress: GET /admin/job?id=<id>&from=<offset>.
func (s *uiServer) handleJob(w http.ResponseWriter, r *http.Request) {
	j, ok := s.jobs().get(r.URL.Query().Get("id"))
	if !ok {
		http.Error(w, "no such job (the dashboard may have restarted)", http.StatusNotFound)
		return
	}
	from, _ := strconv.ParseInt(r.URL.Query().Get("from"), 10, 64)
	if from < 0 {
		from = 0
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(j.status(from))
}

// busyNote describes what's running right now — a dashboard job, or any other hsctl operation
// holding the shared lock (the backup timer, a terminal) — for the banner on every admin page,
// plus the ID of the dashboard job (if it is one) so the page can follow its output.
func (s *uiServer) busyNote() (note, jobID string) {
	if j := s.jobs().running(); j != nil {
		return fmt.Sprintf("%s — running for %s", j.Title, shortDuration(time.Since(j.Started))), j.ID
	}
	if holder := opLockBusy(s.repo); holder != "" {
		return holder, ""
	}
	return "", ""
}
