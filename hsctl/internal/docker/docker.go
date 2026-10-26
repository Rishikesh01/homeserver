// Package docker is the stack's Docker client: it runs the docker CLI (compose included) for the
// other services, picks sudo only when the daemon refuses this user, and explains a Docker that
// can't be used instead of failing with a bare exit status.
package docker

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/user"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Rishikesh01/homeserver/hsctl/internal/prompt"
)

// Client runs the docker CLI.
type Client struct {
	// Dir is where probes (inspect, info, …) run; compose commands pass their own folder.
	Dir            string
	Stdout, Stderr io.Writer

	sudoOnce sync.Once
	sudo     bool
}

// New returns a client whose probes run in dir, streaming to the process's stdout/stderr.
func New(dir string) *Client {
	return &Client{Dir: dir, Stdout: os.Stdout, Stderr: os.Stderr}
}

// needsSudo reports whether the daemon needs sudo from this user, detected once. The web UI asks
// from many request goroutines at once, hence the Once. Only a permission error means "use sudo";
// a stopped or missing daemon is reported by Problem instead of being retried with sudo (which
// just turned it into a surprise password prompt).
func (c *Client) needsSudo() bool {
	c.sudoOnce.Do(func() {
		out, err := exec.Command("docker", "info").CombinedOutput()
		c.sudo = err != nil && os.Geteuid() != 0 && strings.Contains(strings.ToLower(string(out)), "permission denied")
		if c.sudo {
			fmt.Fprintln(c.Stderr, "note: using sudo for docker — "+groupAdvice())
		}
	})
	return c.sudo
}

// Command builds a docker (or sudo docker) command rooted at dir. Without a terminal to ask for a
// password (a script, the timer), sudo is told not to ask (-n) so it fails fast instead of hanging.
func (c *Client) Command(dir string, args ...string) *exec.Cmd {
	name := "docker"
	if c.needsSudo() {
		pre := []string{"docker"}
		if !prompt.IsTerminal(os.Stdin) {
			pre = []string{"-n", "docker"}
		}
		name, args = "sudo", append(pre, args...)
	}
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	return cmd
}

// Run streams a docker command's output to the client's writers.
func (c *Client) Run(dir string, args ...string) error {
	cmd := c.Command(dir, args...)
	cmd.Stdout, cmd.Stderr, cmd.Stdin = c.Stdout, c.Stderr, os.Stdin
	return cmd.Run()
}

// Output captures stdout of a docker command, trimmed (stderr still streams).
func (c *Client) Output(dir string, args ...string) (string, error) {
	cmd := c.Command(dir, args...)
	cmd.Stderr = c.Stderr
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}

// Combined captures stdout and stderr together (for showing to the user).
func (c *Client) Combined(dir string, args ...string) (string, error) {
	out, err := c.Command(dir, args...).CombinedOutput()
	return string(out), err
}

// Quiet runs a probe and only reports whether it succeeded.
func (c *Client) Quiet(dir string, args ...string) error {
	return c.Command(dir, args...).Run()
}

// ComposeUpArgs is `compose up -d` plus extra args — without per-layer download progress when
// the output isn't a terminal (a dashboard job's log, the journal), where every progress tick
// would land as its own line and bury the messages that matter.
func (c *Client) ComposeUpArgs(extra ...string) []string {
	args := []string{"compose", "up", "-d"}
	if f, ok := c.Stdout.(*os.File); !ok || !prompt.IsTerminal(f) {
		args = append(args, "--quiet-pull")
	}
	return append(args, extra...)
}

// ComposePullArgs is `compose pull`, quiet off a terminal (see ComposeUpArgs).
func (c *Client) ComposePullArgs() []string {
	if f, ok := c.Stdout.(*os.File); !ok || !prompt.IsTerminal(f) {
		return []string{"compose", "pull", "--quiet"}
	}
	return []string{"compose", "pull"}
}

