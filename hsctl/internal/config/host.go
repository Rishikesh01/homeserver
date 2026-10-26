package config

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// LocalHost answers Host from the machine hsctl runs on.
type LocalHost struct{}

// IP is the source address of the default route (`ip -4 route get 1.1.1.1`), "" if unknown.
func (LocalHost) IP() string {
	out, err := exec.Command("ip", "-4", "route", "get", "1.1.1.1").Output()
	if err != nil {
		return ""
	}
	f := strings.Fields(string(out))
	for i, w := range f {
		if w == "src" && i+1 < len(f) {
			return f[i+1]
		}
	}
	return ""
}

// Timezone asks timedatectl, then /etc/timezone, then settles for UTC.
func (LocalHost) Timezone() string {
	if out, err := exec.Command("timedatectl", "show", "-p", "Timezone", "--value").Output(); err == nil {
		if s := strings.TrimSpace(string(out)); s != "" {
			return s
		}
	}
	if b, err := os.ReadFile("/etc/timezone"); err == nil {
		if s := strings.TrimSpace(string(b)); s != "" {
			return s
		}
	}
	return "Etc/UTC"
}

// PortBusy reports whether a TCP port is listening (via `ss`).
func (LocalHost) PortBusy(p int) bool {
	out, err := exec.Command("ss", "-ltn").Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), fmt.Sprintf(":%d ", p))
}
