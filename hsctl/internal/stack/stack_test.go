//go:build integration

package stack_test

import (
	"bytes"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Rishikesh01/homeserver/hsctl/internal/config"
	"github.com/Rishikesh01/homeserver/hsctl/internal/docker"
	"github.com/Rishikesh01/homeserver/hsctl/internal/envfile"
	"github.com/Rishikesh01/homeserver/hsctl/internal/itest"
	"github.com/Rishikesh01/homeserver/hsctl/internal/layout"
	"github.com/Rishikesh01/homeserver/hsctl/internal/paths"
	"github.com/Rishikesh01/homeserver/hsctl/internal/provision"
	"github.com/Rishikesh01/homeserver/hsctl/internal/stack"
)

// TestUpEach: one app failing to start must not stop the ones after it — above all Caddy, the only
// way to reach the dashboard.
func TestUpEach(t *testing.T) {
	cases := []struct {
		name       string
		failing    []string
		wantFailed []string
	}{
		{"all start", nil, nil},
		{"pihole can't bind :53", []string{"pihole"}, []string{"pihole"}},
		{"two apps fail", []string{"vaultwarden", "stirling"}, []string{"vaultwarden", "stirling"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var started []string
			failed := stack.UpEach(layout.Services, func(svc string) error {
				started = append(started, svc)
				if slices.Contains(tc.failing, svc) {
					return errors.New("boom")
				}
				return nil
			})
			if !slices.Equal(started, layout.Services) || !slices.Equal(failed, tc.wantFailed) {
				t.Errorf("started %v failed %v", started, failed)
			}
		})
	}
}

// TestStopOrder stops Caddy first — or not at all when the dashboard must stay reachable — and
// every app, switched off or not.
func TestStopOrder(t *testing.T) {
	cases := []struct {
		keepProxy bool
		want      string
	}{
		{false, "caddy,imagetools,it-tools,stirling,pihole,nextcloud,vaultwarden"},
		{true, "imagetools,it-tools,stirling,pihole,nextcloud,vaultwarden"},
	}
	for _, tc := range cases {
		if got := strings.Join(stack.StopOrder(tc.keepProxy), ","); got != tc.want {
			t.Errorf("StopOrder(%v) = %s, want %s", tc.keepProxy, got, tc.want)
		}
	}
}

// setUp is a configured checkout of the real stack with the heavy apps (Nextcloud, Pi-hole,
// Stirling-PDF) switched off, and a stack service on it.
func setUp(t *testing.T) (*stack.Service, *bytes.Buffer, paths.Repo) {
	t.Helper()
	itest.NeedRoot(t)
	itest.NeedDocker(t)
	repo := itest.Checkout(t)
	var out bytes.Buffer
	dk := docker.New(repo.Root)
	dk.Stdout, dk.Stderr = &out, &out
	store := config.NewStore(repo, config.LocalHost{})
	c := store.Loaded()
	c.DisabledApps = []string{"nextcloud", "pihole", "stirling"}
	if err := store.Save(c); err != nil {
		t.Fatal(err)
	}
	prov := provision.New(repo, dk)
	prov.Out, prov.Err = &out, &out
	if _, err := prov.Generate(c, false); err != nil {
		t.Fatal(err)
	}
	s := stack.New(repo, dk, store, prov)
	s.Out, s.Err = &out, &out
	return s, &out, repo
}

// running lists the stack containers that are up.
func running(t *testing.T, s *stack.Service) []string {
	t.Helper()
	cs, err := s.Containers()
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, c := range cs {
		if c.Running {
			names = append(names, c.Name)
		}
	}
	slices.Sort(names)
	return names
}

func startedAt(t *testing.T, name string) string {
	return itest.Docker(t, "inspect", "-f", "{{.State.StartedAt}}", name)
}

