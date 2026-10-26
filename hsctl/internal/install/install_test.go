//go:build integration

package install_test

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Rishikesh01/homeserver/hsctl/internal/config"
	"github.com/Rishikesh01/homeserver/hsctl/internal/docker"
	"github.com/Rishikesh01/homeserver/hsctl/internal/install"
	"github.com/Rishikesh01/homeserver/hsctl/internal/itest"
	"github.com/Rishikesh01/homeserver/hsctl/internal/paths"
	"github.com/Rishikesh01/homeserver/hsctl/internal/priv"
	"github.com/Rishikesh01/homeserver/hsctl/internal/prompt"
	"github.com/Rishikesh01/homeserver/hsctl/internal/provision"
	"github.com/Rishikesh01/homeserver/hsctl/internal/secrets"
	"github.com/Rishikesh01/homeserver/hsctl/internal/stack"
)

// newInstall wires install for repo; input, when not nil, is what the person at the terminal types.
func newInstall(repo paths.Repo, input *string) (*install.Service, *bytes.Buffer) {
	var out bytes.Buffer
	dk := docker.New(repo.Root)
	dk.Stdout, dk.Stderr = &out, &out
	store := config.NewStore(repo, config.LocalHost{})
	prov := provision.New(repo, dk)
	st := stack.New(repo, dk, store, prov)
	st.Out, st.Err = &out, &out
	sec := secrets.New(repo, dk, priv.Root{})
	term := &prompt.Terminal{Out: &out, Interactive: func() bool { return false }}
	if input != nil {
		term = &prompt.Terminal{In: bufio.NewReader(strings.NewReader(*input)), Out: &out}
	}
	s := install.New(repo, dk, store, st, term, sec.UIPassword, prov.Configured)
	s.Out, s.Err = &out, &out
	return s, &out
}

func str(s string) *string { return &s }

// TestInstall runs from an installed binary, as root, against a real Docker (systemd is the one
// stand-in): the first install saves the settings, makes the dashboard password and starts Caddy;
// both runs write the units for this checkout and binary and enable the dashboard — and the
// backup timer once there's a backup destination.
func TestInstall(t *testing.T) {
	if !itest.Installed(t) {
		return
	}
	itest.NeedDocker(t)
	exe, _ := os.Executable()
	cases := []struct {
		name          string
		configured    bool
		backupConf    bool
		fromDashboard bool
		wantCalls     []string
		notCalls      []string
		wantOut       []string
	}{
		{"first install", false, false, false,
			[]string{"daemon-reload", "enable --now hsctl-ui.service", "try-restart hsctl-ui.service"},
			[]string{"enable --now hsctl-backup.timer"},
			[]string{"starting Caddy", "Finish setting up in your browser", "password  "}},
		{"re-run from the dashboard, backups set up", true, true, true,
			[]string{"daemon-reload", "enable --now hsctl-ui.service", "enable --now hsctl-backup.timer"},
			[]string{"try-restart hsctl-ui.service"},
			[]string{"Nightly backups: on", "restart the dashboard: sudo systemctl restart hsctl-ui"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sc := itest.FakeSystemctl(t)
			if err := os.MkdirAll(install.UnitDir, 0755); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				for _, u := range install.Units {
					os.Remove(filepath.Join(install.UnitDir, u))
				}
			})
			repo := itest.Checkout(t)
			s, out := newInstall(repo, nil)
			if tc.configured {
				c := config.NewStore(repo, config.LocalHost{}).Loaded()
				if _, err := provision.New(repo, docker.New(repo.Root)).Generate(c, false); err != nil {
					t.Fatal(err)
				}
			}
			if tc.backupConf {
				itest.WriteFile(t, repo.Root, paths.BackupConf, "RESTIC_REPO=/mnt/backup/restic\n")
			}
			if tc.fromDashboard {
				t.Setenv("HSCTL_FROM_DASHBOARD", "1")
			}
			if err := s.Install(); err != nil {
				t.Fatalf("%v\n%s", err, out.String())
			}
			for _, w := range tc.wantOut {
				if !strings.Contains(out.String(), w) {
					t.Errorf("output lacks %q:\n%s", w, out.String())
				}
			}
			calls := sc.Calls()
			for _, w := range tc.wantCalls {
				if !slices.Contains(calls, w) {
					t.Errorf("systemctl calls %v lack %q", calls, w)
				}
			}
			for _, n := range tc.notCalls {
				if slices.Contains(calls, n) {
					t.Errorf("systemctl was asked to %q", n)
				}
			}
			unit := itest.ReadFile(filepath.Join(install.UnitDir, "hsctl-ui.service"))
			if !strings.Contains(unit, repo.Root) || !strings.Contains(unit, exe) || strings.Contains(unit, "__") {
				t.Errorf("hsctl-ui.service isn't filled in:\n%s", unit)
			}
			for _, rel := range []string{paths.SetupConf, paths.UIPassword} {
				if o := itest.Owner(t, repo.Path(rel)); o != "1000:1000" {
					t.Errorf("%s owned by %s, want the admin", rel, o)
				}
			}
			if pw := strings.TrimSpace(itest.ReadFile(repo.Path(paths.UIPassword))); len(pw) < 16 || (!tc.configured && !strings.Contains(out.String(), pw)) {
				t.Errorf("dashboard password %q not made or not shown", pw)
			}
			if !tc.configured && docker.New("").ContainerState("caddy") != "running" {
				t.Error("Caddy isn't running after a first install")
			}
		})
	}
}

