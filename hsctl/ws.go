package main

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
)

// A minimal RFC 6455 WebSocket server — just enough to carry the interactive terminal,
// with no third-party dependency (gorilla/nhooyr aren't vendored, and we keep the
// offline build self-contained). It does the upgrade handshake, then reads/writes data
// frames; it handles ping/pong and close, and unmasks client frames as the spec
// requires. It is NOT a general-purpose library: no fragmentation reassembly across
// many frames, no permessage-deflate. The terminal sends small frames, so that's fine.

const wsGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

// wsAcceptKey computes the Sec-WebSocket-Accept value for a client's Sec-WebSocket-Key,
// per RFC 6455: base64(sha1(key + GUID)).
func wsAcceptKey(key string) string {
	sum := sha1.Sum([]byte(key + wsGUID))
	return base64.StdEncoding.EncodeToString(sum[:])
}

// wsConn is an upgraded connection. Use ReadMessage/WriteMessage; it owns the raw conn.
type wsConn struct {
	conn net.Conn
	br   *bufio.Reader
	wmu  sync.Mutex // serialises frame writes (PTY pump + ping/pong can race)
}

// WebSocket opcodes (the ones we care about).
const (
	opContinuation = 0x0
	opText         = 0x1
	opBinary       = 0x2
	opClose        = 0x8
	opPing         = 0x9
	opPong         = 0xA
)

// wsUpgrade performs the server handshake and hijacks the TCP connection. The caller
// must have already authenticated the request (the handshake is a normal HTTP GET, so
// our session cookie and Origin checks run before this).
func wsUpgrade(w http.ResponseWriter, r *http.Request) (*wsConn, error) {
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") ||
		!strings.Contains(strings.ToLower(r.Header.Get("Connection")), "upgrade") {
		return nil, fmt.Errorf("not a websocket upgrade request")
	}
	key := r.Header.Get("Sec-WebSocket-Key")
	if key == "" {
		return nil, fmt.Errorf("missing Sec-WebSocket-Key")
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		return nil, fmt.Errorf("response writer does not support hijacking")
	}
	conn, brw, err := hj.Hijack()
	if err != nil {
		return nil, err
	}
	resp := "HTTP/1.1 101 Switching Protocols\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Accept: " + wsAcceptKey(key) + "\r\n\r\n"
	if _, err := io.WriteString(conn, resp); err != nil {
		conn.Close()
		return nil, err
	}
	return &wsConn{conn: conn, br: brw.Reader}, nil
}

// ReadMessage returns the next data/control message: its opcode and payload. Ping is
// answered with a pong transparently and the read continues; a close frame returns
// io.EOF. Control frames are handled here so callers only see text/binary data.
func (c *wsConn) ReadMessage() (opcode byte, payload []byte, err error) {
	for {
		fin, op, data, err := c.readFrame()
		if err != nil {
			return 0, nil, err
		}
		switch op {
		case opPing:
			_ = c.writeFrame(opPong, data)
			continue
		case opPong:
			continue
		case opClose:
			_ = c.writeFrame(opClose, nil)
			return 0, nil, io.EOF
		}
		// Reassemble a fragmented message (op on the first frame, continuation after).
		full := data
		curOp := op
		for !fin {
			fin, op, data, err = c.readFrame()
			if err != nil {
				return 0, nil, err
			}
			if op != opContinuation {
				return 0, nil, fmt.Errorf("expected continuation frame, got opcode %d", op)
			}
			full = append(full, data...)
		}
		return curOp, full, nil
	}
}

// readFrame reads one WebSocket frame, unmasking the payload (client frames are always
// masked). It enforces a payload cap so a hostile client can't make us allocate wildly.
func (c *wsConn) readFrame() (fin bool, opcode byte, payload []byte, err error) {
	var h [2]byte
	if _, err = io.ReadFull(c.br, h[:]); err != nil {
		return
	}
	fin = h[0]&0x80 != 0
	opcode = h[0] & 0x0f
	masked := h[1]&0x80 != 0
	length := uint64(h[1] & 0x7f)
	switch length {
	case 126:
		var ext [2]byte
		if _, err = io.ReadFull(c.br, ext[:]); err != nil {
			return
		}
		length = uint64(binary.BigEndian.Uint16(ext[:]))
	case 127:
		var ext [8]byte
		if _, err = io.ReadFull(c.br, ext[:]); err != nil {
			return
		}
		length = binary.BigEndian.Uint64(ext[:])
	}
	const maxFrame = 1 << 20 // 1 MiB is plenty for keystrokes + resize control frames
	if length > maxFrame {
		return false, 0, nil, fmt.Errorf("websocket frame too large: %d bytes", length)
	}
	var mask [4]byte
	if masked {
		if _, err = io.ReadFull(c.br, mask[:]); err != nil {
			return
		}
	}
	payload = make([]byte, length)
	if _, err = io.ReadFull(c.br, payload); err != nil {
		return
	}
	if masked {
		for i := range payload {
			payload[i] ^= mask[i&3]
		}
	}
	return fin, opcode, payload, nil
}

// WriteMessage sends a single (unfragmented, unmasked) server frame.
func (c *wsConn) WriteMessage(opcode byte, payload []byte) error {
	return c.writeFrame(opcode, payload)
}

func (c *wsConn) writeFrame(opcode byte, payload []byte) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	var head []byte
	b0 := byte(0x80) | opcode // FIN set
	n := len(payload)
	switch {
	case n < 126:
		head = []byte{b0, byte(n)}
	case n < 1<<16:
		head = []byte{b0, 126, byte(n >> 8), byte(n)}
	default:
		head = []byte{b0, 127,
			byte(n >> 56), byte(n >> 48), byte(n >> 40), byte(n >> 32),
			byte(n >> 24), byte(n >> 16), byte(n >> 8), byte(n)}
	}
	if _, err := c.conn.Write(head); err != nil {
		return err
	}
	if n > 0 {
		if _, err := c.conn.Write(payload); err != nil {
			return err
		}
	}
	return nil
}

// Close sends a close frame (best-effort) and tears down the connection.
func (c *wsConn) Close() error {
	_ = c.writeFrame(opClose, nil)
	return c.conn.Close()
}
