//go:build integration

package updates_test

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/Rishikesh01/homeserver/hsctl/internal/config"
	"github.com/Rishikesh01/homeserver/hsctl/internal/docker"
	"github.com/Rishikesh01/homeserver/hsctl/internal/envfile"
	"github.com/Rishikesh01/homeserver/hsctl/internal/itest"
	"github.com/Rishikesh01/homeserver/hsctl/internal/prompt"
	"github.com/Rishikesh01/homeserver/hsctl/internal/provision"
	"github.com/Rishikesh01/homeserver/hsctl/internal/registry"
	"github.com/Rishikesh01/homeserver/hsctl/internal/stack"
	"github.com/Rishikesh01/homeserver/hsctl/internal/updates"
)

const buildxOut = "Name:      docker.io/library/caddy:2\nMediaType: application/vnd.oci.image.index.v1+json\nDigest:    sha256:aaa111\n"

// TestCompare decides one image's status from its digests and the release probe: a newer release
// outranks a clean digest verdict, and a missing digest on either side never claims a verdict.
func TestCompare(t *testing.T) {
	cases := []struct {
		name               string
		image, local, remo string
		newer              string
		wantState          string
		wantStale          bool
	}{
		{"same digest", "caddy:2", "caddy@sha256:aaa111", buildxOut, "", "up to date", false},
		{"one of several local digests matches", "caddy:2", "caddy@sha256:old,docker.io/library/caddy@sha256:aaa111", buildxOut, "", "up to date", false},
		{"rebuilt tag", "caddy:2", "caddy@sha256:old999", buildxOut, "", "UPDATE available", true},
		{"no local digest", "x:1", "", buildxOut, "", "no local digest (built locally or never pulled) — can't compare", false},
		{"no remote digest", "x:1", "x@sha256:aaa", "ERROR: not found", "", "couldn't read the registry digest — offline, rate-limited, or a private image", false},
		{"newer release outranks", "vaultwarden/server:1.36.0", "v@sha256:aaa111", buildxOut, "1.37.0", "NEWER RELEASE 1.37.0 (pinned to 1.36.0)", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := updates.Compare("c", tc.image, tc.local, tc.remo, tc.newer)
			if st.State != tc.wantState || st.Stale != tc.wantStale || st.Newer != tc.newer {
				t.Errorf("Compare = %+v", st)
			}
		})
	}
	digests := []struct {
		in   string
		want []string
	}{
		{"caddy@sha256:aaa111,docker.io/library/caddy@sha256:bbb222", []string{"sha256:aaa111", "sha256:bbb222"}},
		{"", nil},
		{"caddy:2", nil},
	}
	for _, d := range digests {
		if got := updates.ParseLocalDigests(d.in); !reflect.DeepEqual(got, d.want) {
			t.Errorf("ParseLocalDigests(%q) = %v", d.in, got)
		}
	}
	if updates.ParseRemoteDigest(buildxOut) != "sha256:aaa111" || updates.ParseRemoteDigest("ERROR: image not found") != "" {
		t.Error("ParseRemoteDigest")
	}
}

