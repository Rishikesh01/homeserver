//go:build integration

package backup_test

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/Rishikesh01/homeserver/hsctl/internal/backup"
	"github.com/Rishikesh01/homeserver/hsctl/internal/itest"
	"github.com/Rishikesh01/homeserver/hsctl/internal/paths"
)

// TestImageFailures: a backup asked to carry the app images stops — before anything is saved — when
// one of them can't be: an app that's switched on without its image, or the disk filling up mid-save.
// Only a switched-off app's image, never pulled, may be left out.
func TestImageFailures(t *testing.T) {
	itest.NeedRoot(t)
	itest.NeedDocker(t)
	itest.NeedRestic(t)
	itest.Pull(t, itest.Busybox)
	missing := "hsctl-itest/missing:" + strings.TrimPrefix(itest.Name("t"), "hsctl-it-t-")
	pin := func(t *testing.T, repo paths.Repo, ref string) {
		itest.WriteFile(t, repo.Root, "it-tools/docker-compose.yml", "services:\n  it-tools:\n    image: ${ITTOOLS_IMAGE:-"+ref+"}\n")
	}
	cases := []struct {
		name    string
		prep    func(t *testing.T, repo paths.Repo)
		wantErr string // "" = the backup is taken
		wantOut string
	}{
		{"an app that's switched on without its image", func(t *testing.T, repo paths.Repo) { pin(t, repo, missing) },
			"isn't on this machine, so it can't be saved — the backup was stopped before anything was saved", ""},
		{"the disk fills up while saving", func(t *testing.T, repo paths.Repo) {
			pin(t, repo, itest.Busybox)
			staging := repo.Path(paths.Staging)
			if err := os.MkdirAll(staging, 0700); err != nil {
				t.Fatal(err)
			}
			if out, err := exec.Command("mount", "-t", "tmpfs", "-o", "size=1m", "tmpfs", staging).CombinedOutput(); err != nil {
				t.Skipf("can't mount a tmpfs here: %v %s", err, out)
			}
			t.Cleanup(func() { _ = exec.Command("umount", staging).Run() })
		}, "saving " + itest.Busybox + " failed — the backup was stopped before anything was saved", ""},
		{"a switched-off app that never pulled its image", func(t *testing.T, repo paths.Repo) {
			pin(t, repo, missing)
			itest.WriteFile(t, repo.Root, paths.SetupConf, "DISABLED_APPS=it-tools\n")
		}, "", "not saved: " + missing + " isn't on this machine (it-tools is switched off)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := itest.Repo(t)
			s, out := newService(t, repo)
			initialized(true)(t, repo, s)
			yes := true
			if err := s.Configure(backup.Options{Images: &yes}); err != nil {
				t.Fatal(err)
			}
			vol := "stirling_" + itest.Name("vol") // a stack volume, so there's something to back up
			itest.Docker(t, "volume", "create", vol)
			t.Cleanup(func() { _ = exec.Command("docker", "volume", "rm", "-f", vol).Run() })
			tc.prep(t, repo)
			out.Reset()

			err := s.Run(backup.RunOptions{})
			if (err == nil) != (tc.wantErr == "") || (err != nil && !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("Run = %v, want %q\n%s", err, tc.wantErr, out.String())
			}
			if !strings.Contains(out.String(), tc.wantOut) {
				t.Errorf("output lacks %q:\n%s", tc.wantOut, out.String())
			}
			snaps, serr := s.Restic.Output(s.Settings().Repo, "snapshots", "--json")
			if taken := strings.TrimSpace(snaps) != "[]"; serr != nil || taken != (tc.wantErr == "") {
				t.Errorf("snapshots after the run: %v\n%s", serr, snaps)
			}
			if _, err := os.Stat(repo.Path(paths.Staging, backup.ImageArchiveDir)); err == nil {
				t.Error("the saved images were left in staging")
			}
		})
	}
}
