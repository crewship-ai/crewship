package cli

import (
	"encoding/json"
	"testing"
)

// TestCloseReason pins the extraction of a server rejection reason from the
// one error/session_revoked frame the hub sends before closing (#1386).
func TestCloseReason(t *testing.T) {
	mk := func(typ, message string) *WSMessage {
		p, _ := json.Marshal(map[string]string{"message": message})
		return &WSMessage{Type: typ, Payload: p}
	}

	if r, ok := CloseReason(mk("error", "invalid or expired ws-token")); !ok || r != "invalid or expired ws-token" {
		t.Errorf("error frame: got (%q,%v), want the reason", r, ok)
	}
	// session_revoked with an empty message falls back to the type label.
	if r, ok := CloseReason(mk("session_revoked", "")); !ok || r != "session_revoked" {
		t.Errorf("session_revoked frame: got (%q,%v), want session_revoked", r, ok)
	}
	// A normal chat_event is not a close reason.
	if _, ok := CloseReason(&WSMessage{Type: "chat_event"}); ok {
		t.Error("chat_event must not be treated as a close reason")
	}
	if _, ok := CloseReason(nil); ok {
		t.Error("nil message must not be a close reason")
	}
}

// TestWSClientSurfacesCloseReason exercises the read-loop pattern both CLI run
// loops use: the server writes an error frame then closes; the loop captures
// the reason via CloseReason and, on the subsequent EOF, has a reason to print
// instead of a bare "ws read: EOF".
func TestWSClientSurfacesCloseReason(t *testing.T) {
	serverURL, _, send, stop := startTestWSServer(t)

	c, err := NewWSClient(serverURL, "tok")
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer c.Close()

	// Server writes one rejection frame (buffered), then stop() closes send —
	// the handler drains + writes the frame, then closes the conn → client EOF.
	frame, _ := json.Marshal(WSMessage{
		Type:    "error",
		Payload: json.RawMessage(`{"message":"invalid or expired ws-token"}`),
	})
	send <- frame
	stop() // closes send (flushes the frame) + the httptest server; call once.

	var closeReason string
	for {
		msg, err := c.ReadMessage()
		if err != nil {
			// EOF: the reason captured from the frame above is what a real
			// CLI loop prints in place of "ws read: EOF".
			if closeReason != "invalid or expired ws-token" {
				t.Fatalf("on EOF, closeReason = %q, want the server's reason (bare EOF would be opaque)", closeReason)
			}
			return
		}
		if reason, ok := CloseReason(msg); ok {
			closeReason = reason
		}
	}
}
