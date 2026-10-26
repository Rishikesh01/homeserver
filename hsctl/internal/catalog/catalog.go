// Package catalog is the dashboard's list of apps (its tiles): services.json at the repo root, or
// the built-in defaults. Each app is reached at https://<server-ip>:<HTTPSPort><Path> (Caddy
// terminates TLS).
package catalog

import (
	"encoding/json"
	"fmt"
	"os"
)

// Tile is one app on the home page. Edit services.json (add/remove an entry) and the home page
// updates on the next load.
type Tile struct {
	Key       string `json:"key"`
	Name      string `json:"name"`
	Icon      string `json:"icon"`
	Desc      string `json:"desc"`
	HTTPSPort int    `json:"https_port"`
	Path      string `json:"path,omitempty"`
	// Dir is the app's compose folder (vaultwarden, nextcloud, …) — what the on/off switch acts on.
	// A tile whose Dir is switched off is hidden from the home page.
	Dir string `json:"dir,omitempty"`
}

// Defaults are the tiles used when services.json is missing or unreadable.
func Defaults() []Tile {
	return []Tile{
		{"vault", "Passwords", "🔑", "Vaultwarden — save & sync passwords (Bitwarden-compatible)", 8443, "", "vaultwarden"},
		{"cloud", "Files", "☁️", "Nextcloud — files, photos, calendar", 8444, "", "nextcloud"},
		{"pihole", "Ad blocker", "🛡️", "Pi-hole — network-wide ad-blocking admin", 8445, "/admin", "pihole"},
	}
}

// Load reads the tiles from path (services.json); it falls back to Defaults.
func Load(path string) []Tile {
	b, err := os.ReadFile(path)
	if err != nil {
		return Defaults()
	}
	var t []Tile
	if err := json.Unmarshal(b, &t); err != nil || len(t) == 0 {
		return Defaults()
	}
	return t
}

// URL builds the https address a tile links to.
func (t Tile) URL(ip string) string {
	return fmt.Sprintf("https://%s:%d%s", ip, t.HTTPSPort, t.Path)
}
