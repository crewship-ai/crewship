package ws

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"golang.org/x/net/websocket"
)

// newUpgradedConn spins up a hub + upgrade endpoint and dials it, returning
// both so tests can drive the readPump end-to-end and inspect hub state.
func newUpgradedConn(t *testing.T) (*Hub, *websocket.Conn) {
	t.Helper()
	v := defaultTestValidator(t)
	hub := newRunningHub(t, withValidator(v))
	hub.SetChannelAuthorizer(allowAllAuthorizer{})
	srv := httptest.NewServer(http.HandlerFunc(hub.HandleUpgrade))
	t.Cleanup(srv.Close)

	tok, err := v.IssueWSTicket("user-1", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(srv.URL)
	wsURL := fmt.Sprintf("ws://%s/?token=%s", u.Host, url.QueryEscape(tok))
	conn, err := websocket.Dial(wsURL, "", srv.URL)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return hub, conn
}

func wsSend(t *testing.T, conn *websocket.Conn, msg ClientMessage) {
	t.Helper()
	raw, _ := json.Marshal(msg)
	if _, err := conn.Write(raw); err != nil {
		t.Fatalf("ws write: %v", err)
	}
}

func wsRecv(t *testing.T, conn *websocket.Conn) ServerMessage {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	var raw []byte
	if err := websocket.Message.Receive(conn, &raw); err != nil {
		t.Fatalf("ws recv: %v", err)
	}
	var msg ServerMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		t.Fatalf("unmarshal %q: %v", raw, err)
	}
	return msg
}

// Drives every readPump dispatch arm over a real connection: malformed JSON
// is skipped, subscribe/unsubscribe mutate hub state, ping answers pong,
// send_message and cancel_message route to their handlers.
func TestReadPump_RoutesClientMessages(t *testing.T) {
	t.Parallel()
	hub, conn := newUpgradedConn(t)

	// Malformed JSON must be skipped without killing the connection.
	if _, err := conn.Write([]byte("{not json")); err != nil {
		t.Fatal(err)
	}

	// subscribe registers the channel on the hub.
	wsSend(t, conn, ClientMessage{Type: "subscribe", Channel: "session:s1"})
	waitFor(t, func() bool {
		hub.mu.RLock()
		defer hub.mu.RUnlock()
		_, ok := hub.channels["session:s1"]
		return ok
	}, "subscribe to register")

	// unsubscribe removes it again.
	wsSend(t, conn, ClientMessage{Type: "unsubscribe", Channel: "session:s1"})
	waitFor(t, func() bool {
		hub.mu.RLock()
		defer hub.mu.RUnlock()
		_, ok := hub.channels["session:s1"]
		return !ok
	}, "unsubscribe to deregister")

	// cancel_message for an unknown session is a quiet no-op.
	cancelPayload, _ := json.Marshal(sendMessagePayload{ChatID: "ghost"})
	wsSend(t, conn, ClientMessage{Type: "cancel_message", Payload: cancelPayload})

	// ping → pong (proves the conn survived the malformed frame too).
	wsSend(t, conn, ClientMessage{Type: "ping"})
	if msg := wsRecv(t, conn); msg.Type != "pong" {
		t.Errorf("type = %q, want pong", msg.Type)
	}

	// send_message with no chat handler produces an error frame.
	body, _ := json.Marshal(sendMessagePayload{ChatID: "s1", Content: "hi"})
	wsSend(t, conn, ClientMessage{Type: "send_message", Channel: "session:s1", Payload: body})
	if msg := wsRecv(t, conn); msg.Type != "error" {
		t.Errorf("type = %q, want error (chat handler not wired)", msg.Type)
	}
}

// Closing the send channel must terminate writePump and close the socket.
func TestWritePump_ExitsWhenSendClosed(t *testing.T) {
	t.Parallel()
	conn := dialClientConn(t)
	c := &Client{conn: conn, send: make(chan []byte, 1), writeWait: time.Second}

	done := make(chan struct{})
	go func() {
		c.writePump()
		close(done)
	}()

	c.send <- []byte(`{"type":"ev"}`) // one healthy frame first
	close(c.send)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("writePump did not exit after send channel close")
	}
	// Deferred conn.Close must have run.
	if _, err := conn.Write([]byte("x")); err == nil {
		t.Error("expected write on closed conn to fail after writePump exit")
	}
}

// A failing writeFrame (expired deadline) must terminate writePump.
func TestWritePump_ExitsOnWriteFailure(t *testing.T) {
	t.Parallel()
	conn := dialClientConn(t)
	c := &Client{conn: conn, send: make(chan []byte, 1), writeWait: time.Nanosecond}

	done := make(chan struct{})
	go func() {
		c.writePump()
		close(done)
	}()
	c.send <- []byte(`{"type":"ev"}`)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("writePump did not exit after frame write failure")
	}
}

// SetWriteDeadline on an already-closed conn fails → writeFrame returns false.
func TestWriteFrame_FailsOnClosedConn(t *testing.T) {
	t.Parallel()
	conn := dialClientConn(t)
	_ = conn.Close()
	c := &Client{conn: conn, writeWait: time.Second}
	if c.writeFrame([]byte("x")) {
		t.Fatal("writeFrame must fail on a closed connection")
	}
}

// watchSessionRevocation must return immediately when the connection has no
// auth session (CLI-token tickets) or missing dependencies.
func TestWatchSessionRevocation_EarlyReturns(t *testing.T) {
	t.Parallel()
	hub := newRunningHub(t)

	cases := []struct {
		name string
		c    *Client
	}{
		{"no auth session id", &Client{authSessionID: "", hub: hub}},
		{"nil hub", &Client{authSessionID: "sess-1", hub: nil}},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			done := make(chan struct{})
			go func() {
				tc.c.watchSessionRevocation()
				close(done)
			}()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("watchSessionRevocation should return immediately")
			}
		})
	}
}

// safeSend on a closed channel panics internally; the recover converts that
// into a false return instead of crashing the caller.
func TestSafeSend_RecoversFromClosedChannel(t *testing.T) {
	t.Parallel()
	hub := newRunningHub(t)
	c := newClient(t, hub, "u1")
	hub.unregister <- c
	// Wait until the hub closed the send channel.
	waitFor(t, func() bool { return hub.ConnectionCount() == 0 }, "unregister")

	if c.safeSend([]byte("after close")) {
		t.Fatal("safeSend must report false on closed channel")
	}
}

// handleCancelMessage must unwrap a double-encoded (stringified JSON) payload
// and invoke the registered cancel func.
func TestHandleCancelMessage_DoubleEncodedPayload(t *testing.T) {
	t.Parallel()
	hub := newRunningHub(t)
	c := newClient(t, hub, "u1")

	cancelled := make(chan struct{})
	hub.cancelMu.Lock()
	hub.cancelFns["u1:s9"] = func() { close(cancelled) }
	hub.cancelMu.Unlock()

	inner, _ := json.Marshal(sendMessagePayload{ChatID: "s9"})
	wrapped, _ := json.Marshal(string(inner)) // frontend double-encode
	c.handleCancelMessage(ClientMessage{Type: "cancel_message", Payload: wrapped})

	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("cancel func was not invoked for double-encoded payload")
	}
}