// TestLifecycle drives the real stack — its own compose files and images — through up, the app
// switches, the CA download, restart and down. The steps run in order on one stack.
func TestLifecycle(t *testing.T) {
	s, out, repo := setUp(t)
	all := []string{"caddy", "imagetools", "it-tools", "vaultwarden"}
	var caddyStart string
	steps := []struct {
		name  string
		run   func() error
		check func() error
	}{
		{"up starts the enabled apps, then Caddy", s.Up, func() error {
			if got := running(t, s); !slices.Equal(got, all) {
				return fmt.Errorf("running %v", got)
			}
			if !strings.Contains(out.String(), "skipping disabled apps: nextcloud, pihole, stirling") {
				return fmt.Errorf("output:\n%s", out.String())
			}
			caddyStart = startedAt(t, "caddy")
			return nil
		}},
		{"status shows them", s.Status, func() error {
			for _, n := range all {
				if !strings.Contains(out.String(), n) {
					return fmt.Errorf("status lacks %s:\n%s", n, out.String())
				}
			}
			return nil
		}},
		{"apps report what runs", nil, func() error {
			for _, a := range s.Apps(true) {
				want := slices.Contains(all, a.Dir)
				if a.Running != want || a.Enabled != want {
					return fmt.Errorf("%+v", a)
				}
				if a.Dir == "vaultwarden" && a.Name != "Passwords" {
					return fmt.Errorf("the tile's name should be used: %+v", a)
				}
			}
			return nil
		}},
		{"Caddy's CA is downloadable", nil, func() error {
			var ca []byte
			var err error
			docker.Wait(30*time.Second, func() bool { ca, err = s.CA(); return err == nil })
			if err != nil {
				return err
			}
			block, _ := pem.Decode(ca)
			if block == nil {
				return fmt.Errorf("not PEM: %q", ca)
			}
			cert, err := x509.ParseCertificate(block.Bytes)
			if err != nil || !cert.IsCA {
				return fmt.Errorf("not a CA certificate: %v", err)
			}
			if err := s.GetCA(); err != nil {
				return err
			}
			if itest.ReadFile(repo.Path(paths.CACert)) != string(ca) || itest.Owner(t, repo.Path(paths.CACert)) != "1000:1000" {
				return fmt.Errorf("caddy-root-ca.crt not written for the admin")
			}
			return nil
		}},
		{"switching an app off stops it and remembers", func() error { return s.SetEnabled("it-tools", false, out) }, func() error {
			if slices.Contains(running(t, s), "it-tools") {
				return fmt.Errorf("it-tools still runs")
			}
			if c := config.NewStore(repo, config.LocalHost{}).Load(); !c.IsDisabled("it-tools") {
				return fmt.Errorf("not saved: %v", c.DisabledApps)
			}
			return nil
		}},
		{"switching it back on starts it", func() error { return s.SetEnabled("it-tools", true, out) }, func() error {
			if !slices.Contains(running(t, s), "it-tools") {
				return fmt.Errorf("it-tools isn't running")
			}
			return nil
		}},
		{"the proxy isn't an app", nil, func() error {
			if err := s.SetEnabled("caddy", false, out); err == nil || !strings.Contains(err.Error(), "unknown app") {
				return fmt.Errorf("caddy: %v", err)
			}
			if !slices.Contains(running(t, s), "caddy") {
				return fmt.Errorf("Caddy was stopped")
			}
			return nil
		}},
		{"restart keeps Caddy running", s.Restart, func() error {
			if got := running(t, s); !slices.Equal(got, all) {
				return fmt.Errorf("running %v", got)
			}
			if startedAt(t, "caddy") != caddyStart {
				return fmt.Errorf("Caddy was restarted")
			}
			return nil
		}},
		{"down keeping the proxy", func() error { return s.Down(false, true) }, func() error {
			if got := running(t, s); !slices.Equal(got, []string{"caddy"}) {
				return fmt.Errorf("running %v", got)
			}
			return nil
		}},
		{"down", func() error { return s.Down(false, false) }, func() error {
			if got := running(t, s); len(got) != 0 {
				return fmt.Errorf("running %v", got)
			}
			if !itest.DockerOK("volume", "inspect", layout.VaultwardenVolume) {
				return fmt.Errorf("a plain down deleted data")
			}
			return nil
		}},
		{"proxy alone", s.StartProxy, func() error {
			if got := running(t, s); !slices.Equal(got, []string{"caddy"}) {
				return fmt.Errorf("running %v", got)
			}
			return nil
		}},
		{"a broken app doesn't keep the rest down", func() error {
			if err := envfile.Upsert(repo.Path("imagetools/.env"), "IMAGETOOLS_IMAGE", "nginx:0.0.0-does-not-exist"); err != nil {
				return err
			}
			err := s.Up()
			if err == nil || !strings.Contains(err.Error(), "imagetools didn't start") {
				return fmt.Errorf("Up = %v", err)
			}
			return nil
		}, func() error {
			if got := running(t, s); !slices.Equal(got, []string{"caddy", "it-tools", "vaultwarden"}) {
				return fmt.Errorf("running %v", got)
			}
			return nil
		}},
		{"down with volumes keeps Caddy's CA", func() error { return s.Down(true, true) }, func() error {
			if itest.DockerOK("volume", "inspect", layout.VaultwardenVolume) {
				return fmt.Errorf("the app data wasn't deleted")
			}
			if !itest.DockerOK("volume", "inspect", "caddy_caddy-data") {
				return fmt.Errorf("Caddy's data was deleted")
			}
			return nil
		}},
	}
	for _, st := range steps {
		out.Reset()
		if st.run != nil {
			if err := st.run(); err != nil {
				t.Fatalf("%s: %v\n%s", st.name, err, out.String())
			}
		}
		if err := st.check(); err != nil {
			t.Fatalf("%s: %v", st.name, err)
		}
	}
}