// TestVersions: what counts as a newer release — same shape (number count and variant), a higher
// version — and which jumps are majors that `--apply all` leaves alone.
func TestVersions(t *testing.T) {
	parse := []struct {
		tag    string
		nums   []int
		suffix string
		ok     bool
	}{
		{"1.37.0", []int{1, 37, 0}, "", true},
		{"1.31.8-alpine", []int{1, 31, 8}, "-alpine", true},
		{"35.0.2-apache", []int{35, 0, 2}, "-apache", true},
		{"2024.10.22-7ca5933", []int{2024, 10, 22}, "", true}, // build hash stripped
		{"2026.10.0", []int{2026, 10, 0}, "", true},
		{"1.2-decade", []int{1, 2}, "-decade", true}, // a word made of hex letters isn't a hash
		{"latest", nil, "", false},
		{"apache", nil, "", false},
	}
	for _, tc := range parse {
		nums, suffix, ok := updates.ParseTagVersion(tc.tag)
		if ok != tc.ok || suffix != tc.suffix || !reflect.DeepEqual(nums, tc.nums) {
			t.Errorf("ParseTagVersion(%s) = (%v, %q, %v)", tc.tag, nums, suffix, ok)
		}
	}
	vw := []string{"latest", "1.36.0", "1.36.1", "1.37.0", "1.34.3-alpine", "testing"}
	newer := []struct {
		cur  string
		tags []string
		want string
	}{
		{"1.36.0", vw, "1.37.0"},
		{"1.37.0", vw, ""},
		{"latest", vw, ""},
		{"1.27-alpine", []string{"1.29-alpine", "1.29.1-alpine", "1.29", "mainline-alpine"}, "1.29-alpine"},
		{"2024.10.22-7ca5933", []string{"2024.10.22-7ca5933", "2025.5.1-abc1234"}, "2025.5.1-abc1234"},
	}
	for _, tc := range newer {
		if got := updates.NewerVersionTag(tc.cur, tc.tags); got != tc.want {
			t.Errorf("NewerVersionTag(%s) = %q, want %q", tc.cur, got, tc.want)
		}
	}
	majors := []struct {
		cur, next string
		want      bool
	}{
		{"16-alpine", "18-alpine", true},
		{"34.0.1-apache", "35.0.2-apache", true},
		{"2.8-alpine", "2.11-alpine", false},
		{"1.37.0", "1.37.1", false},
		{"2025.04.0", "2026.07.2", false}, // calendar versions roll routinely
		{"latest", "1.0", false},
	}
	for _, tc := range majors {
		if got := updates.IsMajorJump(tc.cur, tc.next); got != tc.want {
			t.Errorf("IsMajorJump(%s, %s) = %v", tc.cur, tc.next, got)
		}
		st := updates.Status{Image: "x:" + tc.cur, Newer: tc.next}
		if st.Major() != tc.want {
			t.Errorf("Status{%s -> %s}.Major() = %v", tc.cur, tc.next, st.Major())
		}
	}
}

// TestPinVarFor finds the variable an update moves — through an applied override, only in the
// matching service — and refuses a bare pin or a compose file that drifted from the container.
func TestPinVarFor(t *testing.T) {
	vw := "services:\n  vaultwarden:\n    image: ${VAULTWARDEN_IMAGE:-vaultwarden/server:1.36.0}\n    restart: unless-stopped\n"
	multi := "  db:\n    image: ${DB_IMAGE:-postgres:16-alpine}\n  app:\n    image: ${APP_IMAGE:-nextcloud:30-apache}\n"
	override := map[string]string{"VAULTWARDEN_IMAGE": "vaultwarden/server:1.36.5"}
	cases := []struct {
		name, compose string
		env           map[string]string
		ref, want     string
		wantErr       string
	}{
		{"default", vw, nil, "vaultwarden/server:1.36.0", "VAULTWARDEN_IMAGE", ""},
		{"through an override", vw, override, "vaultwarden/server:1.36.5", "VAULTWARDEN_IMAGE", ""},
		{"shadowed default", vw, override, "vaultwarden/server:1.36.0", "", "drifted"},
		{"multi-service file", multi, nil, "postgres:16-alpine", "DB_IMAGE", ""},
		{"bare pin", "  image: postgres:16-alpine\n", nil, "postgres:16-alpine", "", "bare pin"},
		{"drifted", vw, nil, "vaultwarden/server:1.35.0", "", "drifted"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := updates.PinVarFor(tc.compose, tc.env, tc.ref)
			if got != tc.want || (err == nil) != (tc.wantErr == "") || (err != nil && !strings.Contains(err.Error(), tc.wantErr)) {
				t.Errorf("PinVarFor = (%q, %v), want (%q, %q)", got, err, tc.want, tc.wantErr)
			}
		})
	}
}

