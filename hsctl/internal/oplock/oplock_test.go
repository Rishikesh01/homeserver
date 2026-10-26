package oplock

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// TestOpLock: one operation at a time. A second taker is turned away naming the holder (or waits
// for it with a wait budget), and releasing frees the lock for the next one.
func TestOpLock(t *testing.T) {
	cases := []struct {
		name       string
		releaseIn  time.Duration // when the first holder lets go (0 = never during the test)
		wait       time.Duration
		wantBusy   bool
		wantHolder string
	}{
		{"busy, no wait", 0, 0, true, "backup run"},
		{"waits out a short holder", 200 * time.Millisecond, 3 * time.Second, false, ""},
		{"gives up when the holder outlasts the wait", 0, 700 * time.Millisecond, true, "backup run"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := t.TempDir()
			first, err := New(repo).Acquire("backup run", 0)
			if err != nil {
				t.Fatal(err)
			}
			defer first.Release()
			if got := New(repo).Busy(); !strings.Contains(got, "backup run") {
				t.Errorf("opLockBusy = %q, want it to name the holder", got)
			}
			if tc.releaseIn > 0 {
				time.AfterFunc(tc.releaseIn, first.Release)
			}
			second, err := New(repo).Acquire("dashboard: up", tc.wait)
			if tc.wantBusy {
				if !errors.Is(err, ErrBusy) || !strings.Contains(err.Error(), tc.wantHolder) {
					t.Fatalf("err = %v, want ErrBusy naming %q", err, tc.wantHolder)
				}
				return
			}
			if err != nil {
				t.Fatalf("second acquire: %v", err)
			}
			second.Release()
			if got := New(repo).Busy(); got != "" {
				t.Errorf("lock still busy after release: %q", got)
			}
		})
	}
}
