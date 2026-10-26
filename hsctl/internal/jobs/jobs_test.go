package jobs

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeHsctl writes a stand-in "hsctl" that runs script, so jobs can be tested without the real
// binary or Docker.
func fakeHsctl(t *testing.T, script string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "hsctl")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+script+"\n"), 0755); err != nil {
		t.Fatal(err)
	}
	return p
}

func waitJob(t *testing.T, jr *Runner, id string) Status {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		j, ok := jr.Get(id)
		if !ok {
			t.Fatalf("job %s vanished", id)
		}
		if st := j.Status(0); st.Done {
			return st
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("job %s didn't finish", id)
	return Status{}
}

// TestJobRunner: a job's whole output lands in its log, its exit status is reported, and a
// second job is refused while one runs.
func TestJobRunner(t *testing.T) {
	cases := []struct {
		name    string
		script  string
		wantOK  bool
		wantOut []string
	}{
		{"success", `echo "args: $*"; echo to-stderr >&2`, true, []string{"$ hsctl backup run", "args: backup run", "to-stderr", "[done]"}},
		{"failure", `echo boom; exit 3`, false, []string{"boom", "[command exited with error: exit status 3]"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			jr := New(t.TempDir(), fakeHsctl(t, tc.script), nil)
			j, err := jr.Start("backup-run", "Back up now", []string{"backup", "run"})
			if err != nil {
				t.Fatal(err)
			}
			st := waitJob(t, jr, j.ID)
			if st.OK != tc.wantOK {
				t.Errorf("ok = %v, want %v", st.OK, tc.wantOK)
			}
			for _, w := range tc.wantOut {
				if !strings.Contains(st.Out, w) {
					t.Errorf("output %q lacks %q", st.Out, w)
				}
			}
		})
	}
}

// TestJobRunnerOneAtATime: while a job runs, starting another is refused with ErrBusy, and the
// output can be read incrementally from an offset.
func TestJobRunnerOneAtATime(t *testing.T) {
	jr := New(t.TempDir(), fakeHsctl(t, `echo first; sleep 1; echo second`), nil)
	j, err := jr.Start("slow", "Slow job", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := jr.Start("other", "Other", nil); !errors.Is(err, ErrBusy) {
		t.Fatalf("second start err = %v, want ErrBusy", err)
	}
	if r := jr.Running(); r == nil || r.ID != j.ID {
		t.Fatalf("running() = %v, want the slow job", r)
	}
	st := waitJob(t, jr, j.ID)
	half := int64(strings.Index(st.Out, "second"))
	rest := func() string { jj, _ := jr.Get(j.ID); return jj.Status(half).Out }()
	if !strings.HasPrefix(rest, "second") {
		t.Errorf("status(from) = %q, want the output from the offset on", rest)
	}
	if jr.Running() != nil {
		t.Error("finished job still reported as running")
	}
}
