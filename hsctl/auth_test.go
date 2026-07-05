package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestValidSessionAndLogout pins the auth gate for /admin (root terminal, device mount,
// destructive restore): unknown/expired cookies are rejected (and expired ones purged), a live
// cookie is accepted, and logout invalidates the server-side session.
func TestValidSessionAndLogout(t *testing.T) {
	s := &uiServer{sessions: map[string]time.Time{}}

	withCookie := func(val string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/admin", nil)
		if val != "" {
			r.AddCookie(&http.Cookie{Name: sessionCookie, Value: val})
		}
		return r
	}

	if s.validSession(withCookie("")) {
		t.Error("a request with no session cookie must be invalid")
	}
	if s.validSession(withCookie("does-not-exist")) {
		t.Error("an unknown token must be invalid")
	}

	s.sessions["live"] = time.Now().Add(time.Hour)
	if !s.validSession(withCookie("live")) {
		t.Error("a live token must be valid")
	}

	// Expired token: rejected AND swept from the map when presented.
	s.sessions["stale"] = time.Now().Add(-time.Minute)
	if s.validSession(withCookie("stale")) {
		t.Error("an expired token must be invalid")
	}
	if _, ok := s.sessions["stale"]; ok {
		t.Error("an expired token must be purged from the session map")
	}

	// Logout invalidates the server-side session even though the cookie value is unchanged.
	logout := httptest.NewRequest(http.MethodPost, "/logout", nil)
	logout.AddCookie(&http.Cookie{Name: sessionCookie, Value: "live"})
	s.handleLogout(httptest.NewRecorder(), logout)
	if _, ok := s.sessions["live"]; ok {
		t.Error("logout must delete the server-side session")
	}
	if s.validSession(withCookie("live")) {
		t.Error("a session must be invalid after logout")
	}
}

// TestWSOriginOK guards the anti-cross-site-WebSocket-hijack check on the root terminal. The
// load-bearing case is a sibling service on another PORT of the same host: it shares our
// hostname and cookie, so a hostname-only check would wrongly admit it.
func TestWSOriginOK(t *testing.T) {
	const serverIP = "192.168.0.150"
	mkReq := func(host, origin, xfproto string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/admin/terminal/ws", nil)
		r.Host = host
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		if xfproto != "" {
			r.Header.Set("X-Forwarded-Proto", xfproto)
		}
		return r
	}
	cases := []struct {
		name         string
		host, origin string
		xfproto      string
		want         bool
	}{
		{"via caddy https front", serverIP, "https://" + serverIP, "https", true},
		{"direct local http", "127.0.0.1:8088", "http://127.0.0.1:8088", "", true},
		{"sibling service on another port (hijack)", serverIP, "https://" + serverIP + ":8444", "https", false},
		{"same host wrong scheme", serverIP, "http://" + serverIP + ":8444", "", false},
		{"foreign origin", serverIP, "https://evil.example.com", "https", false},
		{"missing origin", serverIP, "", "https", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := wsOriginOK(mkReq(c.host, c.origin, c.xfproto), serverIP); got != c.want {
				t.Errorf("wsOriginOK(host=%q origin=%q)=%v, want %v", c.host, c.origin, got, c.want)
			}
		})
	}
}
