package ws

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/auth"
	"golang.org/x/net/websocket"
)

// dialClientConn spins up a real hub upgrade endpoint and returns a
// dialed *websocket.Conn so writeFrame can be exercised against an
// actual network connection (not a mock).
func dialClientConn(t *testing.T) *websocket.Conn {
	t.Helper()
	v, err := auth.NewJWTValidator("test-secret-of-sufficient-length")
	if err != nil {
		t.Fatal(err)
	}
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
	return conn
}

// A write deadline already in the past must make writeFrame fail
// immediately rather than block — this is the core of the slow-consumer
// protection. Without SetWriteDeadline the write would proceed (or hang),
// so this test fails on the pre-fix code path.
func TestWriteFrameHonoursPastDeadline(t *testing.T) {
	t.Parallel()
	conn := dialClientConn(t)
	// 1ns deadline is already in the past by the time the write syscall
	// runs, so the socket write must time out. On the pre-fix code path
	// (no SetWriteDeadline) the write would proceed and this fails.
	c := &Client{conn: conn, writeWait: time.Nanosecond}

	if c.writeFrame([]byte(`{"type":"ev"}`)) {
		t.Fatal("writeFrame returned true with an expired write deadline; expected timeout/false")
	}
}

// With a sane positive deadline a normal frame is delivered and writeFrame
// reports success — guards against a deadline so short it breaks delivery.
func TestWriteFrameDeliversWithinDeadline(t *testing.T) {
	t.Parallel()
	conn := dialClientConn(t)
	c := &Client{conn: conn, writeWait: 5 * time.Second}

	if !c.writeFrame([]byte(`{"type":"ev"}`)) {
		t.Fatal("writeFrame returned false for a healthy write within the deadline")
	}
}

// Zero writeWait falls back to defaultWriteWait (not an instant-expiry
// zero deadline), so production clients constructed without the field set
// still get the full grace period.
func TestWriteFrameZeroUsesDefault(t *testing.T) {
	t.Parallel()
	conn := dialClientConn(t)
	c := &Client{conn: conn, writeWait: 0}

	if !c.writeFrame([]byte(`{"type":"ev"}`)) {
		t.Fatal("writeFrame with zero writeWait should use defaultWriteWait and succeed")
	}
}
