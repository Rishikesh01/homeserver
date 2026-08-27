package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestParsePlatforms(t *testing.T) {
	cases := []struct {
		name string
		body string
		want []string
	}{
		{
			// The shape Docker Hub returns for a multi-arch tag, including the
			// attestation entries buildx attaches (os/architecture "unknown").
			name: "index skips attestations and dedups the arm64 variant",
			body: `{"manifests":[
				{"platform":{"os":"linux","architecture":"amd64"}},
				{"platform":{"os":"linux","architecture":"arm64","variant":"v8"}},
				{"platform":{"os":"linux","architecture":"arm64"}},
				{"platform":{"os":"unknown","architecture":"unknown"}}]}`,
			want: []string{"linux/amd64", "linux/arm64"},
		},
		{
			// A single-arch image answers with a bare manifest, no platform list —
			// which is exactly the case `hsctl images` has to catch.
			name: "bare manifest publishes nothing",
			body: `{"schemaVersion":2,"config":{"mediaType":"application/vnd.oci.image.config.v1+json"}}`,
			want: nil,
		},
		{name: "garbage is not a platform list", body: `not json`, want: nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parsePlatforms([]byte(tc.body)); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("parsePlatforms() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestMissingPlatforms(t *testing.T) {
	want := []string{"linux/amd64", "linux/arm64"}
	cases := []struct {
		got     []string
		missing []string
	}{
		{[]string{"linux/amd64", "linux/arm64", "linux/s390x"}, nil},
		{[]string{"linux/amd64"}, []string{"linux/arm64"}}, // the regression we care about
		{[]string{"linux/arm/v7"}, want},                   // 32-bit only helps nobody
		{nil, want},                                        // single-arch image
	}
	for _, tc := range cases {
		if got := missingPlatforms(want, tc.got); !reflect.DeepEqual(got, tc.missing) {
			t.Errorf("missingPlatforms(%v) = %v, want %v", tc.got, got, tc.missing)
		}
	}
}

func TestComposePins(t *testing.T) {
	repo := t.TempDir()
	write := func(dir, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(repo, dir), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repo, dir, "docker-compose.yml"), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write("caddy", "services:\n  caddy:\n    image: caddy:2.11-alpine\n")
	// Two services in one file, plus a commented-out pin that must not be reported.
	write("nextcloud", "services:\n  db:\n    image: postgres:18-alpine\n  app:\n    image: \"nextcloud:34-apache\"\n#    image: nextcloud:33-apache\n")

	pins, err := composePins(repo)
	if err != nil {
		t.Fatal(err)
	}
	want := []composePin{
		{File: filepath.Join("caddy", "docker-compose.yml"), Image: "caddy:2.11-alpine"},
		{File: filepath.Join("nextcloud", "docker-compose.yml"), Image: "postgres:18-alpine"},
		{File: filepath.Join("nextcloud", "docker-compose.yml"), Image: "nextcloud:34-apache"},
	}
	if !reflect.DeepEqual(pins, want) {
		t.Errorf("composePins() = %v, want %v", pins, want)
	}
}
