//go:build integration

package registry_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/Rishikesh01/homeserver/hsctl/internal/itest"
	"github.com/Rishikesh01/homeserver/hsctl/internal/registry"
)

// TestRefs splits and rewrites image references the way Docker reads them.
func TestRefs(t *testing.T) {
	cases := []struct {
		image, host, repo, tag string
		retag, retagged        string
	}{
		{"vaultwarden/server:1.37.4", "docker.io", "vaultwarden/server", "1.37.4", "1.37.5", "vaultwarden/server:1.37.5"},
		{"nginx:1.31.8-alpine", "docker.io", "library/nginx", "1.31.8-alpine", "1.32.0-alpine", "nginx:1.32.0-alpine"},
		{"nginx", "docker.io", "library/nginx", "latest", "1.29-alpine", "nginx:1.29-alpine"},
		{"ghcr.io/corentinth/it-tools:2024.10.22-7ca5933", "ghcr.io", "corentinth/it-tools", "2024.10.22-7ca5933", "x", "ghcr.io/corentinth/it-tools:x"},
		{"localhost:5000/thing:1.0", "localhost:5000", "thing", "1.0", "2.0", "localhost:5000/thing:2.0"},
		{"localhost/thing", "localhost", "thing", "latest", "1", "localhost/thing:1"},
		{"redis:8.10.2-alpine@sha256:abc", "docker.io", "library/redis", "8.10.2-alpine", "8-alpine", "redis:8-alpine"},
	}
	for _, tc := range cases {
		t.Run(tc.image, func(t *testing.T) {
			host, repo, tag := registry.SplitRef(tc.image)
			if host != tc.host || repo != tc.repo || tag != tc.tag {
				t.Errorf("SplitRef = (%s, %s, %s)", host, repo, tag)
			}
			if registry.Tag(tc.image) != tc.tag {
				t.Errorf("Tag = %s", registry.Tag(tc.image))
			}
			if got := registry.Retag(tc.image, tc.retag); got != tc.retagged {
				t.Errorf("Retag = %s, want %s", got, tc.retagged)
			}
		})
	}
	if registry.Base("docker.io") != "https://registry-1.docker.io" || registry.Base("ghcr.io") != "https://ghcr.io" {
		t.Errorf("Base: %s %s", registry.Base("docker.io"), registry.Base("ghcr.io"))
	}
}

// TestRealRegistry lists tags and fetches manifests from a real registry Docker pushed to.
func TestRealRegistry(t *testing.T) {
	reg := itest.StartRegistry(t)
	itest.Pull(t, itest.Busybox)
	for _, tag := range []string{"1.0.0", "1.1.0", "2.0.0"} {
		reg.Push(t, itest.Busybox, "hs/app:"+tag)
	}
	c := &registry.Client{HTTP: reg.HTTP}
	base := registry.Base(reg.Host)
	tags, err := c.Tags(base, "hs/app")
	if err != nil {
		t.Fatal(err)
	}
	accept := "application/vnd.docker.distribution.manifest.v2+json,application/vnd.oci.image.manifest.v1+json"
	cases := []struct {
		name       string
		url        string
		wantStatus int
	}{
		{"pushed tag", base + "/v2/hs/app/manifests/1.1.0", http.StatusOK},
		{"missing tag", base + "/v2/hs/app/manifests/9.9.9", http.StatusNotFound},
		{"missing repository", base + "/v2/hs/nope/manifests/1.0.0", http.StatusNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := c.Get(tc.url, accept)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != tc.wantStatus {
				t.Errorf("status = %d, want %d", resp.StatusCode, tc.wantStatus)
			}
		})
	}
	slices.Sort(tags)
	if !slices.Equal(tags, []string{"1.0.0", "1.1.0", "2.0.0"}) {
		t.Errorf("Tags = %v", tags)
	}
	if _, err := c.Tags(base, "hs/nope"); err == nil {
		t.Error("tags of a missing repository must fail")
	}
}

// TestTokenAndPages: Docker Hub and ghcr.io answer anonymously only after the bearer-token dance,
// and page long tag lists with Link headers. This stands in for them (no network in the tests).
func TestTokenAndPages(t *testing.T) {
	cases := []struct {
		name      string
		tokenOK   bool
		pages     [][]string
		wantTags  []string
		wantError bool
	}{
		{"two pages behind a token", true, [][]string{{"1.35.0", "1.36.0"}, {"1.37.0", "latest"}}, []string{"1.35.0", "1.36.0", "1.37.0", "latest"}, false},
		{"one page", true, [][]string{{"1.0"}}, []string{"1.0"}, false},
		{"token refused", false, [][]string{{"1.0"}}, nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mux := http.NewServeMux()
			srv := httptest.NewServer(mux)
			defer srv.Close()
			mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
				if !tc.tokenOK || r.URL.Query().Get("scope") != "repository:vaultwarden/server:pull" {
					w.WriteHeader(http.StatusForbidden)
					return
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
				page := 0
				fmt.Sscan(r.URL.Query().Get("page"), &page)
				if page+1 < len(tc.pages) {
					w.Header().Set("Link", fmt.Sprintf(`</v2/vaultwarden/server/tags/list?page=%d>; rel="next"`, page+1))
				}
				b := `{"tags":[`
				for i, tag := range tc.pages[page] {
					if i > 0 {
						b += ","
					}
					b += `"` + tag + `"`
				}
				fmt.Fprint(w, b+`]}`)
			})
			tags, err := registry.New().Tags(srv.URL, "vaultwarden/server")
			if (err != nil) != tc.wantError {
				t.Fatalf("err = %v, want error %v", err, tc.wantError)
			}
			if !slices.Equal(tags, tc.wantTags) {
				t.Errorf("tags = %v, want %v", tags, tc.wantTags)
			}
		})
	}
}
