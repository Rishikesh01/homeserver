package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

const buildxOut = `Name:      docker.io/library/caddy:2
MediaType: application/vnd.oci.image.index.v1+json
Digest:    sha256:aaa111
`

func TestParseRemoteDigest(t *testing.T) {
	if got := parseRemoteDigest(buildxOut); got != "sha256:aaa111" {
		t.Errorf("got %q", got)
	}
	if got := parseRemoteDigest("ERROR: image not found"); got != "" {
		t.Errorf("garbage should yield empty, got %q", got)
	}
}

func TestParseLocalDigests(t *testing.T) {
	got := parseLocalDigests("caddy@sha256:aaa111,docker.io/library/caddy@sha256:bbb222")
	if len(got) != 2 || got[0] != "sha256:aaa111" || got[1] != "sha256:bbb222" {
		t.Errorf("got %v", got)
	}
	if got := parseLocalDigests(""); len(got) != 0 {
		t.Errorf("empty input should yield none, got %v", got)
	}
}

func TestCheckImage(t *testing.T) {
	up := checkImage("caddy", "caddy:2", "caddy@sha256:aaa111", buildxOut, "")
	if up.Stale || up.State != "up to date" {
		t.Errorf("matching digests: %+v", up)
	}
	stale := checkImage("caddy", "caddy:2", "caddy@sha256:old999", buildxOut, "")
	if !stale.Stale || stale.State != "UPDATE available" {
		t.Errorf("differing digests: %+v", stale)
	}
	if st := checkImage("x", "x", "", buildxOut, ""); st.Stale || st.State == "up to date" {
		t.Errorf("no local digest must not claim a verdict: %+v", st)
	}
	if st := checkImage("x", "x", "x@sha256:aaa", "ERROR", ""); st.Stale || st.State == "up to date" {
		t.Errorf("no remote digest must not claim a verdict: %+v", st)
	}
	// A newer release outranks a clean digest verdict: the pinned tag matching
	// the registry says nothing about newer pins existing.
	rel := checkImage("vaultwarden", "vaultwarden/server:1.36.0", "v@sha256:aaa111", buildxOut, "1.37.0")
	if !rel.Stale || rel.State != "NEWER RELEASE 1.37.0 (pinned to 1.36.0)" {
		t.Errorf("newer release: %+v", rel)
	}
}

func TestSplitImageRef(t *testing.T) {
	for _, tc := range []struct{ image, host, repo, tag string }{
		{"vaultwarden/server:1.36.0", "docker.io", "vaultwarden/server", "1.36.0"},
		{"nginx:1.27-alpine", "docker.io", "library/nginx", "1.27-alpine"},
		{"nginx", "docker.io", "library/nginx", "latest"},
		{"ghcr.io/corentinth/it-tools:2024.10.22-7ca5933", "ghcr.io", "corentinth/it-tools", "2024.10.22-7ca5933"},
		{"localhost:5000/thing:1.0", "localhost:5000", "thing", "1.0"},
		{"redis:7-alpine@sha256:abc", "docker.io", "library/redis", "7-alpine"},
	} {
		host, repo, tag := splitImageRef(tc.image)
		if host != tc.host || repo != tc.repo || tag != tc.tag {
			t.Errorf("%s: got (%s, %s, %s)", tc.image, host, repo, tag)
		}
	}
}

func TestParseTagVersion(t *testing.T) {
	for _, tc := range []struct {
		tag    string
		nums   []int
		suffix string
		ok     bool
	}{
		{"1.37.0", []int{1, 37, 0}, "", true},
		{"1.27-alpine", []int{1, 27}, "-alpine", true},
		{"30-apache", []int{30}, "-apache", true},
		{"2024.10.22-7ca5933", []int{2024, 10, 22}, "", true}, // build hash stripped
		{"2025.04.0", []int{2025, 4, 0}, "", true},
		{"latest", nil, "", false},
		{"apache", nil, "", false},
	} {
		nums, suffix, ok := parseTagVersion(tc.tag)
		if ok != tc.ok || suffix != tc.suffix || !reflect.DeepEqual(nums, tc.nums) {
			t.Errorf("%s: got (%v, %q, %v)", tc.tag, nums, suffix, ok)
		}
	}
}

func TestNewerVersionTag(t *testing.T) {
	vwTags := []string{"latest", "1.36.0", "1.36.1", "1.37.0", "1.34.3-alpine", "testing"}
	if got := newerVersionTag("1.36.0", vwTags); got != "1.37.0" {
		t.Errorf("pinned 1.36.0: got %q, want 1.37.0", got)
	}
	if got := newerVersionTag("1.37.0", vwTags); got != "" {
		t.Errorf("newest pin must report nothing, got %q", got)
	}
	// Shape matters: same suffix, same number-count only.
	nginxTags := []string{"1.29-alpine", "1.29.1-alpine", "1.29", "mainline-alpine"}
	if got := newerVersionTag("1.27-alpine", nginxTags); got != "1.29-alpine" {
		t.Errorf("suffix/shape filter: got %q, want 1.29-alpine", got)
	}
	// Build hashes compare by their version part.
	itTags := []string{"2024.10.22-7ca5933", "2025.5.1-abc1234"}
	if got := newerVersionTag("2024.10.22-7ca5933", itTags); got != "2025.5.1-abc1234" {
		t.Errorf("build-hash pins: got %q, want 2025.5.1-abc1234", got)
	}
	if got := newerVersionTag("latest", vwTags); got != "" {
		t.Errorf("non-version pin must report nothing, got %q", got)
	}
}

