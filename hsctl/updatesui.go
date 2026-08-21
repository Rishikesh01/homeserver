package main

import (
	"fmt"
	"net/http"
	"time"
)

// The Updates page: the CLI's update checker as a dashboard page. A check runs
// in-process (same code path as `hsctl updates`) and streams row-by-row into
// the page, then the parsed results render as a table with per-app buttons.
// Applying re-execs `hsctl updates --apply <name> --yes` exactly like the
// Command Center runs commands — and <name> must match a container from the
// last check (or "all"), so the browser still can't smuggle arbitrary args.

// updatesRow is one imageStatus prepared for the template.
type updatesRow struct {
	imageStatus
	Major bool // major upgrade — "all" skips it; per-app button warns hard
}

type updatesData struct {
	Checked  bool // a check has run since the dashboard started
	Age      string
	Rows     []updatesRow
	StaleN   int
	RoutineN int // what "apply all routine" would cover
}

// setUpdateStatuses caches check results for the page; nil clears the cache
// (after an apply, the last check no longer reflects reality).
func (s *uiServer) setUpdateStatuses(sts []imageStatus) {
	s.updMu.Lock()
	s.updSt, s.updAt = sts, time.Now()
	s.updMu.Unlock()
}

func (s *uiServer) updateStatuses() ([]imageStatus, time.Time) {
	s.updMu.Lock()
	defer s.updMu.Unlock()
	return s.updSt, s.updAt
}

func (s *uiServer) handleUpdatesPage(w http.ResponseWriter, r *http.Request) {
	sts, at := s.updateStatuses()
	d := updatesData{Checked: sts != nil}
	if d.Checked {
		d.Age = fmt.Sprintf("%d min ago", int(time.Since(at).Minutes()))
	}
	for _, st := range sts {
		row := updatesRow{imageStatus: st}
		if st.Newer != "" {
			row.Major = isMajorJump(imageTag(st.Image), st.Newer)
		}
		if st.Stale {
			d.StaleN++
			if !row.Major {
				d.RoutineN++
			}
		}
		d.Rows = append(d.Rows, row)
	}
	render(w, updatesTmpl, d)
}

// handleUpdatesCheck streams a live check into the page and caches the result.
func (s *uiServer) handleUpdatesCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	uiLog.Info("update check", "from", remoteIP(r))
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	fl, _ := w.(http.Flusher)
	fw := flushWriter{w: w, f: fl}
	fmt.Fprintf(fw, "checking each app against its registry — this takes a little while…\n\n")
	sts, err := checkAllUpdates(s.repo, func(st imageStatus) {
		fmt.Fprintf(fw, "%-16s %s\n", st.Container, st.State)
	})
	if err != nil {
		fmt.Fprintf(fw, "\ncheck failed: %v\n", err)
		return
	}
	s.setUpdateStatuses(sts)
	fmt.Fprintf(fw, "\n[done]\n")
}

// handleUpdatesApply applies one app's update (or "all" routine ones) by
// re-execing the CLI, streaming its output.
func (s *uiServer) handleUpdatesApply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	name := r.FormValue("name")
	valid := name == "all"
	sts, _ := s.updateStatuses()
	for _, st := range sts {
		if st.Container == name && st.Stale {
			valid = true
		}
	}
	if !valid {
		http.Error(w, "unknown app or nothing to apply — run a check first", http.StatusNotFound)
		return
	}
	// Applying pulls images and restarts services, so it must not overlap a
	// running backup or restore — same collision the stack actions guard against.
	if !s.opMu.TryLock() {
		http.Error(w, "a backup or restore is running — try again once it finishes", http.StatusConflict)
		return
	}
	defer s.opMu.Unlock()
	s.updMu.Lock()
	s.updSt, s.updAt = nil, time.Time{}
	s.updMu.Unlock()
	uiLog.Info("update apply", "name", name, "from", remoteIP(r))
	s.streamHsctl(w, "updates", "--apply", name, "--yes")
}
