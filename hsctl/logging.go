package main

import (
	"bufio"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

// Web-UI logging. The UI runs under systemd, so structured lines on stderr land in
// the journal (journalctl -u hsctl-ui). Only server-side events log here — CLI
// commands keep their plain, user-facing stdout.
var uiLog = slog.New(slog.NewTextHandler(os.Stderr, nil))

// remoteIP is the client address for log lines: the first X-Forwarded-For hop when
// Caddy proxied the request, else the socket peer. (The UI is only reachable via
// loopback and the docker bridge, so the header is Caddy's, not an outsider's.)
func remoteIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		first, _, _ := strings.Cut(xff, ",")
		return strings.TrimSpace(first)
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

// statusWriter records the response status for the request log. It passes Flush and
// Hijack through so streamed output (/admin/run) and the terminal WebSocket work
// unchanged behind the middleware.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hj, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("response writer does not support hijacking")
	}
	return hj.Hijack()
}

// quietPaths are logged only at debug level: static assets, and the progress endpoints the
// browser polls every few seconds (restore, Let's Encrypt apply) — routine noise that would
// bury the interesting lines in the journal.
func quietPath(path string) bool {
	return strings.HasPrefix(path, "/admin/assets/") ||
		path == "/admin/backup/restore/status" || path == "/admin/letsencrypt/status"
}

// logRequests logs every finished request: method, path, status, duration, client.
func logRequests(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		start := time.Now()
		h.ServeHTTP(sw, r)
		level := slog.LevelInfo
		if quietPath(r.URL.Path) {
			level = slog.LevelDebug
		}
		uiLog.Log(r.Context(), level, "http",
			"method", r.Method, "path", r.URL.Path, "status", sw.status,
			"dur", time.Since(start).Round(time.Millisecond).String(), "from", remoteIP(r))
	})
}