func TestRetag(t *testing.T) {
	for _, tc := range []struct{ image, tag, want string }{
		{"vaultwarden/server:1.36.0", "1.37.0", "vaultwarden/server:1.37.0"},
		{"nginx", "1.29-alpine", "nginx:1.29-alpine"},
		{"localhost:5000/thing:1.0", "2.0", "localhost:5000/thing:2.0"},
		{"redis:7-alpine@sha256:abc", "8-alpine", "redis:8-alpine"},
	} {
		if got := retag(tc.image, tc.tag); got != tc.want {
			t.Errorf("retag(%s, %s) = %s, want %s", tc.image, tc.tag, got, tc.want)
		}
	}
}

func TestIsMajorJump(t *testing.T) {
	for _, tc := range []struct {
		cur, next string
		want      bool
	}{
		{"16-alpine", "18-alpine", true},    // postgres generation
		{"30-apache", "34-apache", true},    // nextcloud majors are stepwise
		{"2.8-alpine", "2.11-alpine", false},
		{"1.37.0", "1.37.1", false},
		{"2025.04.0", "2026.07.2", false}, // calendar versions roll routinely
	} {
		if got := isMajorJump(tc.cur, tc.next); got != tc.want {
			t.Errorf("isMajorJump(%s, %s) = %v, want %v", tc.cur, tc.next, got, tc.want)
		}
	}
}

func TestServiceDirFor(t *testing.T) {
	for in, want := range map[string]string{
		"nextcloud-db": "nextcloud", "nextcloud-app": "nextcloud", "nextcloud-redis": "nextcloud",
		"stirling-pdf": "stirling", "vaultwarden": "vaultwarden", "caddy": "caddy", "it-tools": "it-tools",
	} {
		if got := serviceDirFor(in); got != want {
			t.Errorf("serviceDirFor(%s) = %s, want %s", in, got, want)
		}
	}
}

func TestBumpImageTag(t *testing.T) {
	compose := "services:\n  vaultwarden:\n    image: vaultwarden/server:1.36.0\n    restart: unless-stopped\n"
	got, err := bumpImageTag(compose, "vaultwarden/server:1.36.0", "vaultwarden/server:1.37.0")
	if err != nil {
		t.Fatalf("bumpImageTag: %v", err)
	}
	want := "services:\n  vaultwarden:\n    image: vaultwarden/server:1.37.0\n    restart: unless-stopped\n"
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
	// Only the matching service's line changes in a multi-image file.
	multi := "  db:\n    image: postgres:16-alpine\n  app:\n    image: nextcloud:30-apache\n"
	got, err = bumpImageTag(multi, "postgres:16-alpine", "postgres:17-alpine")
	if err != nil || !strings.Contains(got, "postgres:17-alpine") || !strings.Contains(got, "nextcloud:30-apache") {
		t.Errorf("multi-image edit wrong (err %v):\n%s", err, got)
	}
	// A drifted compose file must refuse the edit, not guess.
	if _, err := bumpImageTag(compose, "vaultwarden/server:1.35.0", "vaultwarden/server:1.37.0"); err == nil {
		t.Error("drifted file should error")
	}
}

// TestRegistryTags runs the full client flow against a fake v2 registry:
// anonymous 401 -> bearer token fetch -> two Link-paginated pages.
func TestRegistryTags(t *testing.T) {
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	defer srv.Close()

	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("scope") != "repository:vaultwarden/server:pull" {
			t.Errorf("bad scope: %q", r.URL.Query().Get("scope"))
		}
		fmt.Fprint(w, `{"token":"tok123"}`)
	})
	mux.HandleFunc("/v2/vaultwarden/server/tags/list", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok123" {
			w.Header().Set("Www-Authenticate",
				fmt.Sprintf(`Bearer realm="%s/token",service="test",scope="repository:vaultwarden/server:pull"`, srv.URL))
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Query().Get("last") == "" {
			w.Header().Set("Link", `</v2/vaultwarden/server/tags/list?last=1.36.0&n=1000>; rel="next"`)
			fmt.Fprint(w, `{"tags":["1.35.0","1.36.0"]}`)
			return
		}
		fmt.Fprint(w, `{"tags":["1.37.0","latest"]}`)
	})

	tags, err := registryTags(srv.URL, "vaultwarden/server")
	if err != nil {
		t.Fatalf("registryTags: %v", err)
	}
	want := []string{"1.35.0", "1.36.0", "1.37.0", "latest"}
	if !reflect.DeepEqual(tags, want) {
		t.Errorf("got %v, want %v", tags, want)
	}
	if got := newerVersionTag("1.36.0", tags); got != "1.37.0" {
		t.Errorf("end to end: got %q, want 1.37.0", got)
	}
}
