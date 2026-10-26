//go:build integration

package images_test

import (
	"bytes"
	"errors"
	"net"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Rishikesh01/homeserver/hsctl/internal/images"
	"github.com/Rishikesh01/homeserver/hsctl/internal/itest"
	"github.com/Rishikesh01/homeserver/hsctl/internal/paths"
	"github.com/Rishikesh01/homeserver/hsctl/internal/registry"
)

// TestParseComposeImage: the one definition of a pin — `image: ${VAR:-default}`, quoted or not —
// that both the platform check and `updates --apply` use.
func TestParseComposeImage(t *testing.T) {
	cases := []struct {
		line, varName, def string
		ok                 bool
	}{
		{"    image: ${CADDY_IMAGE:-caddy:2.11.7-alpine}", "CADDY_IMAGE", "caddy:2.11.7-alpine", true},
		{"    image: \"${NEXTCLOUD_IMAGE:-nextcloud:35.0.2-apache}\"", "NEXTCLOUD_IMAGE", "nextcloud:35.0.2-apache", true},
		{"    image: postgres:18.6-alpine", "", "postgres:18.6-alpine", true}, // bare: no variable to override
		{"    image: 'redis:8.10.2-alpine'", "", "redis:8.10.2-alpine", true},
		{"#    image: nextcloud:33-apache", "", "", false}, // commented out
		{"    restart: unless-stopped", "", "", false},
	}
	for _, tc := range cases {
		varName, def, ok := images.ParseComposeImage(tc.line)
		if varName != tc.varName || def != tc.def || ok != tc.ok {
			t.Errorf("ParseComposeImage(%q) = (%q, %q, %v), want (%q, %q, %v)", tc.line, varName, def, ok, tc.varName, tc.def, tc.ok)
		}
		if ok {
			env := map[string]string{"CADDY_IMAGE": "caddy:9", "UNRELATED": "x"}
			want := tc.def
			if tc.varName == "CADDY_IMAGE" {
				want = "caddy:9"
			}
			if got := images.EffectiveRef(varName, def, env); got != want {
				t.Errorf("EffectiveRef(%q) = %s, want %s", tc.line, got, want)
			}
		}
	}
}

// TestPlatforms reads what an index publishes: attestations skipped, the arm64 variant folded in,
// a bare manifest publishing none — and a body that isn't JSON is an error (a captive portal
// answered), never "publishes nothing".
func TestPlatforms(t *testing.T) {
	want := []string{"linux/amd64", "linux/arm64"}
	cases := []struct {
		name      string
		body      string
		platforms []string
		missing   []string
		wantErr   bool
	}{
		{"index", `{"manifests":[
			{"platform":{"os":"linux","architecture":"amd64"}},
			{"platform":{"os":"linux","architecture":"arm64","variant":"v8"}},
			{"platform":{"os":"linux","architecture":"arm64"}},
			{"platform":{"os":"unknown","architecture":"unknown"}}]}`, want, nil, false},
		{"extra platforms are fine", `{"manifests":[{"platform":{"os":"linux","architecture":"amd64"}},
			{"platform":{"os":"linux","architecture":"arm64"}},{"platform":{"os":"linux","architecture":"s390x"}}]}`,
			[]string{"linux/amd64", "linux/arm64", "linux/s390x"}, nil, false},
		{"amd64 only", `{"manifests":[{"platform":{"os":"linux","architecture":"amd64"}}]}`, []string{"linux/amd64"}, []string{"linux/arm64"}, false},
		{"32-bit only helps nobody", `{"manifests":[{"platform":{"os":"linux","architecture":"arm"}}]}`, []string{"linux/arm"}, want, false},
		{"bare manifest", `{"schemaVersion":2,"config":{"mediaType":"application/vnd.oci.image.config.v1+json"}}`, nil, want, false},
		{"not json", `<html>pay for wifi</html>`, nil, nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := images.ParsePlatforms([]byte(tc.body))
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, want error %v", err, tc.wantErr)
			}
			if !reflect.DeepEqual(got, tc.platforms) {
				t.Errorf("ParsePlatforms = %v, want %v", got, tc.platforms)
			}
			if !tc.wantErr {
				if m := images.MissingPlatforms(want, got); !reflect.DeepEqual(m, tc.missing) {
					t.Errorf("MissingPlatforms = %v, want %v", m, tc.missing)
				}
			}
		})
	}
}

