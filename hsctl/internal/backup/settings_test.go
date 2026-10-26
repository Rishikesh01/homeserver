//go:build integration

package backup_test

import (
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/Rishikesh01/homeserver/hsctl/internal/backup"
	"github.com/Rishikesh01/homeserver/hsctl/internal/itest"
	"github.com/Rishikesh01/homeserver/hsctl/internal/paths"
)

func ptr(s string) *string { return &s }

// TestSettings: what SaveSettings writes, Settings reads back — and Configure changes only what it's
// given, gives an external-disk destination its guard, and refuses a bad retention policy.
func TestSettings(t *testing.T) {
	itest.NeedRestic(t)
	ver, _ := exec.Command("restic", "version").Output()
	cases := []struct {
		name    string
		steps   []backup.Options
		want    backup.Settings
		wantErr string
		wantOut string
	}{
		{"defaults", nil, backup.Settings{Retention: backup.DefaultRetention}, "", ""},
		{"everything", []backup.Options{{Repo: ptr("/srv/restic"), Retention: ptr("--keep-last 3"), Replica: ptr("sftp:nas:/b"),
			NotifyURL: ptr("https://ntfy.sh/x"), RequireMount: ptr("/srv")}},
			backup.Settings{Repo: "/srv/restic", Retention: "--keep-last 3", ReplicaRepo: "sftp:nas:/b", NotifyURL: "https://ntfy.sh/x", RequireMount: "/srv"},
			"", "saved backup.conf (repo=/srv/restic)"},
		{"cleared again", []backup.Options{{Repo: ptr("/srv/restic"), Replica: ptr("sftp:nas:/b"), NotifyURL: ptr("https://x")},
			{Replica: ptr(""), NotifyURL: ptr("")}},
			backup.Settings{Repo: "/srv/restic", Retention: backup.DefaultRetention}, "", ""},
		{"an external disk gets its guard", []backup.Options{{Repo: ptr("/mnt/usb/restic")}},
			backup.Settings{Repo: "/mnt/usb/restic", Retention: backup.DefaultRetention, RequireMount: "/mnt/usb"}, "", "set the backup-disk guard to /mnt/usb"},
		{"the guard follows the disk", []backup.Options{{Repo: ptr("/mnt/usb/restic")}, {Repo: ptr("/mnt/big/restic")}},
			backup.Settings{Repo: "/mnt/big/restic", Retention: backup.DefaultRetention, RequireMount: "/mnt/big"}, "", ""},
		{"a guard that protects nothing is pointed out", []backup.Options{{Repo: ptr("/srv/restic"), RequireMount: ptr("/mnt/usb")}},
			backup.Settings{Repo: "/srv/restic", Retention: backup.DefaultRetention, RequireMount: "/mnt/usb"}, "", "isn't on the guarded disk"},
		{"pin restic", []backup.Options{{PinRestic: true}},
			backup.Settings{Retention: backup.DefaultRetention, ResticVersion: strings.Fields(string(ver))[1]}, "", "pinned restic version"},
		{"a bad policy", []backup.Options{{Retention: ptr("keep 7 daily")}}, backup.Settings{Retention: backup.DefaultRetention}, "retention", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := itest.Repo(t)
			s, out := newService(t, repo)
			var err error
			for _, o := range tc.steps {
				if err = s.Configure(o); err != nil {
					break
				}
			}
			if (err == nil) != (tc.wantErr == "") || (err != nil && !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("Configure = %v, want %q", err, tc.wantErr)
			}
			want := tc.want
			if want.Repo == "" { // the default: a local repository inside the checkout
				want.Repo = repo.Path("backups", "restic")
			}
			if got := s.Settings(); got != want {
				t.Errorf("settings = %+v, want %+v", got, want)
			}
			if !strings.Contains(out.String(), tc.wantOut) {
				t.Errorf("output lacks %q:\n%s", tc.wantOut, out.String())
			}
			if err := s.SaveSettings(want); err != nil {
				t.Fatal(err)
			}
			if got := s.Settings(); got != want {
				t.Errorf("round-trip = %+v", got)
			}
		})
	}
}

// TestCredentialsEnv hands .backup-env's credentials to restic, and copes without the file.
func TestCredentialsEnv(t *testing.T) {
	cases := []struct {
		name, file string
		want       []string
	}{
		{"credentials", "AWS_ACCESS_KEY_ID=abc\nAWS_SECRET_ACCESS_KEY=def\n", []string{"AWS_ACCESS_KEY_ID=abc", "AWS_SECRET_ACCESS_KEY=def"}},
		{"no file", "", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := itest.Repo(t)
			if tc.file != "" {
				itest.WriteFile(t, repo.Root, paths.BackupEnv, tc.file)
			}
			env := backup.CredentialsEnv(repo)()
			slices.Sort(env)
			if !slices.Equal(env, tc.want) {
				t.Errorf("env = %v, want %v", env, tc.want)
			}
		})
	}
}
