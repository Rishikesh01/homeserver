//go:build integration

package layout_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Rishikesh01/homeserver/hsctl/internal/itest"
	"github.com/Rishikesh01/homeserver/hsctl/internal/layout"
)

// TestFolderFor maps every container to the compose folder that runs it.
func TestFolderFor(t *testing.T) {
	cases := map[string]string{
		"nextcloud-app": "nextcloud", "nextcloud-db": "nextcloud", "nextcloud-redis": "nextcloud",
		"stirling-pdf": "stirling", "vaultwarden": "vaultwarden", "pihole": "pihole",
		"caddy": "caddy", "it-tools": "it-tools", "imagetools": "imagetools",
	}
	for c, want := range cases {
		if got := layout.FolderFor(c); got != want {
			t.Errorf("FolderFor(%s) = %s, want %s", c, got, want)
		}
	}
}

// TestApps: everything but the proxy can be switched off, in start order.
func TestApps(t *testing.T) {
	cases := []struct {
		dir  string
		want bool
	}{
		{"vaultwarden", true}, {"stirling", true}, {"imagetools", true},
		{"caddy", false}, {"../etc", false}, {"", false},
	}
	for _, tc := range cases {
		if got := layout.IsApp(tc.dir); got != tc.want {
			t.Errorf("IsApp(%q) = %v, want %v", tc.dir, got, tc.want)
		}
	}
	if apps := layout.Apps(); slices.Contains(apps, layout.Proxy) || len(apps) != len(layout.Services)-1 {
		t.Errorf("Apps = %v", apps)
	}
}

// TestMatchesTheRepo: the layout describes the real checkout — every service has a compose file,
// the proxy starts last, and every compose file's container_name is known.
func TestMatchesTheRepo(t *testing.T) {
	root := itest.SourceRoot(t)
	if layout.Services[len(layout.Services)-1] != layout.Proxy {
		t.Errorf("the proxy must start last: %v", layout.Services)
	}
	var names []string
	for _, svc := range layout.Services {
		b, err := os.ReadFile(filepath.Join(root, svc, "docker-compose.yml"))
		if err != nil {
			t.Errorf("%s: %v", svc, err)
			continue
		}
		for _, line := range strings.Split(string(b), "\n") {
			if _, v, ok := strings.Cut(strings.TrimSpace(line), "container_name:"); ok {
				names = append(names, strings.TrimSpace(v))
			}
		}
	}
	cases := []struct {
		what string
		list []string
	}{
		{"Containers", layout.Containers},
	}
	for _, tc := range cases {
		for _, n := range names {
			if !slices.Contains(tc.list, n) {
				t.Errorf("%s lacks %s", tc.what, n)
			}
			if layout.FolderFor(n) == "" {
				t.Errorf("no folder for %s", n)
			}
		}
	}
}
