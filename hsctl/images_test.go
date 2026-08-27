package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestParsePlatforms(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		want    []string
		wantErr bool
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
		{
			// A 200 that isn't JSON (captive portal, broken proxy) means we never
			// talked to a registry — it must error, not read as "publishes nothing"
			// (which would misreport network trouble as a bad pin).
			name:    "non-JSON body is an error, not a single-arch image",
			body:    `<html>pay for wifi</html>`,
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parsePlatforms([]byte(tc.body))
			if (err != nil) != tc.wantErr {
				t.Fatalf("parsePlatforms() err = %v, wantErr %v", err, tc.wantErr)
			}
			if !reflect.DeepEqual(got, tc.want) {
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

func TestParseComposeImage(t *testing.T) {
	cases := []struct {
		line, varName, def string
		ok                 bool
	}{
		// The repo's pin form: interpolation variable + frozen default.
		{"    image: ${CADDY_IMAGE:-caddy:2.11-alpine}", "CADDY_IMAGE", "caddy:2.11-alpine", true},
		{"    image: \"${NEXTCLOUD_IMAGE:-nextcloud:34-apache}\"", "NEXTCLOUD_IMAGE", "nextcloud:34-apache", true},
		// Bare pins still parse (varName "") so pinVarFor can refuse them explicitly.
		{"    image: postgres:18-alpine", "", "postgres:18-alpine", true},
		{"    image: 'redis:8-alpine'", "", "redis:8-alpine", true},
		{"#    image: nextcloud:33-apache", "", "", false}, // commented out
		{"    restart: unless-stopped", "", "", false},
	}
	for _, tc := range cases {
		varName, def, ok := parseComposeImage(tc.line)
		if varName != tc.varName || def != tc.def || ok != tc.ok {
			t.Errorf("parseComposeImage(%q) = (%q, %q, %v), want (%q, %q, %v)",
				tc.line, varName, def, ok, tc.varName, tc.def, tc.ok)
		}
	}
}

func TestComposePins(t *testing.T) {
	repo := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		path := filepath.Join(repo, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write("caddy/docker-compose.yml", "services:\n  caddy:\n    image: ${CADDY_IMAGE:-caddy:2.11-alpine}\n")
	// Two services in one file, plus a commented-out pin that must not be reported.
	write("nextcloud/docker-compose.yml", "services:\n  db:\n    image: ${NEXTCLOUD_DB_IMAGE:-postgres:18-alpine}\n  app:\n    image: \"${NEXTCLOUD_IMAGE:-nextcloud:34-apache}\"\n#    image: nextcloud:33-apache\n")
	// A locally applied update: the service's .env override wins over the default.
	write("nextcloud/.env", "POSTGRES_PASSWORD=x\nNEXTCLOUD_IMAGE=nextcloud:35-apache\n")

	pins, err := composePins(repo)
	if err != nil {
		t.Fatal(err)
	}
	want := []composePin{
		{File: filepath.Join("caddy", "docker-compose.yml"), Var: "CADDY_IMAGE",
			Default: "caddy:2.11-alpine", Image: "caddy:2.11-alpine"},
		{File: filepath.Join("nextcloud", "docker-compose.yml"), Var: "NEXTCLOUD_DB_IMAGE",
			Default: "postgres:18-alpine", Image: "postgres:18-alpine"},
		{File: filepath.Join("nextcloud", "docker-compose.yml"), Var: "NEXTCLOUD_IMAGE",
			Default: "nextcloud:34-apache", Image: "nextcloud:35-apache"},
	}
	if !reflect.DeepEqual(pins, want) {
		t.Errorf("composePins() = %v, want %v", pins, want)
	}
}