// TestPins reads every pin from the compose files — two in one file, a commented one skipped — and
// resolves each against its folder's .env, where an applied update lives.
func TestPins(t *testing.T) {
	cases := []struct {
		name  string
		files map[string]string
		want  []images.Pin
	}{
		{"overrides and multi-service files", map[string]string{
			"caddy/docker-compose.yml": "services:\n  caddy:\n    image: ${CADDY_IMAGE:-caddy:2.11.7-alpine}\n",
			"nextcloud/docker-compose.yml": "services:\n  db:\n    image: ${NEXTCLOUD_DB_IMAGE:-postgres:18.6-alpine}\n" +
				"  app:\n    image: \"${NEXTCLOUD_IMAGE:-nextcloud:35.0.2-apache}\"\n#    image: nextcloud:33-apache\n",
			"nextcloud/.env": "POSTGRES_PASSWORD=x\nNEXTCLOUD_IMAGE=nextcloud:35.0.3-apache\n",
		}, []images.Pin{
			{File: "caddy/docker-compose.yml", Var: "CADDY_IMAGE", Default: "caddy:2.11.7-alpine", Image: "caddy:2.11.7-alpine"},
			{File: "nextcloud/docker-compose.yml", Var: "NEXTCLOUD_DB_IMAGE", Default: "postgres:18.6-alpine", Image: "postgres:18.6-alpine"},
			{File: "nextcloud/docker-compose.yml", Var: "NEXTCLOUD_IMAGE", Default: "nextcloud:35.0.2-apache", Image: "nextcloud:35.0.3-apache"},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			for f, c := range tc.files {
				itest.WriteFile(t, root, f, c)
			}
			pins, err := images.New(paths.Repo{Root: root}, nil).Pins()
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(pins, tc.want) {
				t.Errorf("Pins =\n %v\nwant\n %v", pins, tc.want)
			}
		})
	}
	// The real checkout: every service's image is a ${VAR:-default} pin with an exact version.
	real, err := images.New(paths.Repo{Root: itest.SourceRoot(t)}, nil).Pins()
	if err != nil || len(real) < 8 {
		t.Fatalf("the repo's pins: %v %v", real, err)
	}
	for _, p := range real {
		if p.Var == "" || strings.HasSuffix(p.Default, ":latest") || !strings.Contains(p.Default, ":") {
			t.Errorf("%s: %q isn't an exact ${VAR:-default} pin", p.File, p.Default)
		}
	}
}

// TestCheck asks a real registry what each pin publishes: an index covering both platforms passes,
// a single-arch tag or a vanished one fails the check, and a registry that doesn't answer is only
// tolerated while some pin could be checked.
func TestCheck(t *testing.T) {
	reg := itest.StartRegistry(t)
	itest.Pull(t, itest.Busybox)
	reg.Push(t, itest.Busybox, "hs/multi:base")
	reg.Index(t, "hs/multi", "1.0.0", "base", "linux/amd64", "linux/arm64", "unknown/unknown")
	single := reg.Push(t, itest.Busybox, "hs/single:1.0.0")
	ln, err := net.Listen("tcp", "127.0.0.1:0") // a port where nothing answers TLS
	if err != nil {
		t.Fatal(err)
	}
	dead := ln.Addr().String()
	ln.Close()
	multi := reg.Host + "/hs/multi:1.0.0"
	cases := []struct {
		name    string
		pins    []string
		wantErr string // "" = pass
		wantOut []string
	}{
		{"every pin covered", []string{multi}, "", []string{"ok", "every image publishes linux/amd64 and linux/arm64"}},
		{"single-arch tag", []string{multi, single}, "1 image(s) failed", []string{"MISSING", "publishes"}},
		{"vanished tag", []string{multi, reg.Host + "/hs/multi:9.9.9"}, "1 image(s) failed", []string{"BROKEN", "not in the registry any more"}},
		{"one unreachable registry", []string{multi, dead + "/hs/x:1.0"}, "", []string{"UNREACHABLE", "couldn't be checked"}},
		{"nothing reachable", []string{dead + "/hs/x:1.0"}, "none of the 1 pins could be checked", []string{"UNREACHABLE"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := itest.Repo(t)
			for i, p := range tc.pins {
				itest.WriteFile(t, repo.Root, filepath.Join("app"+string(rune('a'+i)), "docker-compose.yml"),
					"services:\n  app:\n    image: ${APP_IMAGE:-"+p+"}\n")
			}
			var out bytes.Buffer
			c := images.New(repo, &registry.Client{HTTP: reg.HTTP})
			c.Out, c.Err = &out, &out
			err := c.Check(images.SupportedPlatforms)
			if (err == nil) != (tc.wantErr == "") || (err != nil && !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("Check = %v, want %q\n%s", err, tc.wantErr, out.String())
			}
			for _, w := range tc.wantOut {
				if !strings.Contains(out.String(), w) {
					t.Errorf("output lacks %q:\n%s", w, out.String())
				}
			}
		})
	}
	if _, err := images.New(itest.Repo(t), &registry.Client{HTTP: reg.HTTP}).Platforms(reg.Host + "/hs/nope:1"); !errors.Is(err, images.ErrBadPin) {
		t.Errorf("a missing repository is the pin's fault: %v", err)
	}
}
