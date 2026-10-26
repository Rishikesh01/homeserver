package registry

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestSplitImageRef(t *testing.T) {
	for _, tc := range []struct{ image, host, repo, tag string }{
		{"vaultwarden/server:1.36.0", "docker.io", "vaultwarden/server", "1.36.0"},
		{"nginx:1.27-alpine", "docker.io", "library/nginx", "1.27-alpine"},
		{"nginx", "docker.io", "library/nginx", "latest"},
		{"ghcr.io/corentinth/it-tools:2024.10.22-7ca5933", "ghcr.io", "corentinth/it-tools", "2024.10.22-7ca5933"},
		{"localhost:5000/thing:1.0", "localhost:5000", "thing", "1.0"},
		{"redis:7-alpine@sha256:abc", "docker.io", "library/redis", "7-alpine"},
	} {
		host, repo, tag := SplitRef(tc.image)
		if host != tc.host || repo != tc.repo || tag != tc.tag {
			t.Errorf("%s: got (%s, %s, %s)", tc.image, host, repo, tag)
		}
	}
}

func TestRetag(t *testing.T) {
	for _, tc := range []struct{ image, tag, want string }{
		{"vaultwarden/server:1.36.0", "1.37.0", "vaultwarden/server:1.37.0"},
		{"nginx", "1.29-alpine", "nginx:1.29-alpine"},
		{"localhost:5000/thing:1.0", "2.0", "localhost:5000/thing:2.0"},
		{"redis:7-alpine@sha256:abc", "8-alpine", "redis:8-alpine"},
	} {
		if got := Retag(tc.image, tc.tag); got != tc.want {
			t.Errorf("Retag(%s, %s) = %s, want %s", tc.image, tc.tag, got, tc.want)
		}
	}
}

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

	tags, err := New().Tags(srv.URL, "vaultwarden/server")
	if err != nil {
		t.Fatalf("registryTags: %v", err)
	}
	want := []string{"1.35.0", "1.36.0", "1.37.0", "latest"}
	if !reflect.DeepEqual(tags, want) {
		t.Errorf("got %v, want %v", tags, want)
	}
}