// TestInstallRefusals: only root installs, only from inside a checkout, and only a binary no normal
// user can replace — the root service would otherwise run their code.
func TestInstallRefusals(t *testing.T) {
	itest.NeedRoot(t)
	cases := []struct {
		name    string
		repo    func(t *testing.T) paths.Repo
		wantErr string
	}{
		{"binary a user can replace", func(t *testing.T) paths.Repo { return itest.Repo(t) }, "writable by a normal user"},
		{"not a checkout", func(t *testing.T) paths.Repo { return paths.Repo{Root: t.TempDir()} }, "not in the homeserver repo"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// The test binary lives in the harness's folder, which belongs to the host's user.
			if fi, err := os.Stat(os.Args[0]); err != nil || fi.Sys() == nil {
				t.Skip("can't inspect the test binary")
			}
			if itest.Owner(t, os.Args[0]) == "0:0" && tc.wantErr == "writable by a normal user" {
				t.Skip("the test binary is root's here")
			}
			s, _ := newInstall(tc.repo(t), nil)
			if err := s.Install(); err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("Install = %v, want %q", err, tc.wantErr)
			}
		})
	}
}

// TestNotRoot: install and uninstall say to use sudo.
func TestNotRoot(t *testing.T) {
	if !itest.Unprivileged(t) {
		return
	}
	cases := []struct {
		name string
		run  func(*install.Service) error
		want string
	}{
		{"install", (*install.Service).Install, "sudo hsctl install"},
		{"uninstall", func(s *install.Service) error { return s.Uninstall(false, false, true) }, "sudo hsctl uninstall"},
	}
	for _, tc := range cases {
		s, _ := newInstall(itest.Repo(t), nil)
		if err := tc.run(s); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v", tc.name, err)
		}
	}
}

// TestEnableBackupTimer switches the timer on once it's installed, and says what to run when that
// fails.
func TestEnableBackupTimer(t *testing.T) {
	itest.NeedRoot(t)
	cases := []struct {
		name      string
		installed bool
		fail      bool
		wantCall  bool
		wantOut   string
	}{
		{"not installed", false, false, false, ""},
		{"installed", true, false, true, "nightly backups: on"},
		{"systemctl fails", true, true, true, "run: sudo systemctl enable --now hsctl-backup.timer"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sc := itest.FakeSystemctl(t)
			if tc.fail {
				sc.FailOn("enable --now hsctl-backup.timer")
			}
			timer := filepath.Join(install.UnitDir, "hsctl-backup.timer")
			if tc.installed {
				itest.WriteFile(t, install.UnitDir, "hsctl-backup.timer", "[Timer]\n")
				t.Cleanup(func() { os.Remove(timer) })
			}
			s, out := newInstall(itest.Repo(t), nil)
			s.EnableBackupTimer()
			if got := slices.Contains(sc.Calls(), "enable --now hsctl-backup.timer"); got != tc.wantCall {
				t.Errorf("enabled = %v, want %v", got, tc.wantCall)
			}
			if !strings.Contains(out.String(), tc.wantOut) {
				t.Errorf("output %q lacks %q", out.String(), tc.wantOut)
			}
		})
	}
}

