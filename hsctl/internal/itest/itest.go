// Package itest is the shared plumbing for hsctl's integration tests (built with
// `-tags integration`, run by `make test-integration`). The tests drive each package through its
// public API against real dependencies — the filesystem, restic, Docker, root, block devices — so
// these helpers find those dependencies, skip a test cleanly when one is missing (a plain
// `go test -tags integration` on a laptop without restic, say), and give every test its own names
// and folders to clean up.
package itest

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"

	"github.com/Rishikesh01/homeserver/hsctl/internal/paths"
	"golang.org/x/sys/unix"
)

// NeedDocker skips the test unless a Docker daemon answers.
func NeedDocker(t testing.TB) {
	t.Helper()
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skip("needs a Docker daemon (make test-integration provides one)")
	}
}

// NeedRestic skips the test unless restic is installed.
func NeedRestic(t testing.TB) {
	t.Helper()
	if _, err := exec.LookPath("restic"); err != nil {
		t.Skip("needs restic (make test-integration provides it)")
	}
}

// NeedRoot skips the test unless it runs as root (chown, mount, Docker's volume files).
func NeedRoot(t testing.TB) {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("needs root (make test-integration runs the suite as root)")
	}
}

// Name returns a unique, Docker-safe name for a throwaway container, volume, network or folder.
func Name(prefix string) string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return "hsctl-it-" + prefix + "-" + hex.EncodeToString(b)
}

// Repo creates an empty homeserver checkout in a temp dir: just enough (caddy/docker-compose.yml
// and the core service folders) for paths.IsRepo and the services to treat it as the real thing.
func Repo(t testing.TB) paths.Repo {
	t.Helper()
	root := t.TempDir()
	for _, d := range []string{"caddy", "vaultwarden", "nextcloud", "pihole"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0755); err != nil {
			t.Fatal(err)
		}
	}
	WriteFile(t, root, "caddy/docker-compose.yml", "services: {}\n")
	return paths.Repo{Root: root}
}

// WriteFile writes a file relative to root (creating its folders), failing the test on error.
func WriteFile(t testing.TB, root, rel, content string) string {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return p
}

// ReadFile returns a file's content, or "" when it can't be read.
func ReadFile(path string) string {
	b, _ := os.ReadFile(path)
	return string(b)
}

// SourceRoot returns the real homeserver checkout the tests were built from (the tests run from
// their package's source folder), for fixtures like setup.conf.example and the compose files.
func SourceRoot(t testing.TB) string {
	t.Helper()
	d, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if paths.IsRepo(d) {
			return d
		}
		parent := filepath.Dir(d)
		if parent == d {
			t.Fatal("can't find the homeserver checkout above the test's folder")
		}
		d = parent
	}
}

var composeImage = regexp.MustCompile(`(?m)^\s*image:\s*\$\{[A-Z0-9_]+:-([^}]+)\}`)

// Image returns the image the real stack pins in folder's docker-compose.yml (the first one, or
// the one whose name contains match), so the tests run the stack's own images.
func Image(t testing.TB, folder, match string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(SourceRoot(t), folder, "docker-compose.yml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range composeImage.FindAllStringSubmatch(string(b), -1) {
		if strings.Contains(m[1], match) {
			return m[1]
		}
	}
	t.Fatalf("no image matching %q in %s/docker-compose.yml", match, folder)
	return ""
}

