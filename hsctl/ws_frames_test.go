package main

import (
	"bufio"
	"bytes"
	"net"
	"testing"
)

// buildWSFrame builds one (unmasked) WebSocket frame — enough to exercise the read path's
// length decoding and fragmentation. The parser unmasks only when the mask bit is set, so
// unmasked frames are valid input here.
func buildWSFrame(fin bool, opcode byte, payload []byte) []byte {
	h0 := opcode
	if fin {
		h0 |= 0x80
	}
	out := []byte{h0}
	n := len(payload)
	switch {
	case n < 126:
		out = append(out, byte(n))
	case n < 1<<16:
		out = append(out, 126, byte(n>>8), byte(n))
	default:
		out = append(out, 127,
			byte(n>>56), byte(n>>48), byte(n>>40), byte(n>>32),
			byte(n>>24), byte(n>>16), byte(n>>8), byte(n))
	}
	return append(out, payload...)
}

func newTestWS(t *testing.T, frames ...[]byte) *wsConn {
	t.Helper()
	cli, srv := net.Pipe()
	t.Cleanup(func() { cli.Close(); srv.Close() })
	go func() {
		for _, f := range frames {
			if _, err := cli.Write(f); err != nil {
				return
			}
		}
	}()
	return &wsConn{conn: srv, br: bufio.NewReader(srv)}
}

// TestWSReadFrameEdges covers the hand-rolled frame parser's edge cases: the 16-bit extended
// length (126), the oversize cap (which must reject before allocating), and fragmentation
// reassembly across a continuation frame.
func TestWSReadFrameEdges(t *testing.T) {
	t.Run("extended 16-bit length", func(t *testing.T) {
		payload := bytes.Repeat([]byte("x"), 300) // >125 => 126 + 2-byte length
		ws := newTestWS(t, buildWSFrame(true, opText, payload))
		fin, op, got, err := ws.readFrame()
		if err != nil {
			t.Fatalf("readFrame: %v", err)
		}
		if !fin || op != opText || !bytes.Equal(got, payload) {
			t.Fatalf("fin=%v op=%d len=%d, want fin text len=300", fin, op, len(got))
		}
	})

	t.Run("oversize frame is rejected", func(t *testing.T) {
		// 64-bit length header declaring 32 MiB (> the 16 MiB cap); no payload follows because
		// the parser must bail on the length alone, without trying to allocate/read it.
		hdr := []byte{0x82, 127, 0, 0, 0, 0, 0x02, 0, 0, 0}
		ws := newTestWS(t, hdr)
		if _, _, _, err := ws.readFrame(); err == nil {
			t.Fatal("expected an error for an oversize frame, got nil")
		}
	})

	t.Run("fragmented message is reassembled", func(t *testing.T) {
		ws := newTestWS(t,
			buildWSFrame(false, opText, []byte("he")),
			buildWSFrame(true, opContinuation, []byte("llo")),
		)
		op, msg, err := ws.ReadMessage()
		if err != nil {
			t.Fatalf("ReadMessage: %v", err)
		}
		if op != opText || string(msg) != "hello" {
			t.Fatalf("op=%d msg=%q, want text \"hello\"", op, msg)
		}
	})
}
