package ws

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/auth/sessions"
	"golang.org/x/net/websocket"
	"strings"
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

func dialWithSid(t *testing.T, store sessions.Store, sid string) (*websocket.Conn, *http.Response, error) {
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

	// Plain GET first to read status code/body (websocket.Dial hides them).
	resp, err := http.Get(srv.URL + "/?token=" + url.QueryEscape(tok))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })

	u, _ := url.Parse(srv.URL)
	wsURL := fmt.Sprintf("ws://%s/?token=%s", u.Host, url.QueryEscape(tok))
	conn, dialErr := websocket.Dial(wsURL, "", srv.URL)
	if conn != nil {
		t.Cleanup(func() { _ = conn.Close() })
	}
	return conn, resp, dialErr
}

func TestHandleUpgrade_SessionMissingIsRevoked(t *testing.T) {
	t.Parallel()
	store := &stubSessions{get: func(context.Context, string) (*sessions.Session, error) {
		return nil, sessions.ErrNotFound
	}}
	_, resp, dialErr := dialWithSid(t, store, "sess-gone")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if got := string(body); !strings.Contains(got, "session_revoked") {
		t.Errorf("body = %q, want session_revoked", got)
	}
	if dialErr == nil {
		t.Error("websocket dial must fail for a revoked session")
	}
}

func TestHandleUpgrade_SessionLookupErrorIs500(t *testing.T) {
	t.Parallel()
	store := &stubSessions{get: func(context.Context, string) (*sessions.Session, error) {
		return nil, errors.New("db timeout")
	}}
	_, resp, dialErr := dialWithSid(t, store, "sess-1")
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", resp.StatusCode)
	}
	if dialErr == nil {
		t.Error("websocket dial must fail on session lookup error")
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
	_, resp, dialErr := dialWithSid(t, store, "sess-1")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
	if dialErr == nil {
		t.Error("websocket dial must fail for an inactive session")
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
	conn, _, dialErr := dialWithSid(t, store, "sess-1")
	if dialErr != nil {
		t.Fatalf("dial: %v", dialErr)
	}
	// The connection is live end-to-end: ping → pong.
	wsSend(t, conn, ClientMessage{Type: "ping"})
	if msg := wsRecv(t, conn); msg.Type != "pong" {
		t.Errorf("type = %q, want pong", msg.Type)
	}
}

// --- HandleUpgrade: Origin validation ---

func originDial(t *testing.T, origin string) error {
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
	conn, err := websocket.Dial(wsURL, "", origin)
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