// Docker runs a docker command for the test's own setup or checks, failing the test on error.
func Docker(t testing.TB, args ...string) string {
	t.Helper()
	out, err := exec.Command("docker", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("docker %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// DockerOK reports whether a docker command succeeds (for checks like "does this volume exist").
func DockerOK(args ...string) bool {
	return exec.Command("docker", args...).Run() == nil
}

// Pull makes sure an image is in the daemon (the harness caches pulls between runs).
func Pull(t testing.TB, image string) {
	t.Helper()
	if exec.Command("docker", "image", "inspect", image).Run() == nil {
		return
	}
	Docker(t, "pull", "-q", image)
}

// Busybox is the small image the fixtures run when a test needs a container but not an app.
const Busybox = "busybox:1.37.0"

// childEnv marks a test process re-run by Unprivileged.
const childEnv = "HSCTL_ITEST_UNPRIVILEGED"

// Nobody is the unprivileged user Unprivileged runs as.
const Nobody = 65534

// Unprivileged makes a top-level test check what a NORMAL user sees, even though the suite runs as
// root: as root it re-runs just this test as nobody and reports that run's result, returning false
// (the caller then returns); in the unprivileged run — or when the suite isn't root in the first
// place — it returns true and the test does its work.
func Unprivileged(t *testing.T) bool {
	t.Helper()
	if os.Geteuid() != 0 {
		return true
	}
	rerun(t, os.Args[0], &syscall.Credential{Uid: Nobody, Gid: Nobody}, fmt.Sprintf("as an unprivileged user (uid %d)", Nobody))
	return false
}

// installedEnv marks a test process re-run by Installed.
const installedEnv = "HSCTL_ITEST_INSTALLED"

// Installed makes a top-level test run from a root-owned copy of the test binary in a root-owned
// folder — the way an installed hsctl runs, which is the only kind `hsctl install` will put behind
// a root service. Like Unprivileged, it returns false in the original run (which reports the
// copy's result) and true in the copy.
func Installed(t *testing.T) bool {
	t.Helper()
	NeedRoot(t)
	if os.Getenv(installedEnv) != "" {
		return true
	}
	dir := filepath.Join("/usr/local/libexec", Name("bin"))
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	b, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(dir, "hsctl")
	if err := os.WriteFile(exe, b, 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(installedEnv, "1")
	rerun(t, exe, nil, "from an installed copy")
	return false
}

// rerun runs just this test again from exe (as cred, when set) and reports that run's result.
func rerun(t *testing.T, exe string, cred *syscall.Credential, how string) {
	t.Helper()
	if strings.Contains(t.Name(), "/") {
		t.Fatal("a test re-run works on top-level tests only")
	}
	cmd := exec.Command(exe, "-test.run", "^"+regexp.QuoteMeta(t.Name())+"$", "-test.count=1", "-test.v")
	env := os.Environ()
	if cred != nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{Credential: cred}
		env = append(env, childEnv+"=1", "HOME=/tmp", "TMPDIR=/tmp")
	}
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s: %v\n%s", how, err, out)
	}
	switch {
	case strings.Contains(string(out), "--- PASS: "+t.Name()+" "):
		t.Logf("ran %s:\n%s", how, out)
	case strings.Contains(string(out), "--- SKIP: "+t.Name()+" "):
		t.Skipf("skipped %s:\n%s", how, out)
	default:
		t.Fatalf("the run %s didn't run %s:\n%s", how, t.Name(), out)
	}
}

// Chown hands a test folder (and everything in it) to uid:gid, as an admin's checkout would be.
func Chown(t testing.TB, dir string, uid, gid int) {
	t.Helper()
	err := filepath.Walk(dir, func(p string, _ os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		return os.Lchown(p, uid, gid)
	})
	if err != nil {
		t.Fatal(err)
	}
}

// Owner returns a file's uid:gid.
func Owner(t testing.TB, path string) string {
	t.Helper()
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	st := fi.Sys().(*syscall.Stat_t)
	return fmt.Sprintf("%d:%d", st.Uid, st.Gid)
}

// Systemctl is a stand-in systemctl first on PATH, for the tests of what hsctl asks systemd to do
// (the test container runs no systemd). It records each call, answers is-active as SetActive
// said, and fails the call FailOn names.
type Systemctl struct{ dir string }

// FakeSystemctl puts a recording systemctl first on PATH for the rest of the test.
func FakeSystemctl(t *testing.T) *Systemctl {
	t.Helper()
	dir := t.TempDir()
	script := `#!/bin/sh
d='` + dir + `'
echo "$*" >> "$d/calls"
if [ -f "$d/fail" ] && [ "$*" = "$(cat "$d/fail")" ]; then echo "systemctl: $* failed" >&2; exit 1; fi
case "$1" in
  is-active) cat "$d/active" 2>/dev/null || echo inactive ;;
  is-enabled) cat "$d/enabled" 2>/dev/null || echo not-found ;;
  show) echo "Mon 2026-10-26 03:30:00 UTC" ;;
esac
exit 0
`
	WriteFile(t, dir, "bin/systemctl", script)
	if err := os.Chmod(filepath.Join(dir, "bin/systemctl"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0755); err != nil { // the unprivileged re-runs use it too
		t.Fatal(err)
	}
	t.Setenv("PATH", filepath.Join(dir, "bin")+":"+os.Getenv("PATH"))
	return &Systemctl{dir: dir}
}

// Calls returns the systemctl calls so far, one "arg arg …" line each.
func (s *Systemctl) Calls() []string {
	b, _ := os.ReadFile(filepath.Join(s.dir, "calls"))
	if len(b) == 0 {
		return nil
	}
	return strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
}

// SetActive sets what `systemctl is-active` answers.
func (s *Systemctl) SetActive(active bool) {
	state := "inactive\n"
	if active {
		state = "active\n"
	}
	_ = os.WriteFile(filepath.Join(s.dir, "active"), []byte(state), 0644)
}

// SetEnabled sets what `systemctl is-enabled` answers ("enabled", "disabled", "not-found").
func (s *Systemctl) SetEnabled(state string) {
	_ = os.WriteFile(filepath.Join(s.dir, "enabled"), []byte(state+"\n"), 0644)
}

// FailOn makes the call with exactly these arguments fail.
func (s *Systemctl) FailOn(args string) {
	_ = os.WriteFile(filepath.Join(s.dir, "fail"), []byte(args+"\n"), 0644)
}

// Hsctl returns the real hsctl binary the harness built (HSCTL_BIN), skipping the test without it.
func Hsctl(t testing.TB) string {
	t.Helper()
	p := os.Getenv("HSCTL_BIN")
	if p == "" {
		t.Skip("needs the hsctl binary in HSCTL_BIN (make test-integration provides it)")
	}
	return p
}

// PTY opens a pseudo-terminal and returns its terminal end, for the tests of what hsctl does when a
// person is at a terminal. Both ends close when the test ends.
func PTY(t testing.TB) *os.File {
	t.Helper()
	m, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("no pseudo-terminals here: %v", err)
	}
	t.Cleanup(func() { m.Close() })
	if err := unix.IoctlSetPointerInt(int(m.Fd()), unix.TIOCSPTLCK, 0); err != nil {
		t.Fatal(err)
	}
	n, err := unix.IoctlGetInt(int(m.Fd()), unix.TIOCGPTN)
	if err != nil {
		t.Fatal(err)
	}
	s, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", n), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