// ContainerState returns a container's state ("running", "exited", …), or "" if it's missing.
func (c *Client) ContainerState(name string) string {
	out, err := c.Command(c.Dir, "inspect", "-f", "{{.State.Status}}", name).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// ContainerImage returns the image a container was created from (e.g. "postgres:18.6-alpine"), or
// "" if it doesn't exist.
func (c *Client) ContainerImage(name string) string {
	out, err := c.Command(c.Dir, "inspect", "-f", "{{.Config.Image}}", name).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// VolumeMountpoint resolves a volume's host mountpoint. Empty output (a volume docker knows but
// can't place) is as unusable as a failed inspect, so it errors too.
func (c *Client) VolumeMountpoint(name string) (string, error) {
	mp, err := c.Output(c.Dir, "volume", "inspect", "-f", "{{.Mountpoint}}", name)
	if err != nil {
		return "", fmt.Errorf("inspect volume %s: %w", name, err)
	}
	if mp == "" {
		return "", fmt.Errorf("inspect volume %s: empty mountpoint", name)
	}
	return mp, nil
}

// VolumeExistsOrUnknown reports whether a volume exists — and says yes when docker can't be asked,
// because callers use it to decide whether changing something would strand data.
func (c *Client) VolumeExistsOrUnknown(name string) bool {
	if c.Quiet(c.Dir, "volume", "inspect", name) == nil {
		return true
	}
	return c.Quiet(c.Dir, "info") != nil
}

// Volumes lists every volume's name.
func (c *Client) Volumes() ([]string, error) {
	out, err := c.Output(c.Dir, "volume", "ls", "--format", "{{.Name}}")
	if err != nil {
		return nil, err
	}
	return strings.Fields(out), nil
}

// EnsureNetwork creates a network if it isn't already there. Idempotent, and safe against a
// concurrent create.
func (c *Client) EnsureNetwork(name string) error {
	if c.Quiet(c.Dir, "network", "inspect", name) == nil {
		return nil
	}
	if out, err := c.Combined(c.Dir, "network", "create", name); err != nil {
		if !strings.Contains(out, "already exists") { // lost a create race — that's fine
			return fmt.Errorf("create docker network %q: %v\n%s", name, err, strings.TrimSpace(out))
		}
	}
	return nil
}

// FirstLocalImage returns a repository:tag of some image already on the box (skipping dangling
// <none> ones), or "" if there are none — an offline-safe image for a throwaway container.
func (c *Client) FirstLocalImage() string {
	out, err := c.Output(c.Dir, "image", "ls", "--format", "{{.Repository}}:{{.Tag}}")
	if err != nil {
		return ""
	}
	for _, l := range strings.Split(out, "\n") {
		if l = strings.TrimSpace(l); l != "" && !strings.Contains(l, "<none>") {
			return l
		}
	}
	return ""
}

// BridgeGateway returns the IPv4 gateway of docker's default bridge (e.g. 172.17.0.1) — the
// address host.docker.internal resolves to, where Caddy connects to reach the host. The bridge can
// have several IPAM configs (IPv4 + IPv6), so the first IPv4 one is picked.
func (c *Client) BridgeGateway() string {
	out, err := c.Output(c.Dir, "network", "inspect", "bridge", "-f", "{{range .IPAM.Config}}{{.Gateway}} {{end}}")
	if err != nil {
		return ""
	}
	for _, f := range strings.Fields(out) {
		if ip := net.ParseIP(f); ip != nil && ip.To4() != nil {
			return f
		}
	}
	return ""
}

// Wait polls check once a second until it's true or the timeout elapses.
func Wait(timeout time.Duration, check func() bool) bool {
	deadline := time.Now().Add(timeout)
	for {
		if check() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(time.Second)
	}
}

// Problem describes why docker can't be used right now, or "" when it can.
func (c *Client) Problem() string {
	if _, err := exec.LookPath("docker"); err != nil {
		return "Docker isn't installed — install it: curl -fsSL https://get.docker.com | sh"
	}
	out, err := c.Command(c.Dir, "info").CombinedOutput()
	if err == nil {
		return ""
	}
	return Classify(string(out), err)
}

// Require fails with Problem's explanation when docker can't be used.
func (c *Client) Require() error {
	if p := c.Problem(); p != "" {
		return errors.New(p)
	}
	return nil
}

// Classify turns `docker info`'s failure output into what to do about it.
func Classify(out string, err error) string {
	msg := strings.ToLower(out)
	switch {
	case strings.Contains(msg, "cannot connect to the docker daemon") || strings.Contains(msg, "is the docker daemon running"):
		return "Docker isn't running — start it: sudo systemctl start docker"
	case strings.Contains(msg, "permission denied"):
		return "Docker refused this user — " + groupAdvice()
	case strings.Contains(msg, "a password is required") || strings.Contains(msg, "a terminal is required"):
		return "Docker needs sudo here and there's no terminal to ask for the password — " + groupAdvice()
	}
	if line := strings.TrimSpace(out); line != "" {
		first, _, _ := strings.Cut(line, "\n")
		return "Docker isn't usable: " + first
	}
	return "Docker isn't usable: " + err.Error()
}

// groupAdvice explains how to use docker without sudo, depending on whether the user is already in
// the docker group on disk (then only this login session predates it).
func groupAdvice() string {
	if u, err := user.Current(); err == nil {
		if g, err := user.LookupGroup("docker"); err == nil {
			if ids, err := u.GroupIds(); err == nil && slices.Contains(ids, g.Gid) {
				return "you're in the docker group, but this login session started before you were added: " +
					"log out and back in (or run: newgrp docker)"
			}
		}
	}
	return "your user isn't in the docker group: sudo usermod -aG docker $USER, then log out and back in"
}
