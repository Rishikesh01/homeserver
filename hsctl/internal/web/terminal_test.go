//go:build integration

package web_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// TestTerminal: the admin terminal is a real shell on a PTY over a WebSocket — only for a logged-in
// admin, and only from the dashboard's own origin, never from a sibling app on another port.
func TestTerminal(t *testing.T) {
	d := newDash(t, configured(t), nil)
	d.login()
	wsURL := "ws" + strings.TrimPrefix(d.url, "http") + "/admin/terminal/ws"
	cases := []struct {
		name     string
		origin   string
		cookies  bool
		wantCode int // the refused handshake's status, 0 = the shell opens
	}{
		{"own origin", d.url, true, 0},
		{"Caddy's front for the server", "https://10.0.0.5", true, 0},
		{"sibling app on another port", "https://10.0.0.5:8444", true, 403},
		{"other site", "https://evil.example", true, 403},
		{"no origin", "", true, 403},
		{"not logged in", d.url, false, 303},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hdr := http.Header{}
			if tc.origin != "" {
				hdr.Set("Origin", tc.origin)
			}
			if tc.cookies {
				for _, c := range d.c.Jar.Cookies(mustURL(d.url)) {
					hdr.Add("Cookie", c.Name+"="+c.Value)
				}
			}
			ws, resp, err := websocket.DefaultDialer.Dial(wsURL, hdr)
			if tc.wantCode != 0 {
				if err == nil {
					ws.Close()
					t.Fatal("the shell opened")
				}
				if resp == nil || resp.StatusCode != tc.wantCode {
					t.Fatalf("handshake: %v %v", resp, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("handshake: %v", err)
			}
			defer ws.Close()
			if err := ws.WriteMessage(websocket.TextMessage, []byte(`{"resize":{"cols":120,"rows":30}}`)); err != nil {
				t.Fatal(err)
			}
			if err := ws.WriteMessage(websocket.BinaryMessage, []byte("echo $((6*7)) $(pwd) $(stty size)\n")); err != nil {
				t.Fatal(err)
			}
			var seen strings.Builder
			deadline := time.Now().Add(10 * time.Second)
			for !strings.Contains(seen.String(), "42 "+d.repo.Root+" 30 120") {
				_ = ws.SetReadDeadline(deadline)
				_, msg, err := ws.ReadMessage()
				if err != nil {
					t.Fatalf("reading the shell: %v\n%q", err, seen.String())
				}
				seen.Write(msg)
			}
		})
	}
	if !strings.Contains(d.log.String(), "terminal session started") {
		t.Errorf("sessions aren't logged:\n%s", d.log.String())
	}
}

func mustURL(s string) *url.URL {
	u, err := url.Parse(s)
	if err != nil {
		panic(err)
	}
	return u
}
