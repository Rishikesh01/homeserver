//go:build integration

package docker_test

import (
	"bytes"
	"errors"
	"net"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Rishikesh01/homeserver/hsctl/internal/docker"
	"github.com/Rishikesh01/homeserver/hsctl/internal/itest"
)

// TestClassify: an unusable Docker is diagnosed by what actually went wrong, not always as
// "needs sudo".
func TestClassify(t *testing.T) {
	cases := []struct{ out, want string }{
		{"Cannot connect to the Docker daemon at unix:///var/run/docker.sock. Is the docker daemon running?", "isn't running"},
		{"permission denied while trying to connect to the Docker daemon socket", "refused this user"},
		{"sudo: a password is required", "no terminal"},
		{"sudo: a terminal is required to read the password", "no terminal"},
		{"some other failure\nmore", "Docker isn't usable: some other failure"},
		{"", "exit status 1"},
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			if got := docker.Classify(tc.out, errors.New("exit status 1")); !strings.Contains(got, tc.want) {
				t.Errorf("= %q, want it to mention %q", got, tc.want)
			}
		})
	}
}

// TestProblem asks a real daemon: none when it answers, "isn't running" when nothing listens.
func TestProblem(t *testing.T) {
	itest.NeedDocker(t)
	cases := []struct {
		name, host, want string
	}{
		{"daemon up", "", ""},
		{"daemon down", "unix:///nonexistent/docker.sock", "Docker isn't running"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.host != "" {
				t.Setenv("DOCKER_HOST", tc.host)
			}
			c := docker.New(t.TempDir())
			got := c.Problem()
			if tc.want == "" && got != "" || !strings.Contains(got, tc.want) {
				t.Errorf("Problem = %q, want %q", got, tc.want)
			}
			if err := c.Require(); (err != nil) != (tc.want != "") {
				t.Errorf("Require = %v", err)
			}
		})
	}
}

// TestProblemAsUser: a user the daemon refuses gets told how to fix it — through sudo, which has no
// terminal to ask for a password here, never a hang.
func TestProblemAsUser(t *testing.T) {
	if !itest.Unprivileged(t) {
		return
	}
	if _, err := os.Stat("/var/run/docker.sock"); err != nil { // `docker info` fails for this user by design
		t.Skip("needs a local Docker daemon")
	}
	var stderr bytes.Buffer
	c := docker.New(t.TempDir())
	c.Stderr = &stderr
	cases := []struct{ name, want string }{
		{"diagnosis", "docker group"},
	}
	for _, tc := range cases {
		if got := c.Problem(); !strings.Contains(got, tc.want) {
			t.Errorf("%s: Problem = %q, want it to mention %q", tc.name, got, tc.want)
		}
	}
	if !strings.Contains(stderr.String(), "using sudo for docker") {
		t.Errorf("the switch to sudo should be announced: %q", stderr.String())
	}
}

