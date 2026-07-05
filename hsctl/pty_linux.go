//go:build linux

package main

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/unix"
)

// A small, dependency-free PTY. The web terminal needs a real pseudo-terminal so
// interactive programs (bash line editing, less, top, even vim) behave — a plain pipe
// would make them think they're not on a terminal. Rather than pull in creack/pty,
// we open /dev/ptmx and wire the slave up by hand; it's ~40 lines on Linux via
// x/sys/unix, which the module already depends on.

// startPTY starts cmd attached to a new pseudo-terminal and returns the master side.
// Read the returned file for the program's output; write to it to deliver keystrokes.
// The child becomes a session leader with the pts as its controlling terminal, so job
// control and signals (Ctrl-C) work as on a real login.
func startPTY(cmd *exec.Cmd) (*os.File, error) {
	ptm, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		return nil, fmt.Errorf("open /dev/ptmx: %w", err)
	}
	// Unlock the slave, then learn its number to open /dev/pts/N.
	if err := unix.IoctlSetPointerInt(int(ptm.Fd()), unix.TIOCSPTLCK, 0); err != nil {
		ptm.Close()
		return nil, fmt.Errorf("unlock pts: %w", err)
	}
	n, err := unix.IoctlGetInt(int(ptm.Fd()), unix.TIOCGPTN)
	if err != nil {
		ptm.Close()
		return nil, fmt.Errorf("get pts number: %w", err)
	}
	pts, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", n), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		ptm.Close()
		return nil, fmt.Errorf("open pts: %w", err)
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = pts, pts, pts
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setsid = true  // new session: the child leads it
	cmd.SysProcAttr.Setctty = true // make the pts (its fd 0) the controlling terminal
	if err := cmd.Start(); err != nil {
		ptm.Close()
		pts.Close()
		return nil, err
	}
	// The child holds the slave now; we only need the master in this process.
	pts.Close()
	return ptm, nil
}

// setPTYSize tells the kernel the terminal's window size, so full-screen programs lay
// out correctly and SIGWINCH fires on resize.
func setPTYSize(ptm *os.File, rows, cols uint16) error {
	return unix.IoctlSetWinsize(int(ptm.Fd()), unix.TIOCSWINSZ, &unix.Winsize{
		Row: rows, Col: cols,
	})
}
