//go:build integration

package secrets_test

import (
	"bytes"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Rishikesh01/homeserver/hsctl/internal/config"
	"github.com/Rishikesh01/homeserver/hsctl/internal/docker"
	"github.com/Rishikesh01/homeserver/hsctl/internal/envfile"
	"github.com/Rishikesh01/homeserver/hsctl/internal/install"
	"github.com/Rishikesh01/homeserver/hsctl/internal/itest"
	"github.com/Rishikesh01/homeserver/hsctl/internal/paths"
	"github.com/Rishikesh01/homeserver/hsctl/internal/priv"
	"github.com/Rishikesh01/homeserver/hsctl/internal/provision"
	"github.com/Rishikesh01/homeserver/hsctl/internal/secrets"
	"golang.org/x/crypto/argon2"
)

func newSecrets(repo paths.Repo) (*secrets.Service, *bytes.Buffer) {
	var out bytes.Buffer
	dk := docker.New(repo.Root)
	dk.Stdout, dk.Stderr = &out, &out
	s := secrets.New(repo, dk, priv.Root{})
	s.Out = &out
	s.DashboardActive = install.DashboardActive
	return s, &out
}

// configured is a checkout with every .env generated.
func configured(t *testing.T) paths.Repo {
	t.Helper()
	repo := itest.Checkout(t)
	c := config.NewStore(repo, config.LocalHost{}).Loaded()
	c.ServerIP = "10.0.0.5"
	if _, err := provision.New(repo, docker.New(repo.Root)).Generate(c, false); err != nil {
		t.Fatal(err)
	}
	return repo
}

// TestShow prints each login from the live files — never a hash as if it were a login, and a
// placeholder copied from an .env.example flagged as one.
func TestShow(t *testing.T) {
	cases := []struct {
		name    string
		prep    func(t *testing.T) paths.Repo
		wantOut []string
		notOut  []string
	}{
		{"not set up", func(t *testing.T) paths.Repo { return itest.Repo(t) }, []string{"No logins found"}, nil},
		{"set up", func(t *testing.T) paths.Repo {
			repo := configured(t)
			itest.WriteFile(t, repo.Root, paths.ResticPassword, "restic-pw-123456\n")
			itest.WriteFile(t, repo.Root, paths.UIPassword, "ui-pw-12345678\n")
			return repo
		}, []string{"(Argon2-hashed; shown once at setup", "Nextcloud (user 'admin'):", "Pi-hole admin:",
			"restic-pw-123456 (keep a copy off this machine)", "ui-pw-12345678 (user 'admin')", "GENERATED AT SETUP"},
			[]string{"argon2id$"}},
		{"placeholder", func(t *testing.T) paths.Repo {
			repo := itest.Repo(t)
			itest.WriteFile(t, repo.Root, "pihole/.env", "PIHOLE_PASSWORD=change-me\n")
			return repo
		}, []string{"change-me   <- a PLACEHOLDER from the .env.example"}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, out := newSecrets(tc.prep(t))
			if err := s.Show(); err != nil {
				t.Fatal(err)
			}
			for _, w := range tc.wantOut {
				if !strings.Contains(out.String(), w) {
					t.Errorf("output lacks %q:\n%s", w, out.String())
				}
			}
			for _, n := range tc.notOut {
				if strings.Contains(out.String(), n) {
					t.Errorf("output shows %q:\n%s", n, out.String())
				}
			}
		})
	}
}

// TestShowUnreadable runs as a normal user: files left root-owned are named, with the fix, never
// read as "not set up".
func TestShowUnreadable(t *testing.T) {
	if !itest.Unprivileged(t) {
		return
	}
	repo := itest.Repo(t)
	cases := []string{"nextcloud/.env", paths.ResticPassword}
	for _, rel := range cases {
		itest.WriteFile(t, repo.Root, rel, "NC_ADMIN_PASSWORD=x\n")
		if err := os.Chmod(repo.Path(rel), 0); err != nil {
			t.Fatal(err)
		}
	}
	s, out := newSecrets(repo)
	err := s.Show()
	if err == nil || !strings.Contains(err.Error(), "can't read nextcloud/.env, .restic-password") {
		t.Fatalf("Show = %v", err)
	}
	for _, rel := range cases {
		if !strings.Contains(out.String(), "(can't read "+rel+")") {
			t.Errorf("output doesn't name %s:\n%s", rel, out.String())
		}
	}
}

