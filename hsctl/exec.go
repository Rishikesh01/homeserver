package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// requireRepoDir resolves the homeserver repo and errors — instead of silently using the
// cwd — when we're not actually in it, so repo-dependent commands (backup, secrets, …)
// don't operate on the wrong directory. Honors $HOMESERVER_DIR; else run from the repo.
func requireRepoDir() (string, error) {
	repo := repoDir()
	if !isRepo(repo) {
		return "", fmt.Errorf("not in the homeserver repo (resolved to %s)\n"+
			"run from the repo directory, or set HOMESERVER_DIR:\n"+
			"  cd /path/to/homeserver && hsctl ...\n"+
			"  # or: HOMESERVER_DIR=/path/to/homeserver hsctl ...", repo)
	}
	return repo, nil
}

// repoDir returns the homeserver repo root (the folder holding the service dirs).
// It honors $HOMESERVER_DIR, else walks up from cwd looking for caddy/docker-compose.yml,
// else falls back to the parent of the executable.
func repoDir() string {
	if d := os.Getenv("HOMESERVER_DIR"); d != "" {
		return d
	}
	if d, _ := os.Getwd(); d != "" {
		for {
			if isRepo(d) {
				return d
			}
			parent := filepath.Dir(d)
			if parent == d {
				break
			}
			d = parent
		}
	}
	if exe, err := os.Executable(); err == nil {
		if up := filepath.Dir(filepath.Dir(exe)); isRepo(up) {
			return up
		}
	}
	wd, _ := os.Getwd()
	return wd
}

func isRepo(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, "caddy", "docker-compose.yml"))
	return err == nil
}

// dockerSudo caches whether the docker daemon needs sudo from this user. The web UI probes
// this from many request goroutines at once, so the one-time detection is guarded by Once —
// otherwise concurrent first calls would race on the cache variable.
var (
	dockerSudoOnce sync.Once
	dockerSudo     bool
)

func dockerNeedsSudo() bool {
	dockerSudoOnce.Do(func() {
		dockerSudo = exec.Command("docker", "info").Run() != nil
	})
	return dockerSudo
}

// dockerCmd builds a docker (or sudo docker) command rooted at dir.
func dockerCmd(dir string, args ...string) *exec.Cmd {
	name := "docker"
	if dockerNeedsSudo() {
		name, args = "sudo", append([]string{"docker"}, args...)
	}
	c := exec.Command(name, args...)
	c.Dir = dir
	return c
}

// firstLocalImage returns a repository:tag of some image already present on the box
// (skipping dangling <none> ones), or "" if there are none. Used as an offline-safe
// default fixture image for `backup verify`.
func firstLocalImage(dir string) string {
	out, err := dockerOut(dir, "image", "ls", "--format", "{{.Repository}}:{{.Tag}}")
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

// containerImage returns the image a (running or stopped) container was created from,
// e.g. "postgres:16-alpine" for nextcloud-db. "" if the container doesn't exist. Lets
// the self-test use the EXACT images the live services run.
func containerImage(dir, name string) string {
	out, err := dockerOut(dir, "inspect", "-f", "{{.Config.Image}}", name)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// volumeMountpoint resolves a docker volume's host mountpoint. Empty output (a volume
// docker knows but can't place) is as unusable as a failed inspect, so it errors too.
func volumeMountpoint(repo, name string) (string, error) {
	mp, err := dockerOut(repo, "volume", "inspect", "-f", "{{.Mountpoint}}", name)
	if err != nil {
		return "", fmt.Errorf("inspect volume %s: %w", name, err)
	}
	if mp == "" {
		return "", fmt.Errorf("inspect volume %s: empty mountpoint", name)
	}
	return mp, nil
}

// containerState returns a container's state ("running", "exited", …), or "" if missing. It's
// a probe — "no such object" is an answer, so docker's stderr is swallowed rather than streamed.
func containerState(dir, name string) string {
	out, err := dockerCmd(dir, "inspect", "-f", "{{.State.Status}}", name).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// dockerRun streams a docker command's output to the user.
func dockerRun(dir string, args ...string) error {
	c := dockerCmd(dir, args...)
	c.Stdout, c.Stderr, c.Stdin = os.Stdout, os.Stderr, os.Stdin
	return c.Run()
}

// dockerOut captures stdout of a docker command (stderr still streams).
func dockerOut(dir string, args ...string) (string, error) {
	c := dockerCmd(dir, args...)
	c.Stderr = os.Stderr
	out, err := c.Output()
	return strings.TrimSpace(string(out)), err
}

// dockerCombined captures stdout+stderr together (for the web UI to display).
func dockerCombined(dir string, args ...string) (string, error) {
	out, err := dockerCmd(dir, args...).CombinedOutput()
	return string(out), err
}
