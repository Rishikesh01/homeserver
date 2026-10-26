// Package priv runs the few commands that need root (mount, umount, smartctl, systemctl): directly
// when hsctl already is root — the dashboard service — and through sudo for a local run.
package priv

import (
	"os"
	"os/exec"
)

// Command builds name args… to run as root.
func Command(name string, args ...string) *exec.Cmd {
	if os.Geteuid() == 0 {
		return exec.Command(name, args...)
	}
	return exec.Command("sudo", append([]string{name}, args...)...)
}

// Root is Command as a value, for the services that take a way to run commands as root.
type Root struct{}

// Command builds name args… to run as root.
func (Root) Command(name string, args ...string) *exec.Cmd { return Command(name, args...) }