// TestObjects drives a real container, volume and network through the probes the services use. The
// steps run in order against the same objects.
func TestObjects(t *testing.T) {
	itest.NeedDocker(t)
	itest.Pull(t, itest.Busybox)
	name, vol, netw, missing := itest.Name("ctr"), itest.Name("vol"), itest.Name("net"), itest.Name("missing")
	t.Cleanup(func() {
		_ = docker.New("").Quiet("", "rm", "-f", name)
		_ = docker.New("").Quiet("", "volume", "rm", "-f", vol)
		_ = docker.New("").Quiet("", "network", "rm", netw)
	})
	itest.Docker(t, "run", "-d", "--name", name, "-v", vol+":/data", itest.Busybox, "sleep", "300")
	var out bytes.Buffer
	c := docker.New(t.TempDir())
	c.Stdout, c.Stderr = &out, &out
	steps := []struct {
		name string
		got  func() any
		want any
	}{
		{"state of a running container", func() any { return c.ContainerState(name) }, "running"},
		{"state of a missing container", func() any { return c.ContainerState(missing) }, ""},
		{"image", func() any { return c.ContainerImage(name) }, itest.Busybox},
		{"image of a missing container", func() any { return c.ContainerImage(missing) }, ""},
		{"volume mountpoint", func() any {
			mp, err := c.VolumeMountpoint(vol)
			return err == nil && strings.HasPrefix(mp, "/var/lib/docker/volumes/"+vol)
		}, true},
		{"missing volume has no mountpoint", func() any { _, err := c.VolumeMountpoint(missing); return err != nil }, true},
		{"volume listed", func() any { v, _ := c.Volumes(); return slices.Contains(v, vol) }, true},
		{"volume exists", func() any { return c.VolumeExistsOrUnknown(vol) }, true},
		{"missing volume", func() any { return c.VolumeExistsOrUnknown(missing) }, false},
		{"a local image", func() any { return c.FirstLocalImage() != "" }, true},
		{"bridge gateway is IPv4", func() any { return net.ParseIP(c.BridgeGateway()).To4() != nil }, true},
		{"output", func() any { return must(c.Output("", "inspect", "-f", "{{.Name}}", name)) }, "/" + name},
		{"quiet failure", func() any { return c.Quiet("", "inspect", missing) != nil }, true},
		{"combined carries the error", func() any {
			o, err := c.Combined("", "inspect", missing)
			return err != nil && strings.Contains(strings.ToLower(o), "no such")
		}, true},
		{"state after stop", func() any { itest.Docker(t, "stop", "-t", "0", name); return c.ContainerState(name) }, "exited"},
		{"run streams to Stdout", func() any {
			err := c.Run("", "inspect", "-f", "{{.State.Status}}", name)
			return err == nil && strings.Contains(out.String(), "exited")
		}, true},
		{"network created", func() any { return c.EnsureNetwork(netw) == nil && c.Quiet("", "network", "inspect", netw) == nil }, true},
		{"network create is idempotent", func() any { return c.EnsureNetwork(netw) }, nil},
		{"volume counts as maybe-there with docker unreachable", func() any {
			t.Setenv("DOCKER_HOST", "unix:///nonexistent/docker.sock")
			return docker.New("").VolumeExistsOrUnknown(missing)
		}, true},
	}
	for _, st := range steps {
		if got := st.got(); got != st.want {
			t.Errorf("%s = %v, want %v", st.name, got, st.want)
		}
	}
}

func must(s string, err error) string {
	if err != nil {
		return "error: " + err.Error()
	}
	return s
}

// TestComposeArgs: download progress only on a terminal; off one (a job's log, the journal) pulls
// are quiet.
func TestComposeArgs(t *testing.T) {
	pty := itest.PTY(t)
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	cases := []struct {
		name   string
		stdout any
		quiet  bool
	}{
		{"buffer", &bytes.Buffer{}, true},
		{"pipe", w, true},
		{"terminal", pty, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := docker.New("")
			switch s := tc.stdout.(type) {
			case *bytes.Buffer:
				c.Stdout = s
			case *os.File:
				c.Stdout = s
			}
			up := strings.Join(c.ComposeUpArgs("--force-recreate", "vaultwarden"), " ")
			pull := strings.Join(c.ComposePullArgs(), " ")
			wantUp, wantPull := "compose up -d --force-recreate vaultwarden", "compose pull"
			if tc.quiet {
				wantUp, wantPull = "compose up -d --quiet-pull --force-recreate vaultwarden", "compose pull --quiet"
			}
			if up != wantUp || pull != wantPull {
				t.Errorf("up = %q, pull = %q; want %q, %q", up, pull, wantUp, wantPull)
			}
		})
	}
}

// TestWait polls until the check passes or the time is up.
func TestWait(t *testing.T) {
	cases := []struct {
		name     string
		passAt   int // the call that first passes (0 = never)
		timeout  time.Duration
		want     bool
		maxCalls int
	}{
		{"at once", 1, time.Second, true, 1},
		{"after a retry", 2, 3 * time.Second, true, 2},
		{"never", 0, time.Second, false, 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			got := docker.Wait(tc.timeout, func() bool { calls++; return tc.passAt > 0 && calls >= tc.passAt })
			if got != tc.want || calls > tc.maxCalls {
				t.Errorf("Wait = %v after %d calls, want %v within %d", got, calls, tc.want, tc.maxCalls)
			}
		})
	}
}
