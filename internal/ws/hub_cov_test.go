package ws

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/auth/sessions"
	"golang.org/x/net/websocket"
)

// stubSessions lets each test script the Get result; writes are no-ops.
type stubSessions struct {
	get func(ctx context.Context, id string) (*sessions.Session, error)
}

func (s *stubSessions) Create(context.Context, string, string, string, time.Duration) (*sessions.Session, error) {
	return nil, errors.New("not supported")
}
func (s *stubSessions) Get(ctx context.Context, id string) (*sessions.Session, error) {
	return s.get(ctx, id)
}
func (s *stubSessions) ListActiveForUser(context.Context, string) ([]*sessions.Session, error) {
	return nil, nil
}
func (s *stubSessions) Revoke(context.Context, string, string) error { return nil }
func (s *stubSessions) RevokeAllForUser(context.Context, string, string) (int64, error) {
	return 0, nil
}
func (s *stubSessions) TouchLastUsed(context.Context, string) error { return nil }
func (s *stubSessions) RotateRefreshJti(context.Context, string, string, string) error {
	return nil
}
func (s *stubSessions) SetClock(func() time.Time) {}

func withSessions(st sessions.Store) func(*hubOpts) {
	return func(o *hubOpts) { o.sessions = st }
}

func TestNewHub_PanicsWithoutLogger(t *testing.T) {
	t.Parallel()
	defer func() {
		if recover() == nil {
			t.Error("expected NewHub to panic on nil logger")
		}
	}()
	NewHub(nil, nil, NopValidatorForTests, NopSessionsForTests)
}

// An unmarshalable payload must be dropped before it reaches the broadcast
// channel — subscribers see nothing and the hub keeps running.
func TestBroadcast_UnmarshalablePayloadIsDropped(t *testing.T) {
	t.Parallel()
	hub := newRunningHub(t)
	hub.SetChannelAuthorizer(allowAllAuthorizer{})

	c := newClient(t, hub, "u1")
	c.subscribe("session:s1")

	hub.Broadcast("session:s1", ServerMessage{Type: "bad", Payload: make(chan int)})
	expectNothing(t, c.send, 50*time.Millisecond)

	// Hub still works for well-formed messages afterwards.
	hub.Broadcast("session:s1", ServerMessage{Type: "ok", Payload: "fine"})
	recvOrTimeout(t, c.send)
}

func TestBroadcastExcept_UnmarshalablePayloadIsDropped(t *testing.T) {
	t.Parallel()
	hub := newRunningHub(t)
	hub.SetChannelAuthorizer(allowAllAuthorizer{})

	c := newClient(t, hub, "u1")
	c.subscribe("session:s1")

	hub.BroadcastExcept("session:s1", nil, ServerMessage{Type: "bad", Payload: make(chan int)})
	expectNothing(t, c.send, 50*time.Millisecond)
}

// forceDisconnect on a client with a live socket must close it.
func TestForceDisconnect_ClosesLiveConn(t *testing.T) {
	t.Parallel()
	conn := dialClientConn(t)
	ctx, cancel := context.WithCancel(context.Background())
	c := &Client{conn: conn, ctx: ctx, cancel: cancel}

	c.forceDisconnect()

	if ctx.Err() == nil {
		t.Error("forceDisconnect must cancel the client context")
	}
	if _, err := conn.Write([]byte("x")); err == nil {
		t.Error("expected write on force-disconnected conn to fail")
	}
}

// --- HandleUpgrade: session-bound (sid) tickets ---

// dialWithSid dials and completes the post-upgrade auth handshake for a
// sid-carrying ticket. The upgrade itself always succeeds now (auth moved
// post-upgrade — see hub.go authenticateUpgradedConn); session-revocation
// rejection surfaces as a frame + close on the live connection instead of
// an HTTP status, so callers read that frame themselves.
func dialWithSid(t *testing.T, store sessions.Store, sid string) *websocket.Conn {
	t.Helper()
	v := defaultTestValidator(t)
	hub := newRunningHub(t, withValidator(v), withSessions(store))
	hub.SetChannelAuthorizer(allowAllAuthorizer{})
	srv := httptest.NewServer(http.HandlerFunc(hub.HandleUpgrade))
	t.Cleanup(srv.Close)

	tok, err := v.IssueWSTicket("user-1", sid, "", "")
	if err != nil {
		t.Fatal(err)
	}

	u, _ := url.Parse(srv.URL)
	conn, dialErr := websocket.Dial(fmt.Sprintf("ws://%s/", u.Host), "", srv.URL)
	if dialErr != nil {
		t.Fatalf("dial (upgrade must succeed pre-auth): %v", dialErr)
	}
	t.Cleanup(func() { _ = conn.Close() })
	wsAuth(t, conn, tok)
	return conn
}