// TestUninstall runs from an installed binary: each tier removes the stack, the units, the
// generated config and hsctl itself — keeping the app data and backups unless told otherwise, and
// deleting data only after DESTROY is typed twice.
func TestUninstall(t *testing.T) {
	if !itest.Installed(t) {
		return
	}
	itest.NeedDocker(t)
	itest.Pull(t, itest.Busybox)
	cases := []struct {
		name                 string
		data, all, yes       bool
		input                *string
		wantErr              string
		wantVolumes, wantBak bool
	}{
		{"needs a confirmation", false, false, false, nil, "re-run with --yes", true, true},
		{"data needs a terminal", true, false, true, nil, "needs an interactive terminal", true, true},
		{"data needs DESTROY twice", true, false, false, str("DESTROY\nno\n"), "aborted", true, true},
		{"keeps data and backups", false, false, true, nil, "", true, true},
		{"deletes the data", true, false, false, str("DESTROY\nDESTROY\n"), "", false, true},
		{"deletes data and backups", false, true, false, str("DESTROY\nDESTROY\n"), "", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sc := itest.FakeSystemctl(t)
			exe, _ := os.Executable()
			bin, err := os.ReadFile(exe)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.WriteFile(exe, bin, 0755) }) // the uninstall removes it
			repo := itest.Checkout(t)
			c := config.NewStore(repo, config.LocalHost{}).Loaded()
			if _, err := provision.New(repo, docker.New(repo.Root)).Generate(c, false); err != nil {
				t.Fatal(err)
			}
			// The apps run busybox here: what matters is that they're the stack's compose projects.
			for _, svc := range []string{"caddy", "vaultwarden"} {
				itest.WriteFile(t, repo.Root, svc+"/docker-compose.override.yml",
					"services:\n  "+svc+":\n    image: "+itest.Busybox+"\n    command: [\"sleep\", \"3600\"]\n    healthcheck: {disable: true}\n")
			}
			dk := docker.New(repo.Root)
			if err := dk.EnsureNetwork("homeserver-edge"); err != nil {
				t.Fatal(err)
			}
			for _, svc := range []string{"caddy", "vaultwarden"} {
				if o, err := dk.Combined(repo.Path(svc), "compose", "up", "-d"); err != nil {
					t.Fatalf("up %s: %v\n%s", svc, err, o)
				}
			}
			itest.Docker(t, "volume", "create", "nextcloud_db-data") // a volume an older compose file left
			itest.Docker(t, "volume", "create", "unrelated_volume")
			t.Cleanup(func() { _ = dk.Quiet("", "volume", "rm", "-f", "nextcloud_db-data", "unrelated_volume") })
			itest.WriteFile(t, repo.Root, paths.BackupConf, "RESTIC_REPO=/srv/restic\n")
			itest.WriteFile(t, repo.Root, "backups/restic/config", "x")
			itest.WriteFile(t, install.UnitDir, "hsctl-ui.service", "[Unit]\n")

			s, out := newInstall(repo, tc.input)
			err = s.Uninstall(tc.data, tc.all, tc.yes)
			if (err == nil) != (tc.wantErr == "") || (err != nil && !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("Uninstall = %v, want %q\n%s", err, tc.wantErr, out.String())
			}
			if tc.wantErr != "" {
				if dk.ContainerState("caddy") != "running" || !itest.DockerOK("volume", "inspect", "vaultwarden_vw-data") {
					t.Error("a refused uninstall touched the stack")
				}
				return
			}
			cases := []struct {
				what string
				got  bool
				want bool
			}{
				{"Caddy container", itest.DockerOK("inspect", "caddy"), false},
				{"Vaultwarden container", itest.DockerOK("inspect", "vaultwarden"), false},
				{"edge network", itest.DockerOK("network", "inspect", "homeserver-edge"), false},
				{"unit file", fileExists(filepath.Join(install.UnitDir, "hsctl-ui.service")), false},
				{"generated .env", fileExists(repo.Path("vaultwarden/.env")), false},
				{"setup.conf", fileExists(repo.Path(paths.SetupConf)), false},
				{"hsctl binary", fileExists(exe), false},
				{"compose files", fileExists(repo.Path("caddy/docker-compose.yml")), true},
				{"Vaultwarden data", itest.DockerOK("volume", "inspect", "vaultwarden_vw-data"), tc.wantVolumes},
				{"old project's volume", itest.DockerOK("volume", "inspect", "nextcloud_db-data"), tc.wantVolumes},
				{"unrelated volume", itest.DockerOK("volume", "inspect", "unrelated_volume"), true},
				{"backups", fileExists(repo.Path("backups/restic/config")), tc.wantBak},
				{"backup settings", fileExists(repo.Path(paths.BackupConf)), tc.wantBak},
			}
			for _, c := range cases {
				if c.got != c.want {
					t.Errorf("%s present = %v, want %v\n%s", c.what, c.got, c.want, out.String())
				}
			}
			if !slices.Contains(sc.Calls(), "disable --now hsctl-ui.service") {
				t.Errorf("systemctl calls: %v", sc.Calls())
			}
		})
	}
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

// TestInstallableBinary refuses a binary a normal user could replace, and takes an installed one.
func TestInstallableBinary(t *testing.T) {
	if !itest.Installed(t) {
		return
	}
	cases := []struct {
		name    string
		perm    os.FileMode
		wantErr bool
	}{
		{"installed", 0755, false},
		{"group-writable", 0775, true},
	}
	exe, _ := os.Executable()
	for _, tc := range cases {
		if err := os.Chmod(exe, tc.perm); err != nil {
			t.Fatal(err)
		}
		got, err := install.InstallableBinary()
		if (err != nil) != tc.wantErr || (err == nil && got != exe) {
			t.Errorf("%s: InstallableBinary = %q, %v", tc.name, got, err)
		}
	}
	_ = os.Chmod(exe, 0755)
}
