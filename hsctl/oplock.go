package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
)

// The operation lock: one state-changing hsctl operation at a time, however hsctl was started —
// a terminal, the nightly backup timer, or a dashboard button (which re-execs hsctl). Without it
// a restore could wipe the volumes a backup is reading, or a "Stop all" could pull the stack out
// from under either. An in-process mutex can't see the timer or a second terminal, so the lock is
// an flock(2) on a file in the repo: the kernel drops it when the holder exits, crash included,
// so a stale lock never needs cleaning up.
const lockFile = ".hsctl.lock"

// opLock is a held operation lock; Release it when the operation ends.
type opLock struct{ f *os.File }

// errBusy is returned (wrapped) when another operation holds the lock.
var errBusy = errors.New("another hsctl operation is running")

// acquireOpLock takes the operation lock for what (e.g. "backup run"). It fails at once when
// another operation holds it, or with wait > 0 keeps trying for that long first (the nightly
// backup waits out a dashboard job rather than failing).
func acquireOpLock(repo, what string, wait time.Duration) (*opLock, error) {
	path := filepath.Join(repo, lockFile)
	// O_NOFOLLOW: root opens (and truncates) this file in a folder a normal user owns.
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW, 0644)
	if err != nil {
		if f, err = os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0); err != nil { // flock works on a read-only fd too
			return nil, fmt.Errorf("open the operation lock %s: %w", path, err)
		}
	}
	inheritOwner(path)
	// Always retry briefly: the dashboard's "is anything running?" probe takes the lock for an
	// instant, and that must not make a real operation fail.
	if wait < 500*time.Millisecond {
		wait = 500 * time.Millisecond
	}
	deadline := time.Now().Add(wait)
	announced := false
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		holder := lockHolder(path)
		if !errors.Is(err, syscall.EWOULDBLOCK) || time.Now().After(deadline) {
			f.Close()
			return nil, fmt.Errorf("%w: %s — try again once it finishes", errBusy, holder)
		}
		if !announced && wait > time.Second {
			fmt.Fprintf(os.Stderr, "waiting for another hsctl operation to finish: %s\n", holder)
			announced = true
		}
		time.Sleep(100 * time.Millisecond)
	}
	// Record who holds it, for the message a second caller sees. Best-effort (a read-only fd).
	_ = f.Truncate(0)
	_, _ = f.WriteAt([]byte(fmt.Sprintf("%s (pid %d, since %s)\n", what, os.Getpid(),
		time.Now().Format("15:04"))), 0)
	return &opLock{f: f}, nil
}

// Release drops the lock.
func (l *opLock) Release() {
	if l == nil || l.f == nil {
		return
	}
	_ = l.f.Truncate(0)
	_ = syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	_ = l.f.Close()
	l.f = nil
}

// lockHolder describes the current holder from the lock file's contents.
func lockHolder(path string) string {
	b, _ := os.ReadFile(path)
	if s := strings.TrimSpace(string(b)); s != "" {
		return s
	}
	return "unknown operation"
}

// opLockBusy reports what holds the operation lock right now, or "" when it's free. The dashboard
// uses it to show an "operation in progress" banner.
func opLockBusy(repo string) string {
	path := filepath.Join(repo, lockFile)
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return lockHolder(path)
	}
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return ""
}

// locked wraps a command's RunE so it holds the operation lock while it runs. Commands outside the
// repo run unlocked — they fail on their own "not in the homeserver repo" check.
func locked(what string, wait time.Duration, run func(*cobra.Command, []string) error) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		repo := repoDir()
		if !isRepo(repo) {
			return run(cmd, args)
		}
		l, err := acquireOpLock(repo, what, wait)
		if err != nil {
			return err
		}
		defer l.Release()
		return run(cmd, args)
	}
}
