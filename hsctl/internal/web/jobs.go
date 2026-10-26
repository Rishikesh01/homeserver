package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/Rishikesh01/homeserver/hsctl/internal/jobs"
	"github.com/Rishikesh01/homeserver/hsctl/internal/oplock"
)

// startJob starts a dashboard job and answers with its ID as JSON ({"id": …}) for the page to
// follow, or with 409 when another operation — a dashboard job, the backup timer, a terminal —
// is already running (the job would only fail on the shared lock a moment later).
func (s *Server) startJob(w http.ResponseWriter, r *http.Request, slug, title string, args []string) (jobs.Job, bool) {
	if holder := s.Lock.Busy(); holder != "" {
		http.Error(w, "Another hsctl operation is running: "+holder+" — try again once it finishes.", http.StatusConflict)
		return jobs.Job{}, false
	}
	j, err := s.Jobs.Start(slug, title, args)
	if err != nil {
		code := http.StatusInternalServerError
		if errors.Is(err, oplock.ErrBusy) {
			code = http.StatusConflict
		}
		http.Error(w, err.Error(), code)
		return jobs.Job{}, false
	}
	s.Log.Info("job requested", "slug", slug, "job", j.ID, "from", remoteIP(r))
	return j, true
}

// handleJob reports a job's progress: GET /admin/job?id=<id>&from=<offset>.
func (s *Server) handleJob(w http.ResponseWriter, r *http.Request) {
	j, ok := s.Jobs.Get(r.URL.Query().Get("id"))
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
	_ = json.NewEncoder(w).Encode(j.Status(from))
}

// busyNote describes what's running right now — a dashboard job, or any other hsctl operation
// holding the shared lock (the backup timer, a terminal) — for the banner on every admin page,
// plus the ID of the dashboard job (if it is one) so the page can follow its output.
func (s *Server) busyNote() (note, jobID string) {
	if j := s.Jobs.Running(); j != nil {
		return fmt.Sprintf("%s — running for %s", j.Title, jobs.ShortDuration(time.Since(j.Started))), j.ID
	}
	if holder := s.Lock.Busy(); holder != "" {
		return holder, ""
	}
	return "", ""
}
