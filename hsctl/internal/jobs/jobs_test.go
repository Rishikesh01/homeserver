//go:build integration

package jobs_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Rishikesh01/homeserver/hsctl/internal/itest"
	"github.com/Rishikesh01/homeserver/hsctl/internal/jobs"
	"github.com/Rishikesh01/homeserver/hsctl/internal/oplock"
)

// wait polls a job until it's done (and its log fully read), returning its whole output.
func wait(t *testing.T, r *jobs.Runner, id string) jobs.Status {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		j, ok := r.Get(id)
		if !ok {
			t.Fatalf("job %s vanished", id)
		}
		if st := j.Status(0); st.Done {
			return st
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("job %s didn't finish", id)
	return jobs.Status{}
}

// TestRun runs the real hsctl as dashboard jobs: the whole output lands in the job's log, the exit
// status is reported, and the job runs in the repo with HOMESERVER_DIR and HSCTL_FROM_DASHBOARD set.
func TestRun(t *testing.T) {
	bin := itest.Hsctl(t)
	cases := []struct {
		name    string
		args    []string
		wantOK  bool
		wantOut []string
	}{
		{"success", []string{"--version"}, true, []string{"$ hsctl --version", "hsctl version", "[done]"}},
		{"failure", []string{"apps", "enable", "nosuch"}, false,
			[]string{`unknown app "nosuch"`, "[command exited with error: exit status 1]"}},
		{"runs in the repo, as the dashboard", []string{"secrets", "set-ui-password", "dashboard-pw-123"}, true,
			[]string{"password dashboard-pw-123", "takes effect when the dashboard restarts"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := itest.Repo(t)
			r := jobs.New(repo.Root, bin, nil)
			r.Dir = t.TempDir()
			j, err := r.Start("slug", "Title", tc.args)
			if err != nil {
				t.Fatal(err)
			}
			st := wait(t, r, j.ID)
			if st.OK != tc.wantOK {
				t.Errorf("ok = %v, want %v\n%s", st.OK, tc.wantOK, st.Out)
			}
			for _, w := range tc.wantOut {
				if !strings.Contains(st.Out, w) {
					t.Errorf("output lacks %q:\n%s", w, st.Out)
				}
			}
			if st.Slug != "slug" || st.Title != "Title" || st.Elapsed == "" {
				t.Errorf("status = %+v", st)
			}
		})
	}
}

// TestOneAtATime: while a job runs — here a backup waiting on the operation lock — starting another
// is refused with ErrBusy, its output can be followed from an offset, and once it ends the next job
// may start.
func TestOneAtATime(t *testing.T) {
	itest.NeedRoot(t) // `backup run` refuses non-root before it ever waits
	bin := itest.Hsctl(t)
	repo := itest.Repo(t)
	held, err := oplock.New(repo.Root).Acquire("test holder", 0)
	if err != nil {
		t.Fatal(err)
	}
	r := jobs.New(repo.Root, bin, nil)
	j, err := r.Start("backup-run", "Back up now", []string{"backup", "run", "--wait", "3s"})
	if err != nil {
		t.Fatal(err)
	}
	steps := []struct {
		name  string
		check func() error
	}{
		{"a second job is refused", func() error {
			if _, err := r.Start("other", "Other", []string{"--version"}); !errors.Is(err, jobs.ErrBusy) {
				return fmt.Errorf("second start: %v, want ErrBusy", err)
			}
			return nil
		}},
		{"the running job is reported", func() error {
			if run := r.Running(); run == nil || run.ID != j.ID {
				return fmt.Errorf("Running = %v", run)
			}
			return nil
		}},
		{"it gives up on the lock, naming the holder", func() error {
			st := wait(t, r, j.ID)
			held.Release()
			if st.OK || !strings.Contains(st.Out, "waiting for another hsctl operation") || !strings.Contains(st.Out, "error: another hsctl operation is running: test holder") {
				return fmt.Errorf("ok=%v:\n%s", st.OK, st.Out)
			}
			return nil
		}},
		{"output follows from an offset", func() error {
			jj, _ := r.Get(j.ID)
			all := jj.Status(0)
			from := int64(strings.Index(all.Out, "error:"))
			if rest := jj.Status(from); !strings.HasPrefix(rest.Out, "error:") || rest.Next != all.Next {
				return fmt.Errorf("Status(%d) = %q (next %d, want %d)", from, rest.Out, rest.Next, all.Next)
			}
			return nil
		}},
		{"the next job may start", func() error {
			if r.Running() != nil {
				return fmt.Errorf("finished job still running")
			}
			_, err := r.Start("next", "Next", []string{"--version"})
			return err
		}},
	}
	for _, st := range steps {
		if err := st.check(); err != nil {
			t.Fatalf("%s: %v", st.name, err)
		}
	}
}

// TestKept: only the last jobs.Kept jobs (and their logs) are remembered.
func TestKept(t *testing.T) {
	bin := itest.Hsctl(t)
	r := jobs.New(itest.Repo(t).Root, bin, nil)
	r.Dir = t.TempDir()
	var ids []string
	for i := 0; i < jobs.Kept+2; i++ {
		j, err := r.Start("v", "Version", []string{"--version"})
		if err != nil {
			t.Fatal(err)
		}
		wait(t, r, j.ID)
		ids = append(ids, j.ID)
	}
	cases := []struct {
		id   string
		kept bool
	}{
		{ids[0], false}, {ids[1], false}, {ids[2], true}, {ids[len(ids)-1], true},
	}
	for _, tc := range cases {
		if _, ok := r.Get(tc.id); ok != tc.kept {
			t.Errorf("job %s kept = %v, want %v", tc.id, ok, tc.kept)
		}
		if _, err := os.Stat(filepath.Join(r.Dir, tc.id+".log")); (err == nil) != tc.kept {
			t.Errorf("log of %s present = %v, want %v", tc.id, err == nil, tc.kept)
		}
	}
}

// TestShortDuration renders elapsed times the way the banner shows them.
func TestShortDuration(t *testing.T) {
	cases := map[time.Duration]string{
		0: "0s", 45 * time.Second: "45s", 3*time.Minute + 5*time.Second: "3m 05s",
		time.Hour + 2*time.Minute: "1h 02m", 1499 * time.Millisecond: "1s",
	}
	for d, want := range cases {
		if got := jobs.ShortDuration(d); got != want {
			t.Errorf("ShortDuration(%v) = %q, want %q", d, got, want)
		}
	}
}
