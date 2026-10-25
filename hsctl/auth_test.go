package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
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

// TestSweepSessions checks the periodic reaper drops expired tokens and keeps live ones, so the
// session map can't grow unbounded.
func TestSweepSessions(t *testing.T) {
	s := &uiServer{sessions: map[string]time.Time{
		"live1": time.Now().Add(time.Hour),
		"live2": time.Now().Add(time.Minute),
		"dead1": time.Now().Add(-time.Second),
		"dead2": time.Now().Add(-time.Hour),
	}}
	s.sweepSessions()
	if len(s.sessions) != 2 {
		t.Fatalf("after sweep want 2 live sessions, got %d: %v", len(s.sessions), s.sessions)
	}
	for _, dead := range []string{"dead1", "dead2"} {
		if _, ok := s.sessions[dead]; ok {
			t.Errorf("expired token %q should have been swept", dead)
		}
	}
	for _, live := range []string{"live1", "live2"} {
		if _, ok := s.sessions[live]; !ok {
			t.Errorf("live token %q should have survived", live)
		}
	}
}

// TestSafeNext pins the post-login open-redirect guard: only same-site absolute paths pass;
// protocol-relative and backslash-folded off-site targets fall back to /admin.
func TestSafeNext(t *testing.T) {
	cases := map[string]string{
		"/admin":            "/admin",
		"/admin/terminal":   "/admin/terminal",
		"":                  "/admin",
		"//evil.com":        "/admin",
		`/\evil.com`:        "/admin", // browsers fold \ -> /, so this is //evil.com off-site
		`/\/evil.com`:       "/admin",
		`\/evil.com`:        "/admin",
		"https://evil.com":  "/admin",
		`/ok/\notauthority`: "/admin", // any backslash is rejected (no in-app path has one)
	}
	for in, want := range cases {
		if got := safeNext(in); got != want {
			t.Errorf("safeNext(%q) = %q, want %q", in, got, want)
		}
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

// TestFlashRoundTrip: an action's result is carried to the next page in a cookie and shown once;
// a crafted ?msg= link puts nothing on the page.
func TestFlashRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		msg  string
		err  bool
	}{
		{"info", "Mounted at /mnt/backup", false},
		{"error", "Mount failed: no such device", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			redirectFlash(w, httptest.NewRequest("POST", "/admin/devices/mount", nil), "/admin/devices", tc.msg, tc.err)
			if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/admin/devices" {
				t.Fatalf("redirect = %d %q", w.Code, w.Header().Get("Location"))
			}
			r := httptest.NewRequest("GET", "/admin/devices?msg=Run+curl+evil+|+sudo+bash", nil)
			for _, c := range w.Result().Cookies() {
				r.AddCookie(c)
			}
			w2 := httptest.NewRecorder()
			if f := takeFlash(w2, r); f.Msg != tc.msg || f.Err != tc.err {
				t.Errorf("takeFlash = %+v, want %q err=%v", f, tc.msg, tc.err)
			}
			cleared := false
			for _, c := range w2.Result().Cookies() {
				cleared = cleared || (c.Name == flashCookie && c.MaxAge < 0)
			}
			if !cleared {
				t.Error("flash cookie not cleared after showing")
			}
			if f := takeFlash(httptest.NewRecorder(), httptest.NewRequest("GET", "/admin?msg=forged", nil)); f.Msg != "" {
				t.Errorf("a ?msg= link produced a flash: %q", f.Msg)
			}
		})
	}
}

// TestLoginThrottle: a few wrong passwords are free, then each failure locks the client out for
// longer; a forged X-Forwarded-For can't escape the per-peer budget; success resets the count.
func TestLoginThrottle(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name      string
		failures  int
		keys      []string
		wantWait  time.Duration
		afterWait time.Duration // checked at now+afterWait
	}{
		{"free tries", loginFreeTries, []string{"client 1.2.3.4", "peer 172.18.0.9"}, 0, 0},
		{"first lockout", loginFreeTries + 1, []string{"client 1.2.3.4", "peer 172.18.0.9"}, loginFirstLock, 0},
		{"doubles", loginFreeTries + 2, []string{"client 1.2.3.4", "peer 172.18.0.9"}, 2 * loginFirstLock, 0},
		{"capped and expires", loginFreeTries + 20, []string{"client 1.2.3.4", "peer 172.18.0.9"}, 0, loginMaxLock},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &uiServer{}
			for i := 0; i < tc.failures; i++ {
				s.loginFailed(tc.keys, now)
			}
			if got := s.loginBlocked(tc.keys, now.Add(tc.afterWait)); got != tc.wantWait {
				t.Errorf("wait = %v, want %v", got, tc.wantWait)
			}
		})
	}
	t.Run("rotating forwarded addresses hit the peer budget", func(t *testing.T) {
		s := &uiServer{}
		for i := 0; i < loginPeerFreeTries+1; i++ {
			s.loginFailed([]string{fmt.Sprintf("client 10.0.0.%d", i), "peer 172.18.0.9"}, now)
		}
		if s.loginBlocked([]string{"client 10.9.9.9", "peer 172.18.0.9"}, now) == 0 {
			t.Error("a fresh forwarded address escaped the peer lockout")
		}
	})
	t.Run("success resets", func(t *testing.T) {
		s := &uiServer{}
		keys := []string{"client 1.2.3.4", "peer 172.18.0.9"}
		for i := 0; i < loginFreeTries+3; i++ {
			s.loginFailed(keys, now)
		}
		s.loginSucceeded(keys)
		if s.loginBlocked(keys, now) != 0 {
			t.Error("still blocked after a successful login")
		}
	})
}

// TestSameOrigin: admin POSTs from another origin — another app on the same box — are refused.
func TestSameOrigin(t *testing.T) {
	cases := []struct {
		name, origin, referer string
		want                  bool
	}{
		{"own page over Caddy", "https://10.0.0.5", "", true},
		{"own page direct", "http://127.0.0.1:8088", "", true},
		{"sibling app port", "https://10.0.0.5:8444", "", false},
		{"other site", "https://evil.example", "", false},
		{"referer only, sibling", "", "https://10.0.0.5:8447/x", false},
		{"no browser headers (curl)", "", "", true},
		{"opaque origin", "null", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := setupTestRepo(t)
			if err := (Config{ServerIP: "10.0.0.5", UIPort: 8088}).Save(repo); err != nil {
				t.Fatal(err)
			}
			s := &uiServer{repo: repo}
			r := httptest.NewRequest("POST", "http://127.0.0.1:8088/admin/run", nil)
			r.Header.Set("X-Forwarded-Proto", map[bool]string{true: "https", false: ""}[strings.HasPrefix(tc.origin, "https://10.0.0.5")])
			if strings.HasPrefix(tc.origin, "https://10.0.0.5") {
				r.Host = "10.0.0.5"
			}
			if tc.origin != "" {
				r.Header.Set("Origin", tc.origin)
			}
			if tc.referer != "" {
				r.Header.Set("Referer", tc.referer)
			}
			if got := s.sameOrigin(r); got != tc.want {
				t.Errorf("sameOrigin = %v, want %v", got, tc.want)
			}
		})
	}
}
