//go:build integration

package oplock_test

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Rishikesh01/homeserver/hsctl/internal/itest"
	"github.com/Rishikesh01/homeserver/hsctl/internal/oplock"
	"github.com/Rishikesh01/homeserver/hsctl/internal/paths"
)

// TestAcquire: one operation at a time. A second taker is turned away naming the holder (or waits
// for it with a wait budget), and releasing frees the lock for the next one.
func TestAcquire(t *testing.T) {
	cases := []struct {
		name       string
		releaseIn  time.Duration // when the first holder lets go (0 = never during the test)
		wait       time.Duration
		wantBusy   bool
		wantNotice bool
	}{
		{"busy, no wait", 0, 0, true, false},
		{"rides out a probe-length holder even without a wait", 200 * time.Millisecond, 0, false, false},
		{"waits out a short holder", time.Second, 3 * time.Second, false, true},
		{"gives up when the holder outlasts the wait", 0, 1500 * time.Millisecond, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := t.TempDir()
			first, err := oplock.New(repo).Acquire("backup run", 0)
			if err != nil {
				t.Fatal(err)
			}
			defer first.Release()
			if got := oplock.New(repo).Busy(); !strings.Contains(got, "backup run (pid ") {
				t.Errorf("Busy = %q, want it to name the holder", got)
			}
			if tc.releaseIn > 0 {
				time.AfterFunc(tc.releaseIn, first.Release)
			}
			var notice bytes.Buffer
			second, err := (&oplock.Locker{Root: repo, Notice: &notice}).Acquire("dashboard: up", tc.wait)
			if strings.Contains(notice.String(), "waiting for another hsctl operation") != tc.wantNotice {
				t.Errorf("notice = %q, want one: %v", notice.String(), tc.wantNotice)
			}
			if tc.wantBusy {
				if !errors.Is(err, oplock.ErrBusy) || !strings.Contains(err.Error(), "backup run") {
					t.Fatalf("err = %v, want ErrBusy naming the holder", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("second acquire: %v", err)
			}
			if got := oplock.New(repo).Busy(); !strings.Contains(got, "dashboard: up") {
				t.Errorf("Busy = %q, want the new holder", got)
			}
			second.Release()
			second.Release() // twice is harmless
			if got := oplock.New(repo).Busy(); got != "" {
				t.Errorf("lock still busy after release: %q", got)
			}
		})
	}
}

// TestAcrossProcesses: the lock holds against another process (a terminal, the backup timer), and
// the kernel frees it when that process dies — even killed, with no chance to clean up.
func TestAcrossProcesses(t *testing.T) {
	repo := t.TempDir()
	lockFile := filepath.Join(repo, paths.LockFile)
	cases := []struct {
		name string
		end  func(*exec.Cmd)
	}{
		{"holder exits", func(c *exec.Cmd) { _ = c.Process.Signal(os.Interrupt); _ = c.Wait() }},
		{"holder killed", func(c *exec.Cmd) { _ = c.Process.Kill(); _ = c.Wait() }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// flock(1) takes an exclusive lock and becomes `sleep` (no fork), so the lock lives exactly as long
			// as that one process.
			holder := exec.Command("flock", "--no-fork", "-x", lockFile, "sleep", "60")
			if err := holder.Start(); err != nil {
				t.Skipf("flock(1) unavailable: %v", err)
			}
			deadline := time.Now().Add(5 * time.Second)
			for oplock.New(repo).Busy() == "" && time.Now().Before(deadline) {
				time.Sleep(50 * time.Millisecond)
			}
			if _, err := oplock.New(repo).Acquire("restore", 0); !errors.Is(err, oplock.ErrBusy) {
				t.Fatalf("acquire while another process holds it: %v", err)
			}
			tc.end(holder)
			l, err := oplock.New(repo).Acquire("restore", time.Second)
			if err != nil {
				t.Fatalf("lock not freed after the holder went: %v", err)
			}
			l.Release()
		})
	}
}

// TestOwnership: root taking the lock in the admin's checkout leaves the lock file the admin's, and
// a symlink planted in its place isn't followed.
func TestOwnership(t *testing.T) {
	itest.NeedRoot(t)
	cases := []struct {
		name    string
		plant   bool
		wantErr bool
	}{
		{"lock file owned by the admin", false, false},
		{"planted symlink refused", true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := t.TempDir()
			itest.Chown(t, repo, 1000, 1000)
			target := filepath.Join(t.TempDir(), "victim")
			if tc.plant {
				itest.WriteFile(t, filepath.Dir(target), "victim", "precious\n")
				if err := os.Symlink(target, filepath.Join(repo, paths.LockFile)); err != nil {
					t.Fatal(err)
				}
			}
			l, err := oplock.New(repo).Acquire("setup", 0)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, want error %v", err, tc.wantErr)
			}
			if err != nil {
				if itest.ReadFile(target) != "precious\n" {
					t.Error("the symlink's target was written")
				}
				return
			}
			defer l.Release()
			if o := itest.Owner(t, filepath.Join(repo, paths.LockFile)); o != "1000:1000" {
				t.Errorf("lock file owned by %s", o)
			}
		})
	}
}