// TestRefusals: up and down refuse — before touching anything — when setup hasn't run, Docker
// can't be used, or a config file can't be read.
func TestRefusals(t *testing.T) {
	cases := []struct {
		name    string
		prep    func(t *testing.T, repo paths.Repo)
		run     func(s *stack.Service) error
		wantErr string
	}{
		{"up before setup", func(t *testing.T, repo paths.Repo) {}, (*stack.Service).Up, "missing .env for vaultwarden, nextcloud, pihole, caddy — run: hsctl setup"},
		{"up without Docker", func(t *testing.T, repo paths.Repo) {
			t.Setenv("DOCKER_HOST", "unix:///nonexistent/docker.sock")
		}, (*stack.Service).Up, "Docker isn't running"},
		{"down without Docker", func(t *testing.T, repo paths.Repo) {
			t.Setenv("DOCKER_HOST", "unix:///nonexistent/docker.sock")
		}, func(s *stack.Service) error { return s.Down(false, false) }, "Docker isn't running"},
		{"switching on an unknown app", func(t *testing.T, repo paths.Repo) {}, func(s *stack.Service) error {
			return s.SetEnabled("../etc", true, &bytes.Buffer{})
		}, `unknown app "../etc"`},
		{"switching on an app setup never configured", func(t *testing.T, repo paths.Repo) {}, func(s *stack.Service) error {
			return s.SetEnabled("vaultwarden", true, &bytes.Buffer{})
		}, "vaultwarden has no .env yet"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			itest.NeedDocker(t)
			repo := itest.Checkout(t)
			tc.prep(t, repo)
			var out bytes.Buffer
			dk := docker.New(repo.Root)
			dk.Stdout, dk.Stderr = &out, &out
			s := stack.New(repo, dk, config.NewStore(repo, config.LocalHost{}), provision.New(repo, dk))
			s.Out, s.Err = &out, &out
			if err := tc.run(s); err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("err = %v, want %q", err, tc.wantErr)
			}
		})
	}
}

// TestUnreadableConfig runs as a normal user: a .env left root-owned stops up and down with how to
// fix it, instead of compose failing obscurely halfway.
func TestUnreadableConfig(t *testing.T) {
	if !itest.Unprivileged(t) {
		return
	}
	repo := itest.Repo(t)
	itest.WriteFile(t, repo.Root, "vaultwarden/.env", "X=1\n")
	if err := os.Chmod(repo.Path("vaultwarden/.env"), 0); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		run  func(s *stack.Service) error
	}{
		{"up", (*stack.Service).Up},
		{"down", func(s *stack.Service) error { return s.Down(false, false) }},
	}
	for _, tc := range cases {
		var out bytes.Buffer
		s := stack.New(repo, docker.New(repo.Root), config.NewStore(repo, config.LocalHost{}), nil)
		s.Out, s.Err = &out, &out
		if err := tc.run(s); err == nil || !strings.Contains(err.Error(), "can't read vaultwarden/.env") {
			t.Errorf("%s: %v", tc.name, err)
		}
	}
}
