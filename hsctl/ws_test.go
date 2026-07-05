package main

import (
	"bufio"
	"io"
	"net"
	"testing"
)

// TestWSAcceptKey checks the handshake against the worked example in RFC 6455 §1.3.
// If this drifts, every browser will reject the upgrade, so it's the load-bearing test
// for the hand-rolled handshake.
func TestWSAcceptKey(t *testing.T) {
	got := wsAcceptKey("dGhlIHNhbXBsZSBub25jZQ==")
	want := "s3pPLMBiTxaQ9kYGzzhZRbK+xOo="
	if got != want {
		t.Fatalf("wsAcceptKey = %q, want %q", got, want)
	}
}

// TestWSFrameRoundTrip drives a real masked client frame through readFrame and checks a
// server frame is written unmasked with correct framing — the two halves of RFC 6455
// our terminal depends on.
func TestWSFrameRoundTrip(t *testing.T) {
	cli, srv := net.Pipe()
	defer cli.Close()
	defer srv.Close()
	ws := &wsConn{conn: srv, br: bufio.NewReader(srv)}

	// A client text frame "hi" must be masked (RFC requires client->server masking).
	mask := [4]byte{0x37, 0xfa, 0x21, 0x3d}
	payload := []byte("hi")
	masked := make([]byte, len(payload))
	for i := range payload {
		masked[i] = payload[i] ^ mask[i&3]
	}
	clientFrame := append([]byte{0x81, 0x80 | byte(len(payload)), mask[0], mask[1], mask[2], mask[3]}, masked...)

	go func() { _, _ = cli.Write(clientFrame) }()
	op, got, err := ws.ReadMessage()
	if err != nil {
		t.Fatalf("ReadMessage: %v", err)
	}
	if op != opText || string(got) != "hi" {
		t.Fatalf("read op=%d payload=%q, want text 'hi'", op, got)
	}

	// A server frame must be UNmasked, FIN set, opcode text, len in the second byte.
	// writeFrame writes the header and payload in separate Write calls, so read the
	// full 4 bytes (2 header + "hi") with ReadFull to drain both across the sync pipe.
	done := make(chan []byte, 1)
	errc := make(chan error, 1)
	go func() {
		out := make([]byte, 4)
		if _, err := io.ReadFull(cli, out); err != nil {
			errc <- err
			return
		}
		done <- out
	}()
	if err := ws.WriteMessage(opText, []byte("hi")); err != nil {
		t.Fatalf("WriteMessage: %v", err)
	}
	select {
	case err := <-errc:
		t.Fatalf("read server frame: %v", err)
	case out := <-done:
		if out[0] != 0x81 || out[1] != 0x02 || out[2] != 'h' || out[3] != 'i' {
			t.Fatalf("server frame = %v, want [0x81 0x02 'h' 'i'] (FIN+text, unmasked)", out)
		}
	}
}
