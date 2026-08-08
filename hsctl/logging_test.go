package main

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRemoteIP(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "172.17.0.2:51234"
	if got := remoteIP(r); got != "172.17.0.2" {
		t.Errorf("socket peer: got %q", got)
	}
	r.Header.Set("X-Forwarded-For", "192.168.1.50, 172.17.0.2")
	if got := remoteIP(r); got != "192.168.1.50" {
		t.Errorf("forwarded: got %q", got)
	}
}

// TestLogRequests proves the middleware records the real status, keeps quiet paths at
// debug level, and doesn't break handlers that flush (streamed command output).
func TestLogRequests(t *testing.T) {
	var buf bytes.Buffer
	orig := uiLog
	uiLog = slog.New(slog.NewTextHandler(&buf, nil))
	defer func() { uiLog = orig }()

	h := logRequests(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/missing" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.(http.Flusher).Flush() // must not panic behind the wrapper
	}))

	for _, path := range []string{"/missing", "/admin", "/admin/assets/xterm.js"} {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", path, nil))
	}
	out := buf.String()
	if !strings.Contains(out, "path=/missing") || !strings.Contains(out, "status=404") {
		t.Errorf("missing 404 line in:\n%s", out)
	}
	if !strings.Contains(out, "path=/admin ") || !strings.Contains(out, "status=200") {
		t.Errorf("missing 200 line in:\n%s", out)
	}
	if strings.Contains(out, "xterm.js") {
		t.Errorf("quiet asset path should not log at info level:\n%s", out)
	}
}
