package main

import (
	"embed"
	"net/http"
	"strings"
)

// Vendored terminal front-end (xterm.js + its fit addon + css), embedded into the binary
// so the web terminal works on a fully offline / LAN-only box — the project's target
// environment — with no CDN dependency. Update by re-downloading the pinned versions into
// hsctl/web/ (see the Terminal page's note for the versions).
//
//go:embed web/xterm.js web/xterm.css web/addon-fit.js
var webAssets embed.FS

// handleAsset serves a vendored static asset by name from web/. Names are flat (no slashes),
// so there's no path-traversal surface.
func (s *uiServer) handleAsset(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/admin/assets/")
	if name == "" || strings.ContainsAny(name, "/\\") || strings.Contains(name, "..") {
		http.NotFound(w, r)
		return
	}
	b, err := webAssets.ReadFile("web/" + name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	switch {
	case strings.HasSuffix(name, ".js"):
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	case strings.HasSuffix(name, ".css"):
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
	}
	w.Header().Set("Cache-Control", "max-age=86400")
	_, _ = w.Write(b)
}