// TestCheckAndApply runs the whole update flow against a real registry and real containers: a
// newer release (a major one, and a routine one), and a tag rebuilt upstream. `--apply all` takes
// the routine ones and leaves the major; naming it applies it once confirmed; afterwards every
// image is up to date.
func TestCheckAndApply(t *testing.T) {
	reg := itest.StartRegistry(t)
	itest.Pull(t, itest.Busybox)
	itest.Pull(t, "alpine:3.22")
	vw := reg.Push(t, itest.Busybox, "hs/vaultwarden:1.0.0")
	reg.Push(t, itest.Busybox, "hs/vaultwarden:2.0.0")
	tools := reg.Push(t, itest.Busybox, "hs/it-tools:2024.10.22-7ca5933")
	reg.Push(t, itest.Busybox, "hs/it-tools:2024.11.05-abc1234")
	img := reg.Push(t, itest.Busybox, "hs/imagetools:1.0-alpine")

	repo := itest.Repo(t)
	for folder, pin := range map[string][2]string{
		"vaultwarden": {"VAULTWARDEN_IMAGE", vw}, "it-tools": {"ITTOOLS_IMAGE", tools}, "imagetools": {"IMAGETOOLS_IMAGE", img},
	} {
		itest.WriteFile(t, repo.Root, folder+"/docker-compose.yml", fmt.Sprintf(
			"services:\n  app:\n    image: ${%s:-%s}\n    container_name: %s\n    command: [\"sleep\", \"3600\"]\n", pin[0], pin[1], folder))
	}
	var out bytes.Buffer
	dk := docker.New(repo.Root)
	dk.Stdout, dk.Stderr = &out, &out
	for _, folder := range []string{"vaultwarden", "it-tools", "imagetools"} {
		folder := folder
		if err := dk.Run(repo.Path(folder), dk.ComposeUpArgs()...); err != nil {
			t.Fatalf("up %s: %v\n%s", folder, err, out.String())
		}
		t.Cleanup(func() { _ = dk.Quiet(repo.Path(folder), "compose", "down") })
	}
	// Rebuilt upstream under the same tag. Pushing retags locally, so point the local tag back at
	// what the container runs: this box hasn't pulled the rebuild.
	reg.Push(t, "alpine:3.22", "hs/imagetools:1.0-alpine")
	itest.Docker(t, "tag", itest.Busybox, img)

	store := config.NewStore(repo, config.LocalHost{})
	st := stack.New(repo, dk, store, provision.New(repo, dk))
	c := updates.New(repo, dk, &registry.Client{HTTP: reg.HTTP}, st.ContainerNames, &prompt.Terminal{Interactive: func() bool { return false }})
	c.Out = &out
	imageID := func(name string) string { return itest.Docker(t, "inspect", "-f", "{{.Image}}", name) }
	oldImagetools := imageID("imagetools")

	var statuses []updates.Status
	steps := []struct {
		name  string
		check func() error
	}{
		{"check finds all three", func() error {
			var err error
			statuses, err = c.Check(nil)
			if err != nil {
				return err
			}
			got := map[string]string{}
			for _, s := range statuses {
				got[s.Container] = s.State
			}
			want := map[string]string{
				"vaultwarden": "NEWER RELEASE 2.0.0 (pinned to 1.0.0)",
				"it-tools":    "NEWER RELEASE 2024.11.05-abc1234 (pinned to 2024.10.22-7ca5933)",
				"imagetools":  "UPDATE available",
			}
			if !reflect.DeepEqual(got, want) {
				return fmt.Errorf("states = %v", got)
			}
			return nil
		}},
		{"the report says so", func() error {
			out.Reset()
			if err := c.Run(nil, false); err != nil || !strings.Contains(out.String(), "3 image(s) have an update") {
				return fmt.Errorf("Run = %v\n%s", err, out.String())
			}
			return nil
		}},
		{"an unknown name is refused", func() error {
			if err := c.Apply(statuses, []string{"caddy"}, true); err == nil || !strings.Contains(err.Error(), `no stack container named "caddy"`) {
				return fmt.Errorf("Apply(caddy) = %v", err)
			}
			return nil
		}},
		{"all applies the routine ones and skips the major", func() error {
			out.Reset()
			if err := c.Apply(statuses, []string{"all"}, true); err != nil {
				return fmt.Errorf("%v\n%s", err, out.String())
			}
			kv, _ := envfile.Read(repo.Path("it-tools/.env"))
			switch {
			case !strings.Contains(out.String(), "skipping vaultwarden: 1.0.0 -> 2.0.0 is a major upgrade"):
				return fmt.Errorf("major not skipped:\n%s", out.String())
			case kv["ITTOOLS_IMAGE"] != reg.Host+"/hs/it-tools:2024.11.05-abc1234":
				return fmt.Errorf("it-tools/.env = %v", kv)
			case dk.ContainerImage("it-tools") != kv["ITTOOLS_IMAGE"]:
				return fmt.Errorf("it-tools runs %s", dk.ContainerImage("it-tools"))
			case imageID("imagetools") == oldImagetools:
				return fmt.Errorf("imagetools wasn't repulled")
			case dk.ContainerImage("vaultwarden") != vw:
				return fmt.Errorf("vaultwarden was touched: %s", dk.ContainerImage("vaultwarden"))
			case strings.Contains(itest.ReadFile(repo.Path("it-tools/docker-compose.yml")), "2024.11.05"):
				return fmt.Errorf("the tracked compose file was edited")
			}
			return nil
		}},
		{"a major needs a confirmation", func() error {
			if err := c.Apply(statuses, []string{"vaultwarden"}, false); err == nil || !strings.Contains(err.Error(), "no terminal to confirm on") {
				return fmt.Errorf("without a terminal: %v", err)
			}
			c.Ask = &prompt.Terminal{In: bufio.NewReader(strings.NewReader("n\n")), Out: &out}
			if err := c.Apply(statuses, []string{"vaultwarden"}, false); err == nil || !strings.Contains(err.Error(), "aborted") {
				return fmt.Errorf("answered no: %v", err)
			}
			if dk.ContainerImage("vaultwarden") != vw {
				return errors.New("changed although refused")
			}
			c.Ask = &prompt.Terminal{In: bufio.NewReader(strings.NewReader("y\n")), Out: &out}
			if err := c.Apply(statuses, []string{"vaultwarden"}, false); err != nil {
				return fmt.Errorf("answered yes: %v\n%s", err, out.String())
			}
			if got := dk.ContainerImage("vaultwarden"); got != reg.Host+"/hs/vaultwarden:2.0.0" {
				return fmt.Errorf("vaultwarden runs %s", got)
			}
			return nil
		}},
		{"everything is up to date afterwards", func() error {
			out.Reset()
			if err := c.Run(nil, false); err != nil || !strings.Contains(out.String(), "everything is up to date") {
				return fmt.Errorf("Run = %v\n%s", err, out.String())
			}
			return nil
		}},
	}
	for _, s := range steps {
		if err := s.check(); err != nil {
			t.Fatalf("%s: %v", s.name, err)
		}
	}
}

// TestNoContainers: with no stack containers there's nothing to compare — which must not read as
// "up to date".
func TestNoContainers(t *testing.T) {
	itest.NeedDocker(t)
	cases := []struct {
		name  string
		apply []string
	}{
		{"check", nil},
		{"apply", []string{"all"}},
	}
	for _, tc := range cases {
		repo := itest.Repo(t)
		var out bytes.Buffer
		dk := docker.New(repo.Root)
		dk.Stdout, dk.Stderr = &out, &out
		st := stack.New(repo, dk, config.NewStore(repo, config.LocalHost{}), provision.New(repo, dk))
		c := updates.New(repo, dk, registry.New(), st.ContainerNames, nil)
		c.Out = &out
		if err := c.Run(tc.apply, true); !errors.Is(err, updates.ErrNoContainers) {
			t.Errorf("%s: Run = %v, want ErrNoContainers", tc.name, err)
		}
	}
}
