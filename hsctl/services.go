package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Service is one tile on the dashboard. The registry lives in services.json at the
// repo root; edit it (add/remove an entry) and the homepage updates on next load.
// Each app is reached at https://<server-ip>:<HTTPSPort><Path> (Caddy terminates TLS).
type Service struct {
	Key       string `json:"key"`
	Name      string `json:"name"`
	Icon      string `json:"icon"`
	Desc      string `json:"desc"`
	HTTPSPort int    `json:"https_port"`
	Path      string `json:"path,omitempty"`
}

func defaultServices() []Service {
	return []Service{
		{"vault", "Passwords", "🔑", "Vaultwarden — save & sync passwords (Bitwarden-compatible)", 8443, ""},
		{"cloud", "Files", "☁️", "Nextcloud — files, photos, calendar", 8444, ""},
		{"pihole", "Ad blocker", "🛡️", "Pi-hole — network-wide ad-blocking admin", 8445, "/admin"},
	}
}

// LoadServices reads services.json from the repo; falls back to built-in defaults.
func LoadServices(repo string) []Service {
	b, err := os.ReadFile(filepath.Join(repo, "services.json"))
	if err != nil {
		return defaultServices()
	}
	var s []Service
	if err := json.Unmarshal(b, &s); err != nil || len(s) == 0 {
		return defaultServices()
	}
	return s
}

// URL builds the https address a tile links to.
func (s Service) URL(ip string) string {
	return fmt.Sprintf("https://%s:%d%s", ip, s.HTTPSPort, s.Path)
}