// TestUIPassword: $HSCTL_UI_PASSWORD wins, then the saved file; the first use makes and saves one.
func TestUIPassword(t *testing.T) {
	cases := []struct {
		name, env, file string
		want            string // "" = a new random one
	}{
		{"environment", "from-env-123", "from-file-123\n", "from-env-123"},
		{"saved", "", "from-file-123\n", "from-file-123"},
		{"first use", "", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HSCTL_UI_PASSWORD", tc.env)
			repo := itest.Repo(t)
			if tc.file != "" {
				itest.WriteFile(t, repo.Root, paths.UIPassword, tc.file)
			}
			s, _ := newSecrets(repo)
			got, err := s.UIPassword()
			if err != nil {
				t.Fatal(err)
			}
			if tc.want != "" && got != tc.want {
				t.Errorf("UIPassword = %q, want %q", got, tc.want)
			}
			if tc.want == "" {
				if len(got) != 16 || strings.TrimSpace(itest.ReadFile(repo.Path(paths.UIPassword))) != got {
					t.Errorf("first use: %q, file %q", got, itest.ReadFile(repo.Path(paths.UIPassword)))
				}
				if again, _ := s.UIPassword(); again != got {
					t.Error("a second call made another password")
				}
			}
		})
	}
}

// TestUIPasswordUnreadable runs as a normal user: a password file it can't read is reported, never
// silently replaced (the running dashboard would keep the old one).
func TestUIPasswordUnreadable(t *testing.T) {
	if !itest.Unprivileged(t) {
		return
	}
	t.Setenv("HSCTL_UI_PASSWORD", "")
	repo := itest.Repo(t)
	itest.WriteFile(t, repo.Root, paths.UIPassword, "secret\n")
	if err := os.Chmod(repo.Path(paths.UIPassword), 0); err != nil {
		t.Fatal(err)
	}
	cases := []string{"can't read .ui-password"}
	s, _ := newSecrets(repo)
	for _, want := range cases {
		if _, err := s.UIPassword(); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("UIPassword = %v, want %q", err, want)
		}
	}
}

// TestSetUIPassword writes the new password and gets it in front of the running dashboard: restarts
// the service, or says how when this run IS the dashboard (restarting would cut it off).
func TestSetUIPassword(t *testing.T) {
	itest.NeedRoot(t)
	cases := []struct {
		name          string
		pw            string
		active        bool
		fromDashboard bool
		failRestart   bool
		wantErr       string
		wantOut       string
		wantRestart   bool
	}{
		{"too short", "short", false, false, false, "at least 8 characters", "", false},
		{"no service", "long-enough-1", false, false, false, "", "the next `hsctl ui`", false},
		{"service restarted", "long-enough-2", true, false, false, "", "Restarted the dashboard", true},
		{"from the dashboard", "long-enough-3", true, true, false, "", "takes effect when the dashboard restarts", false},
		{"restart fails", "long-enough-4", true, false, true, "restart the dashboard", "", true},
		{"random", "", false, false, false, "", "password ", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sc := itest.FakeSystemctl(t)
			sc.SetActive(tc.active)
			if tc.failRestart {
				sc.FailOn("restart hsctl-ui.service")
			}
			if tc.fromDashboard {
				t.Setenv("HSCTL_FROM_DASHBOARD", "1")
			}
			repo := itest.Checkout(t)
			s, out := newSecrets(repo)
			err := s.SetUIPassword(tc.pw)
			if (err == nil) != (tc.wantErr == "") || (err != nil && !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("SetUIPassword = %v, want %q", err, tc.wantErr)
			}
			if !strings.Contains(out.String(), tc.wantOut) {
				t.Errorf("output %q lacks %q", out.String(), tc.wantOut)
			}
			if got := slices.Contains(sc.Calls(), "restart hsctl-ui.service"); got != tc.wantRestart {
				t.Errorf("restarted = %v, want %v (%v)", got, tc.wantRestart, sc.Calls())
			}
			if tc.wantErr == "" || tc.failRestart {
				saved := strings.TrimSpace(itest.ReadFile(repo.Path(paths.UIPassword)))
				if (tc.pw != "" && saved != tc.pw) || len(saved) < 8 || itest.Owner(t, repo.Path(paths.UIPassword)) != "1000:1000" {
					t.Errorf("saved %q owned by %s", saved, itest.Owner(t, repo.Path(paths.UIPassword)))
				}
			}
		})
	}
}

