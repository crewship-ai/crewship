package ws

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/websocket"
)

// newObservableConn is newUpgradedConn with a WARN-capturing logger, so a test
// can assert BOTH sides of a rejected/undecodable run-path frame (#1386): the
// operator-visible WARN line and the client-visible error frame. The returned
// buffer accumulates the hub's JSON log output.
func newObservableConn(t *testing.T) (*websocket.Conn, *lockedBuffer) {
	t.Helper()
	buf := &lockedBuffer{}
	// Fixed-level handler, immune to logging.New's process-wide level control
	// (same rationale as TestHandleUpgrade_RejectionIsLoggedWARN).
	logger := slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	v := defaultTestValidator(t)
	hub := newRunningHub(t, withValidator(v), withLogger(logger))
	hub.SetChannelAuthorizer(allowAllAuthorizer{})
	srv := httptest.NewServer(http.HandlerFunc(hub.HandleUpgrade))
	t.Cleanup(srv.Close)

	tok, err := v.IssueWSTicket("user-1", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(srv.URL)
	conn, err := websocket.Dial(fmt.Sprintf("ws://%s/", u.Host), "", srv.URL)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	wsAuth(t, conn, tok)
	return conn, buf
}

// waitForLog blocks (up to 1s) until the captured log buffer contains substr.
func waitForLog(t *testing.T, buf *lockedBuffer, substr string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(buf.String(), substr) {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for log %q; got: %s", substr, buf.String())
}

// A malformed post-auth frame is the silent-drop shape #1386 chases: before the
// fix readPump `continue`d with no log and no client frame. Now it must emit a
// WARN AND a client-readable error frame carrying the reason.
func TestReadPump_MalformedFrameIsLoggedAndSurfaced(t *testing.T) {
	t.Parallel()
	conn, buf := newObservableConn(t)

	if _, err := conn.Write([]byte("{not json")); err != nil {
		t.Fatal(err)
	}

	// (1) client-visible error frame, readable by the CLI's CloseReason.
	msg := wsRecv(t, conn)
	if msg.Type != "error" {
		t.Fatalf("type = %q, want error frame", msg.Type)
	}
	assertReasonPayload(t, msg, "malformed message frame")

	// (2) operator-visible WARN naming the reason.
	waitForLog(t, buf, "malformed message frame")
	if got := buf.String(); !strings.Contains(got, `"level":"WARN"`) {
		t.Errorf("malformed frame not logged at WARN; got: %s", got)
	}
}

// An unrecognized message type is the version-skew symptom #1386 named. Before
// the fix the switch had no default: the frame vanished silently. Now it must
// WARN (naming the type) AND return a client error frame.
func TestReadPump_UnknownTypeIsLoggedAndSurfaced(t *testing.T) {
	t.Parallel()
	conn, buf := newObservableConn(t)

	wsSend(t, conn, ClientMessage{Type: "teleport", Channel: "session:s1"})

	msg := wsRecv(t, conn)
	if msg.Type != "error" {
		t.Fatalf("type = %q, want error frame", msg.Type)
	}
	assertReasonPayload(t, msg, "unknown message type")

	waitForLog(t, buf, "unknown message type")
	got := buf.String()
	if !strings.Contains(got, `"level":"WARN"`) {
		t.Errorf("unknown type not logged at WARN; got: %s", got)
	}
	if !strings.Contains(got, "teleport") {
		t.Errorf("WARN log does not name the offending type; got: %s", got)
	}
}

// assertReasonPayload checks the error frame's payload carries the reason under
// BOTH "message" (the key the CLI CloseReason + reject-path frames use) and
// "error" (kept for back-compat), and that it contains want.
func assertReasonPayload(t *testing.T, msg ServerMessage, want string) {
	t.Helper()
	p, ok := msg.Payload.(map[string]any)
	if !ok {
		t.Fatalf("payload type = %T, want object with message+error keys", msg.Payload)
	}
	for _, key := range []string{"message", "error"} {
		v, _ := p[key].(string)
		if !strings.Contains(v, want) {
			t.Errorf("payload[%q] = %q, want it to contain %q", key, v, want)
		}
	}
}
