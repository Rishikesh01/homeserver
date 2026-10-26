//go:build integration

package paths_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Rishikesh01/homeserver/hsctl/internal/itest"
	"github.com/Rishikesh01/homeserver/hsctl/internal/paths"
)

// TestRequire: a folder is a homeserver checkout when it has caddy/docker-compose.yml; anything else
// is refused with a message that says how to point hsctl at the right one.
func TestRequire(t *testing.T) {
	cases := []struct {
		name   string
		layout func(t *testing.T) string
		isRepo bool
	}{
		{"checkout", func(t *testing.T) string { return itest.Repo(t).Root }, true},
		{"empty folder", func(t *testing.T) string { return t.TempDir() }, false},
		{"caddy folder without its compose file", func(t *testing.T) string {
			d := t.TempDir()
			if err := os.Mkdir(filepath.Join(d, "caddy"), 0755); err != nil {
				t.Fatal(err)
			}
			return d
		}, false},
		{"missing folder", func(t *testing.T) string { return filepath.Join(t.TempDir(), "nope") }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := tc.layout(t)
			if got := paths.IsRepo(dir); got != tc.isRepo {
				t.Errorf("IsRepo = %v, want %v", got, tc.isRepo)
			}
			err := paths.Repo{Root: dir}.Require()
			if (err == nil) != tc.isRepo {
				t.Fatalf("Require = %v, want error %v", err, !tc.isRepo)
			}
			if err != nil && (!strings.Contains(err.Error(), dir) || !strings.Contains(err.Error(), "HOMESERVER_DIR")) {
				t.Errorf("the error should name the folder and HOMESERVER_DIR: %v", err)
			}
		})
	}
}

// TestGuess: $HOMESERVER_DIR wins; otherwise the nearest checkout at or above the working directory;
// otherwise the working directory itself (Require then refuses it).
func TestGuess(t *testing.T) {
	repo := itest.Repo(t).Root
	sub := filepath.Join(repo, "nextcloud", "deep")
	if err := os.MkdirAll(sub, 0755); err != nil {
		t.Fatal(err)
	}
	elsewhere := t.TempDir()
	cases := []struct {
		name, env, cwd, want string
	}{
		{"HOMESERVER_DIR", repo, elsewhere, repo},
		{"HOMESERVER_DIR even when not a checkout", elsewhere, repo, elsewhere},
		{"at the checkout's root", "", repo, repo},
		{"in a subfolder", "", sub, repo},
		{"outside any checkout", "", elsewhere, elsewhere},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOMESERVER_DIR", tc.env)
			t.Chdir(tc.cwd)
			got, _ := filepath.EvalSymlinks(paths.Guess().Root)
			want, _ := filepath.EvalSymlinks(tc.want)
			if got != want {
				t.Errorf("Guess = %s, want %s", got, want)
			}
		})
	}
}

// TestPath joins repo-relative paths, and the files hsctl owns are all inside the checkout.
func TestPath(t *testing.T) {
	r := paths.Repo{Root: "/srv/homeserver"}
	cases := []struct {
		elem []string
		want string
	}{
		{[]string{paths.SetupConf}, "/srv/homeserver/setup.conf"},
		{[]string{"caddy", ".env"}, "/srv/homeserver/caddy/.env"},
		{[]string{paths.LastBackup}, "/srv/homeserver/backups/.last-backup"},
		{nil, "/srv/homeserver"},
	}
	for _, tc := range cases {
		if got := r.Path(tc.elem...); got != tc.want {
			t.Errorf("Path(%q) = %s, want %s", tc.elem, got, tc.want)
		}
	}
	for _, rel := range append(append([]string{}, paths.Owned...), paths.EnvFiles...) {
		if filepath.IsAbs(rel) || strings.HasPrefix(filepath.Clean(rel), "..") {
			t.Errorf("%s isn't repo-relative", rel)
		}
	}
}
