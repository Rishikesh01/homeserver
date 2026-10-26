// Package jobs runs dashboard-started hsctl commands as background jobs. A dashboard button that
// changes something runs as a job, not inside the HTTP request: its output goes to a log file,
// never down a pipe to the browser. When the output was piped into the response, closing the tab,
// a phone going to sleep, or Caddy restarting broke the pipe and killed the command at its next
// line of output — an aborted backup, a self-test that left its throwaway volumes behind, a
// password manager left stopped mid-backup. Now the job runs to the end regardless, and the page
// follows it by polling, picking it up again after a reload.
//
// One job runs at a time, and the job itself takes the shared operation lock (package oplock), so
// it can't overlap the backup timer or a terminal either. A job also outlives a dashboard restart:
// the unit uses KillMode=process, so restarting the dashboard leaves its jobs running (the new
// dashboard just can't show their output — the lock banner still says what's running).
package jobs

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ErrBusy is returned (wrapped) when a job is already running.
var ErrBusy = errors.New("another job is running")

// Job is one dashboard-started command.
type Job struct {
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

// Runner tracks the running job and the recent ones.
type Runner struct {
	// Root is the repo the jobs run in (their working directory and HOMESERVER_DIR).
	Root string
	// Exe is the binary to run: this hsctl.
	Exe string
	// Dir is where job logs go; a temp dir is made on first use when empty.
	Dir string
	// Log gets a line when a job starts and ends (nil: discarded).
	Log *slog.Logger

	mu     sync.Mutex
	cur    *Job // the running job, if any
	recent map[string]*Job
	order  []string // recent IDs, oldest first
}

// Kept is how many finished jobs (and their logs) are remembered.
const Kept = 20

// New returns a runner that runs exe in root.
func New(root, exe string, log *slog.Logger) *Runner {
	return &Runner{Root: root, Exe: exe, Log: log}
}

// Running returns the job in progress, or nil.
func (r *Runner) Running() *Job {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cur != nil && !r.cur.Done {
		j := *r.cur
		return &j
	}
	return nil
}

// Get returns a recent job by ID.
func (r *Runner) Get(id string) (Job, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	j, ok := r.recent[id]
	if !ok {
		return Job{}, false
	}
	return *j, true
}

func (r *Runner) logf(msg string, args ...any) {
	if r.Log != nil {
		r.Log.Info(msg, args...)
	}
}

// Start launches `hsctl <args>` as a job titled title. It refuses while another job runs.
func (r *Runner) Start(slug, title string, args []string) (Job, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cur != nil && !r.cur.Done {
		return Job{}, fmt.Errorf("%w: %s (started %s ago) — wait for it to finish",
			ErrBusy, r.cur.Title, ShortDuration(time.Since(r.cur.Started)))
	}
	if r.recent == nil {
		r.recent = map[string]*Job{}
	}
	if r.Dir == "" {
		d, err := os.MkdirTemp("", "hsctl-jobs-")
		if err != nil {
			return Job{}, err
		}
		r.Dir = d
	}
	j := &Job{ID: strconv.FormatInt(time.Now().UnixNano(), 36), Slug: slug, Title: title, Started: time.Now()}
	j.logPath = filepath.Join(r.Dir, j.ID+".log")
	logf, err := os.OpenFile(j.logPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return Job{}, err
	}
	fmt.Fprintf(logf, "$ hsctl %s\n\n", strings.Join(args, " "))

	cmd := exec.Command(r.Exe, args...)
	cmd.Dir = r.Root
	cmd.Env = append(os.Environ(), "HOMESERVER_DIR="+r.Root, "HSCTL_FROM_DASHBOARD=1")
	cmd.Stdout, cmd.Stderr = logf, logf // a file, not a pipe: nothing the browser does can break it
	if err := cmd.Start(); err != nil {
		logf.Close()
		return Job{}, err
	}
	r.cur = j
	r.recent[j.ID] = j
	r.order = append(r.order, j.ID)
	for len(r.order) > Kept {
		old := r.order[0]
		r.order = r.order[1:]
		if o := r.recent[old]; o != nil {
			_ = os.Remove(o.logPath)
		}
		delete(r.recent, old)
	}
	r.logf("job started", "slug", slug, "id", j.ID)

	go func() {
		werr := cmd.Wait()
		if werr != nil {
			fmt.Fprintf(logf, "\n[command exited with error: %v]\n", werr)
		} else {
			fmt.Fprintf(logf, "\n[done]\n")
		}
		logf.Close()
		r.mu.Lock()
		j.Done, j.OK, j.Ended = true, werr == nil, time.Now()
		if werr != nil {
			j.Err = werr.Error()
		}
		r.mu.Unlock()
		r.logf("job finished", "slug", slug, "id", j.ID, "ok", werr == nil)
	}()
	return *j, nil
}

// ShortDuration renders d as "45s" / "3m 05s" / "1h 02m".
func ShortDuration(d time.Duration) string {
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

// Status is a job's state plus its output from a byte offset — what the page polls.
type Status struct {
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

// MaxChunk caps the output returned per poll, so a huge log streams in pieces.
const MaxChunk = 256 << 10

// Status reports the job with its output from byte offset from.
func (j Job) Status(from int64) Status {
	st := Status{ID: j.ID, Slug: j.Slug, Title: j.Title, Done: j.Done, OK: j.OK, Error: j.Err, Next: from}
	end := time.Now()
	if j.Done {
		end = j.Ended
	}
	st.Elapsed = ShortDuration(end.Sub(j.Started))
	if f, err := os.Open(j.logPath); err == nil {
		defer f.Close()
		if _, err := f.Seek(from, io.SeekStart); err == nil {
			b, _ := io.ReadAll(io.LimitReader(f, MaxChunk))
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
