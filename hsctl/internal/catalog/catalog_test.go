//go:build integration

package catalog_test

import (
	"path/filepath"
	"testing"

	"github.com/Rishikesh01/homeserver/hsctl/internal/catalog"
	"github.com/Rishikesh01/homeserver/hsctl/internal/itest"
)

// TestLoad: the tiles come from services.json, falling back to the built-in ones when it's missing,
// broken or empty — and the repo's own services.json parses, with every tile tied to a folder.
func TestLoad(t *testing.T) {
	cases := []struct {
		name, content string // "" = no file
		wantKeys      []string
	}{
		{"no file", "", []string{"vault", "cloud", "pihole"}},
		{"broken json", "[{", []string{"vault", "cloud", "pihole"}},
		{"empty list", "[]", []string{"vault", "cloud", "pihole"}},
		{"custom", `[{"key":"pdf","name":"PDF tools","icon":"x","desc":"d","https_port":8446,"dir":"stirling"}]`, []string{"pdf"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "services.json")
			if tc.content != "" {
				itest.WriteFile(t, filepath.Dir(p), "services.json", tc.content)
			}
			tiles := catalog.Load(p)
			var keys []string
			for _, tl := range tiles {
				keys = append(keys, tl.Key)
			}
			if len(keys) != len(tc.wantKeys) {
				t.Fatalf("keys = %v, want %v", keys, tc.wantKeys)
			}
			for i := range keys {
				if keys[i] != tc.wantKeys[i] {
					t.Fatalf("keys = %v, want %v", keys, tc.wantKeys)
				}
			}
		})
	}
	real := catalog.Load(filepath.Join(itest.SourceRoot(t), "services.json"))
	if len(real) < 3 {
		t.Fatalf("the repo's services.json didn't load: %v", real)
	}
	for _, tl := range real {
		if tl.Dir == "" || tl.HTTPSPort == 0 || tl.Name == "" {
			t.Errorf("incomplete tile in services.json: %+v", tl)
		}
	}
}

// TestURL: each tile links to Caddy's HTTPS port for it, plus its path.
func TestURL(t *testing.T) {
	cases := []struct {
		tile catalog.Tile
		ip   string
		want string
	}{
		{catalog.Tile{HTTPSPort: 8443}, "192.168.1.10", "https://192.168.1.10:8443"},
		{catalog.Tile{HTTPSPort: 8445, Path: "/admin"}, "10.0.0.5", "https://10.0.0.5:8445/admin"},
	}
	for _, tc := range cases {
		if got := tc.tile.URL(tc.ip); got != tc.want {
			t.Errorf("URL = %s, want %s", got, tc.want)
		}
	}
}
