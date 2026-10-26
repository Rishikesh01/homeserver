// Package restic runs the restic CLI against a repository, with the password file and any cloud
// credentials restic needs in its environment.
package restic

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// Client runs restic.
type Client struct {
	// PassFile is the repository password file (one password opens the primary and the replica).
	PassFile string
	// ExtraEnv returns extra KEY=VALUE pairs for restic — cloud credentials. Nil for none.
	ExtraEnv func() []string

	Stdout, Stderr io.Writer
}

// New returns a client using passFile, streaming to the process's stdout/stderr.
func New(passFile string, extraEnv func() []string) *Client {
	return &Client{PassFile: passFile, ExtraEnv: extraEnv, Stdout: os.Stdout, Stderr: os.Stderr}
}

// Env is restic's environment for repository.
func (c *Client) Env(repository string) []string {
	env := append(os.Environ(), "RESTIC_REPOSITORY="+repository, "RESTIC_PASSWORD_FILE="+c.PassFile)
	if c.ExtraEnv != nil {
		env = append(env, c.ExtraEnv()...)
	}
	return env
}

// Run runs restic against repository, streaming its output.
func (c *Client) Run(repository string, args ...string) error {
	cmd := exec.Command("restic", args...)
	cmd.Env = c.Env(repository)
	cmd.Stdout, cmd.Stderr, cmd.Stdin = c.Stdout, c.Stderr, os.Stdin
	return cmd.Run()
}

// Output runs restic and returns its combined output instead of showing it — for the dashboard,
// and for probes whose failure is expected (does the replica exist yet?).
func (c *Client) Output(repository string, args ...string) (string, error) {
	cmd := exec.Command("restic", args...)
	cmd.Env = c.Env(repository)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// Installed reports whether restic is on the PATH.
func (c *Client) Installed() bool { _, err := exec.LookPath("restic"); return err == nil }

// Require fails, with how to install it, when restic is missing.
func (c *Client) Require() error {
	if !c.Installed() {
		return fmt.Errorf("restic not installed — install it first:\n" +
			"  sudo apt-get install -y restic   (or download from https://restic.net)")
	}
	return nil
}

// Version returns the installed restic version, e.g. "0.17.3", parsed from `restic version`
// ("restic 0.17.3 compiled with go1.23.1 on linux/amd64").
func (c *Client) Version() (string, error) {
	out, err := exec.Command("restic", "version").Output()
	if err != nil {
		return "", fmt.Errorf("running `restic version`: %w", err)
	}
	if f := strings.Fields(string(out)); len(f) >= 2 && f[0] == "restic" {
		return f[1], nil
	}
	return "", fmt.Errorf("could not parse restic version from %q", strings.TrimSpace(string(out)))
}

// RepoMissing reports whether restic's output says there's no repository at the location (as
// opposed to a wrong password, a lock, or an unreachable host).
func RepoMissing(out string) bool {
	o := strings.ToLower(out)
	return strings.Contains(o, "repository does not exist") || strings.Contains(o, "is there a repository at")
}
