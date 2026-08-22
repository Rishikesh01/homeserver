package main

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"time"

	"github.com/gorilla/websocket"
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
	c := s.config()
	if !wsOriginOK(r, c.ServerIP) {
		http.Error(w, "bad WebSocket origin", http.StatusForbidden)
		return
	}
	ws, err := (&websocket.Upgrader{
		ReadBufferSize:  32 * 1024,
		WriteBufferSize: 32 * 1024,
		// Origin validation is performed above with the configured server IP.
		CheckOrigin: func(*http.Request) bool { return true },
	}).Upgrade(w, r, nil)
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
		uiLog.Error("terminal shell failed to start", "shell", shell, "err", err)
		_ = ws.WriteMessage(websocket.BinaryMessage, []byte("failed to start shell: "+err.Error()+"\r\n"))
		return
	}
	uiLog.Info("terminal session started", "shell", shell, "from", remoteIP(r))
	defer uiLog.Info("terminal session ended", "from", remoteIP(r))
	// Make sure the shell process is reaped and the master closed no matter how we exit.
	defer func() {
		_ = ptm.Close()
		_ = cmd.Process.Kill() // non-nil: startPTY succeeded, so Start() did too
		_, _ = cmd.Process.Wait()
	}()

	// Detect a vanished client: pong replies refresh this deadline; a silent peer makes the
	// read loop fail and its deferred teardown kills the shell.
	_ = ws.SetReadDeadline(time.Now().Add(wsPongWait))
	ws.SetReadLimit(16 << 20)
	ws.SetPongHandler(func(string) error {
		return ws.SetReadDeadline(time.Now().Add(wsPongWait))
	})
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
				if err := ws.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second)); err != nil {
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
				if werr := ws.WriteMessage(websocket.BinaryMessage, buf[:n]); werr != nil {
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
		case websocket.TextMessage:
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
	if err != nil || u.Host == "" {
		return false
	}
	// Same-origin only. Compare the FULL authority (scheme + host + PORT), not just the
	// hostname: sibling services live on other ports of the same box (e.g. Nextcloud on
	// https://<ip>:8444) and share our hostname and SameSite cookie, so a hostname-only check
	// would let one of their pages open a WebSocket to this root terminal. We accept the
	// request's own origin, or the Caddy HTTPS front for the server IP (port 443 = no :port).
	got := u.Scheme + "://" + u.Host
	scheme := "http"
	if isHTTPS(r) {
		scheme = "https"
	}
	return got == scheme+"://"+r.Host || got == "https://"+serverIP
}
