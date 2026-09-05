package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Service is one tile on the dashboard. The registry lives in services.json at the
// repo root; edit it (add/remove an entry) and the homepage updates on next load.
// Each app is reached at https://<server-ip>:<HTTPSPort><Path> (Caddy terminates TLS) — and,
// with Let's Encrypt on, also at https://<Host>.<domain><Path> (see tls.go).
type Service struct {
	Key       string `json:"key"`
	Name      string `json:"name"`
	Icon      string `json:"icon"`
	Desc      string `json:"desc"`
	HTTPSPort int    `json:"https_port"`
	Path      string `json:"path,omitempty"`
	Host      string `json:"host,omitempty"` // subdomain under the Let's Encrypt domain; defaults to Key
}

func defaultServices() []Service {
	return []Service{
		{Key: "vault", Name: "Passwords", Icon: "🔑", Desc: "Vaultwarden — save & sync passwords (Bitwarden-compatible)", HTTPSPort: 8443},
		{Key: "cloud", Name: "Files", Icon: "☁️", Desc: "Nextcloud — files, photos, calendar", HTTPSPort: 8444},
		{Key: "pihole", Name: "Ad blocker", Icon: "🛡️", Desc: "Pi-hole — network-wide ad-blocking admin", HTTPSPort: 8445, Path: "/admin"},
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

// URL builds the https address a tile links to: the IP:port form.
func (s Service) URL(ip string) string {
	return fmt.Sprintf("https://%s:%d%s", ip, s.HTTPSPort, s.Path)
}

// Subdomain is the label this app gets under the Let's Encrypt domain (vault.<domain>, …).
func (s Service) Subdomain() string {
	if s.Host != "" {
		return s.Host
	}
	return s.Key
}

// DomainURL builds the public-domain address (Let's Encrypt mode): https://<sub>.<domain><path>.
func (s Service) DomainURL(domain string) string {
	return "https://" + s.Subdomain() + "." + domain + s.Path
}