// recvAuthFrame reads the single frame HandleUpgrade sends on an auth
// rejection (session_revoked or a generic error) before closing.
func recvAuthFrame(t *testing.T, conn *websocket.Conn) ServerMessage {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	var raw []byte
	if err := websocket.Message.Receive(conn, &raw); err != nil {
		t.Fatalf("expected an auth-rejection frame before close, got read error: %v", err)
	}
	var msg ServerMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		t.Fatalf("unmarshal %q: %v", raw, err)
	}
	return msg
}

func TestHandleUpgrade_SessionMissingIsRevoked(t *testing.T) {
	t.Parallel()
	store := &stubSessions{get: func(context.Context, string) (*sessions.Session, error) {
		return nil, sessions.ErrNotFound
	}}
	conn := dialWithSid(t, store, "sess-gone")
	if msg := recvAuthFrame(t, conn); msg.Type != "session_revoked" {
		t.Errorf("type = %q, want session_revoked", msg.Type)
	}
}

func TestHandleUpgrade_SessionLookupErrorIs500(t *testing.T) {
	t.Parallel()
	store := &stubSessions{get: func(context.Context, string) (*sessions.Session, error) {
		return nil, errors.New("db timeout")
	}}
	conn := dialWithSid(t, store, "sess-1")
	if msg := recvAuthFrame(t, conn); msg.Type != "error" {
		t.Errorf("type = %q, want error", msg.Type)
	}
}

func TestHandleUpgrade_InactiveSessionIsRevoked(t *testing.T) {
	t.Parallel()
	revokedAt := time.Now().Add(-time.Hour)
	store := &stubSessions{get: func(_ context.Context, id string) (*sessions.Session, error) {
		return &sessions.Session{
			ID:        id,
			UserID:    "user-1",
			ExpiresAt: time.Now().Add(time.Hour),
			RevokedAt: &revokedAt,
		}, nil
	}}
	conn := dialWithSid(t, store, "sess-1")
	if msg := recvAuthFrame(t, conn); msg.Type != "session_revoked" {
		t.Errorf("type = %q, want session_revoked", msg.Type)
	}
}

func TestHandleUpgrade_ActiveSessionConnects(t *testing.T) {
	t.Parallel()
	store := &stubSessions{get: func(_ context.Context, id string) (*sessions.Session, error) {
		return &sessions.Session{
			ID:        id,
			UserID:    "user-1",
			ExpiresAt: time.Now().Add(time.Hour),
		}, nil
	}}
	conn := dialWithSid(t, store, "sess-1")
	// The connection is live end-to-end: ping → pong.
	wsSend(t, conn, ClientMessage{Type: "ping"})
	if msg := wsRecv(t, conn); msg.Type != "pong" {
		t.Errorf("type = %q, want pong", msg.Type)
	}
}

// --- HandleUpgrade: Origin validation ---

// originDial exercises only the Handshake's Origin/CSRF gate, which still
// runs pre-upgrade (unchanged by the auth-moved-post-upgrade fix) — no
// token or auth message needed to observe an origin rejection.
func originDial(t *testing.T, origin string) error {
	t.Helper()
	hub := newRunningHub(t)
	hub.SetChannelAuthorizer(allowAllAuthorizer{})
	srv := httptest.NewServer(http.HandlerFunc(hub.HandleUpgrade))
	t.Cleanup(srv.Close)

	u, _ := url.Parse(srv.URL)
	conn, err := websocket.Dial(fmt.Sprintf("ws://%s/", u.Host), "", origin)
	if conn != nil {
		t.Cleanup(func() { _ = conn.Close() })
	}
	return err
}

func TestHandleUpgrade_OriginPolicy(t *testing.T) {
	t.Parallel()
	// Cross-site origin must be refused (server runs on 127.0.0.1).
	if err := originDial(t, "http://evil.example.com"); err == nil {
		t.Error("expected cross-origin dial to be rejected")
	}
	// localhost origin is allowed in non-production even though the host
	// header says 127.0.0.1.
	if err := originDial(t, "http://localhost:3000"); err != nil {
		t.Errorf("localhost origin should pass the dev bypass: %v", err)
	}
}
