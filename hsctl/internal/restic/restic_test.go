//go:build integration

package restic_test

import (
	"bytes"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Rishikesh01/homeserver/hsctl/internal/itest"
	"github.com/Rishikesh01/homeserver/hsctl/internal/restic"
)

// TestRepository runs real restic: create a repository, snapshot into it, list it — and tell "no
// repository there" apart from a wrong password, which must never be treated as "create one".
func TestRepository(t *testing.T) {
	itest.NeedRestic(t)
	dir := t.TempDir()
	repo := filepath.Join(dir, "repo")
	pass := itest.WriteFile(t, dir, "pass", "correct horse battery staple\n")
	wrong := itest.WriteFile(t, dir, "wrong", "not the password\n")
	data := itest.WriteFile(t, dir, "data/file.txt", "hello")
	var out bytes.Buffer
	c := restic.New(pass, func() []string { return []string{"AWS_ACCESS_KEY_ID=key"} })
	c.Stdout, c.Stderr = &out, &out
	w := restic.New(wrong, nil)
	steps := []struct {
		name        string
		run         func() (string, error)
		wantErr     bool
		wantMissing bool
		wantOut     string
	}{
		{"snapshots before init: no repository", func() (string, error) { return c.Output(repo, "snapshots") }, true, true, ""},
		{"init", func() (string, error) { err := c.Run(repo, "init"); return out.String(), err }, false, false, "created restic repository"},
		{"backup", func() (string, error) {
			err := c.Run(repo, "backup", "--tag", "homeserver", data)
			return out.String(), err
		}, false, false, "snapshot"},
		{"snapshots", func() (string, error) { return c.Output(repo, "snapshots") }, false, false, "homeserver"},
		{"wrong password is not a missing repository", func() (string, error) { return w.Output(repo, "snapshots") }, true, false, "wrong password"},
	}
	for _, st := range steps {
		got, err := st.run()
		if (err != nil) != st.wantErr {
			t.Fatalf("%s: err = %v, want error %v\n%s", st.name, err, st.wantErr, got)
		}
		if restic.RepoMissing(got) != st.wantMissing {
			t.Errorf("%s: RepoMissing = %v, want %v\n%s", st.name, !st.wantMissing, st.wantMissing, got)
		}
		if !strings.Contains(strings.ToLower(got), st.wantOut) {
			t.Errorf("%s: output lacks %q:\n%s", st.name, st.wantOut, got)
		}
	}
	env := c.Env(repo)
	for _, want := range []string{"RESTIC_REPOSITORY=" + repo, "RESTIC_PASSWORD_FILE=" + pass, "AWS_ACCESS_KEY_ID=key"} {
		if !slices.Contains(env, want) {
			t.Errorf("Env lacks %s", want)
		}
	}
}

// TestInstalled: the version is restic's own, and a box without restic is told how to install it.
func TestInstalled(t *testing.T) {
	itest.NeedRestic(t)
	raw, err := exec.Command("restic", "version").Output()
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name      string
		path      string // PATH for the check ("" = unchanged)
		installed bool
	}{
		{"installed", "", true},
		{"missing", t.TempDir(), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.path != "" {
				t.Setenv("PATH", tc.path)
			}
			c := restic.New("", nil)
			if c.Installed() != tc.installed {
				t.Fatalf("Installed = %v, want %v", c.Installed(), tc.installed)
			}
			err := c.Require()
			if (err == nil) != tc.installed || (err != nil && !strings.Contains(err.Error(), "apt-get install -y restic")) {
				t.Errorf("Require = %v", err)
			}
			v, err := c.Version()
			if tc.installed && (err != nil || !strings.HasPrefix(string(raw), "restic "+v+" ")) {
				t.Errorf("Version = %q, %v; restic says %q", v, err, raw)
			}
			if !tc.installed && err == nil {
				t.Error("Version without restic must fail")
			}
		})
	}
}
