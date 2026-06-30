package main

import (
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"
)

// WebSocket keepalive: ping the browser periodically; if it's gone (laptop slept, Wi-Fi
// dropped) it stops answering, the read deadline below expires, ReadMessage returns an
// error, and the deferred teardown kills the shell. Without this a silently-dropped client
// would leak a root shell + goroutine + PTY forever. pingEvery < pongWait so a live client
// always refreshes the deadline in time.
const (
	wsPongWait  = 60 * time.Second
	wsPingEvery = 25 * time.Second
)

// The interactive admin terminal: a real shell on the server, driven from the browser
// over a WebSocket and a PTY. This is a deliberate, powerful capability — whoever holds
// the admin password gets a shell as the UI's user (root, under systemd). It's gated the
// same way the rest of /admin is (login session), only reachable on the LAN, and adds an
// Origin check so another site a logged-in admin visits can't hijack the socket.
//
// Wire format (browser <-> server):
//   - browser -> server BINARY frame  = raw keystrokes, written straight to the PTY
//   - browser -> server TEXT frame    = a JSON control message, e.g. {"resize":{"cols":120,"rows":30}}
//   - server -> browser BINARY frame  = PTY output bytes
// Keeping input as binary and control as text makes the two unambiguous without a header.

type termControl struct {
	Resize *struct {
		Cols uint16 `json:"cols"`
		Rows uint16 `json:"rows"`
	} `json:"resize"`
}

// handleTerminalWS upgrades to a WebSocket and bridges it to a login shell on a PTY.
// requireAuth has already validated the session cookie before we get here.
func (s *uiServer) handleTerminalWS(w http.ResponseWriter, r *http.Request) {
	c := LoadConfig(s.repo)
	c.Normalize()
	if !wsOriginOK(r, c.ServerIP) {
		http.Error(w, "bad WebSocket origin", http.StatusForbidden)
		return
	}
	ws, err := wsUpgrade(w, r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	defer ws.Close()

	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "bash"
		if _, err := exec.LookPath("bash"); err != nil {
			shell = "/bin/sh"
		}
	}
	cmd := exec.Command(shell)
	cmd.Dir = s.repo // land in the repo so `hsctl ...` just works
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	ptm, err := startPTY(cmd)
	if err != nil {
		_ = ws.WriteMessage(opBinary, []byte("failed to start shell: "+err.Error()+"\r\n"))
		return
	}
	// Make sure the shell process is reaped and the master closed no matter how we exit.
	defer func() {
		_ = ptm.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_, _ = cmd.Process.Wait()
	}()

	// Detect a vanished client: a read deadline (refreshed on every frame, including the
	// pong replies to the pings below) tears the read loop down if nothing arrives in time.
	ws.readTimeout = wsPongWait
	pingStop := make(chan struct{})
	defer close(pingStop)
	go func() {
		t := time.NewTicker(wsPingEvery)
		defer t.Stop()
		for {
			select {
			case <-pingStop:
				return
			case <-t.C:
				if err := ws.WriteMessage(opPing, nil); err != nil {
					return
				}
			}
		}
	}()

	// PTY -> browser. When the shell exits (read error/EOF), close the socket to unblock
	// the reader below.
	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, err := ptm.Read(buf)
			if n > 0 {
				if werr := ws.WriteMessage(opBinary, buf[:n]); werr != nil {
					break
				}
			}
			if err != nil {
				break
			}
		}
		ws.Close()
	}()

	// browser -> PTY. Binary frames are keystrokes; text frames are JSON control.
readLoop:
	for {
		op, data, err := ws.ReadMessage()
		if err != nil {
			break
		}
		switch op {
		case opText:
			var ctl termControl
			if json.Unmarshal(data, &ctl) == nil && ctl.Resize != nil {
				_ = setPTYSize(ptm, ctl.Resize.Rows, ctl.Resize.Cols)
			}
		default: // opBinary (and anything else): treat as input
			if _, werr := ptm.Write(data); werr != nil {
				break readLoop // labelled: a bare break would only exit the switch
			}
		}
	}
}

// wsOriginOK is defence-in-depth against cross-site WebSocket hijacking. Our session
// cookie is SameSite=Lax, which already keeps cross-site requests from carrying it, but
// we also require the Origin to match how this server is reached: the request's own Host,
// or the configured server IP (the address the dashboard is served on, incl. via Caddy).
func wsOriginOK(r *http.Request, serverIP string) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return false // real browsers always send Origin on a WS handshake
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	oh := u.Hostname()
	return oh == hostOnly(r.Host) || oh == serverIP
}

// hostOnly returns the hostname from a Host header, dropping any :port and the brackets
// around an IPv6 literal — so it matches url.Hostname() (which also returns "::1", not
// "[::1]"). Without the bracket handling the Origin check rejected every IPv6 connection.
func hostOnly(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		return h // also strips the [] from a bracketed IPv6 literal
	}
	// No port present: trim brackets off a bare IPv6 literal like "[::1]".
	return strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
}