// TestRotateVWAdmin issues a new admin token for a real Vaultwarden: only its hash is stored, the
// container is recreated with it, and the printed token verifies against it.
func TestRotateVWAdmin(t *testing.T) {
	itest.NeedRoot(t)
	itest.NeedDocker(t)
	cases := []struct {
		name    string
		prep    func(t *testing.T) paths.Repo
		wantErr string
	}{
		{"not set up", func(t *testing.T) paths.Repo { return itest.Repo(t) }, "run: hsctl setup"},
		{"running Vaultwarden", configured, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := tc.prep(t)
			s, out := newSecrets(repo)
			before := itest.ReadFile(repo.Path("vaultwarden/.env"))
			err := s.RotateVWAdmin()
			if (err == nil) != (tc.wantErr == "") || (err != nil && !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("RotateVWAdmin = %v, want %q\n%s", err, tc.wantErr, out.String())
			}
			if tc.wantErr != "" {
				return
			}
			token := ""
			for _, line := range strings.Split(out.String(), "\n") {
				if l := strings.TrimSpace(line); len(l) == 40 && !strings.ContainsAny(l, " :") {
					token = l
				}
			}
			kv, _ := envfile.Read(repo.Path("vaultwarden/.env"))
			stored := strings.ReplaceAll(kv["VW_ADMIN_TOKEN"], "$$", "$")
			if token == "" || itest.ReadFile(repo.Path("vaultwarden/.env")) == before || strings.Contains(itest.ReadFile(repo.Path("vaultwarden/.env")), token) {
				t.Fatalf("token %q; the file must change and never hold the plaintext", token)
			}
			if !verify(stored, token) {
				t.Errorf("the printed token doesn't verify against the stored hash %s", stored)
			}
			if !docker.Wait(30*time.Second, func() bool { return docker.New("").ContainerState("vaultwarden") == "running" }) {
				t.Fatal("Vaultwarden isn't running after the rotation")
			}
			env := itest.Docker(t, "inspect", "-f", "{{range .Config.Env}}{{println .}}{{end}}", "vaultwarden")
			if !strings.Contains(env, "ADMIN_TOKEN="+stored+"\n") {
				t.Errorf("the container doesn't carry the new hash:\n%s", env)
			}
			if !strings.Contains(out.String(), "log in at https://10.0.0.5:8443/admin") {
				t.Errorf("output:\n%s", out.String())
			}
		})
	}
}

func verify(phc, token string) bool {
	f := strings.Split(phc, "$")
	if len(f) != 6 {
		return false
	}
	var m, it uint32
	var p uint8
	if _, err := fmt.Sscanf(f[3], "m=%d,t=%d,p=%d", &m, &it, &p); err != nil {
		return false
	}
	salt, err1 := base64.RawStdEncoding.DecodeString(f[4])
	want, err2 := base64.RawStdEncoding.DecodeString(f[5])
	if err1 != nil || err2 != nil {
		return false
	}
	return subtle.ConstantTimeCompare(argon2.IDKey([]byte(token), salt, it, m, p, uint32(len(want))), want) == 1
}
