// Package oplock is the operation lock: one state-changing hsctl operation at a time, however
// hsctl was started — a terminal, the nightly backup timer, or a dashboard button (which re-execs
// hsctl). Without it a restore could wipe the volumes a backup is reading, or a "Stop all" could
// pull the stack out from under either. An in-process mutex can't see the timer or a second
// terminal, so the lock is an flock(2) on a file in the repo: the kernel drops it when the holder
// exits, crash included, so a stale lock never needs cleaning up.
package oplock

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Rishikesh01/homeserver/hsctl/internal/fsutil"
	"github.com/Rishikesh01/homeserver/hsctl/internal/paths"
)

// ErrBusy is returned (wrapped) when another operation holds the lock.
var ErrBusy = errors.New("another hsctl operation is running")

// Lock is a held operation lock; Release it when the operation ends.
type Lock struct {
	f    *os.File
	once sync.Once
}

// Locker takes the operation lock in one repo.
type Locker struct {
	Root string
	// Notice gets the "waiting for …" line while Acquire waits (nil: discarded).
	Notice io.Writer
}

// New returns the locker for the repo at root, announcing waits on stderr.
func New(root string) *Locker { return &Locker{Root: root, Notice: os.Stderr} }

// Acquire takes the lock for what (e.g. "backup run"). It fails at once when another operation
// holds it, or with wait > 0 keeps trying for that long first (the nightly backup waits out a
// dashboard job rather than failing).
func (l *Locker) Acquire(what string, wait time.Duration) (*Lock, error) {
	path := filepath.Join(l.Root, paths.LockFile)
	// O_NOFOLLOW: root opens (and truncates) this file in a folder a normal user owns.
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW, 0644)
	if err != nil {
		if f, err = os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0); err != nil { // flock works on a read-only fd too
			return nil, fmt.Errorf("open the operation lock %s: %w", path, err)
		}
	}
	fsutil.InheritOwner(path)
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
		holder := holderOf(path)
		if !errors.Is(err, syscall.EWOULDBLOCK) || time.Now().After(deadline) {
			f.Close()
			return nil, fmt.Errorf("%w: %s — try again once it finishes", ErrBusy, holder)
		}
		if !announced && wait > time.Second && l.Notice != nil {
			fmt.Fprintf(l.Notice, "waiting for another hsctl operation to finish: %s\n", holder)
			announced = true
		}
		time.Sleep(100 * time.Millisecond)
	}
	// Record who holds it, for the message a second caller sees. Best-effort (a read-only fd).
	_ = f.Truncate(0)
	_, _ = f.WriteAt([]byte(fmt.Sprintf("%s (pid %d, since %s)\n", what, os.Getpid(),
		time.Now().Format("15:04"))), 0)
	return &Lock{f: f}, nil
}

// Release drops the lock.
func (l *Lock) Release() {
	if l == nil {
		return
	}
	l.once.Do(func() {
		_ = l.f.Truncate(0)
		_ = syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
		_ = l.f.Close()
	})
}

// holderOf describes the current holder from the lock file's contents.
func holderOf(path string) string {
	b, _ := os.ReadFile(path)
	if s := strings.TrimSpace(string(b)); s != "" {
		return s
	}
	return "unknown operation"
}

// Busy reports what holds the lock right now, or "" when it's free — for the dashboard's
// "operation in progress" banner.
func (l *Locker) Busy() string {
	path := filepath.Join(l.Root, paths.LockFile)
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	// A shared lock: it fails only against a held operation, so two pages asking at once don't
	// see each other's probe as a running operation.
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_SH|syscall.LOCK_NB); err != nil {
		return holderOf(path)
	}
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return ""
}
