package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

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

// dockerSudo caches whether the docker daemon needs sudo from this user.
var dockerSudo *bool

func dockerNeedsSudo() bool {
	if dockerSudo == nil {
		err := exec.Command("docker", "info").Run()
		v := err != nil
		dockerSudo = &v
	}
	return *dockerSudo
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

// containerState returns a container's state ("running", "exited", …), or "" if missing.
func containerState(dir, name string) string {
	out, err := dockerOut(dir, "inspect", "-f", "{{.State.Status}}", name)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
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
