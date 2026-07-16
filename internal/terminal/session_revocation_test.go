package terminal

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
	"github.com/gorilla/websocket"
)

// These tests mirror the chat hub's session-revocation coverage
// (internal/ws) for the /ws/terminal shell surface. A force-logged-out /
// password-changed / admin-revoked user must not be able to open a NEW
// container shell within the 15-min ws ticket TTL, and an already-open
// shell must be torn down when the backing user_sessions row is revoked.

// authWithTicket sends the auth frame for the given sid (empty sid ==
// CLI-derived ticket) followed by the init frame.
func authWithTicket(t *testing.T, conn *websocket.Conn, v *auth.JWTValidator, sid string, init map[string]any) {
	t.Helper()
	tok, err := v.IssueWSTicket("u1", sid, "", "u1@x")
	if err != nil {
		t.Fatalf("issue ticket: %v", err)
	}
	authMsg, _ := json.Marshal(map[string]string{"type": "auth", "token": tok})
	if err := conn.WriteMessage(websocket.TextMessage, authMsg); err != nil {
		t.Fatalf("write auth: %v", err)
	}
	initMsg, _ := json.Marshal(init)
	if err := conn.WriteMessage(websocket.TextMessage, initMsg); err != nil {
		t.Fatalf("write init: %v", err)
	}
}

// activeSession inserts an active user_sessions row for u1 and returns its id.
func activeSession(t *testing.T, store *sessions.DBStore) string {
	t.Helper()
	sess, err := store.Create(context.Background(), "u1", "test-agent", "10.0.0.1", time.Hour)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	return sess.ID
}

// fakeSessionStore is a programmable sessions.Store for the transient-error
// case: getFunc controls what each Get returns. All writes are no-ops.
type fakeSessionStore struct {
	mu      sync.Mutex
	calls   int
	getFunc func(call int) (*sessions.Session, error)
}

func (f *fakeSessionStore) Get(_ context.Context, _ string) (*sessions.Session, error) {
	f.mu.Lock()
	f.calls++
	call := f.calls
	fn := f.getFunc
	f.mu.Unlock()
	return fn(call)
}
func (f *fakeSessionStore) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}
func (f *fakeSessionStore) Create(context.Context, string, string, string, time.Duration) (*sessions.Session, error) {
	return nil, errors.New("nop")
}
func (f *fakeSessionStore) ListActiveForUser(context.Context, string) ([]*sessions.Session, error) {
	return nil, nil
}
func (f *fakeSessionStore) Revoke(context.Context, string, string) error { return nil }
func (f *fakeSessionStore) RevokeAllForUser(context.Context, string, string) (int64, error) {
	return 0, nil
}
func (f *fakeSessionStore) TouchLastUsed(context.Context, string) error { return nil }
func (f *fakeSessionStore) RotateRefreshJti(context.Context, string, string, string) error {
	return nil
}
func (f *fakeSessionStore) SetClock(func() time.Time) {}

var _ sessions.Store = (*fakeSessionStore)(nil)

// (a) A connect attempt whose sid maps to a revoked row is rejected with
// session_revoked BEFORE any shell starts (the handler returns).
func TestServeHTTP_ConnectRejectedForRevokedSession(t *testing.T) {
	v := newTestValidator(t)
	db := seedTerminalDB(t)
	store := sessions.NewDBStore(db)

	sid := activeSession(t, store)
	if err := store.Revoke(context.Background(), sid, sessions.ReasonAdminForce); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	// Container has a pipe so that, if the check were skipped, a shell
	// WOULD start — the test then proves it does not by seeing the error
	// frame and the handler returning.
	serverSide, _ := net.Pipe()
	im := &covInteractive{covContainer: &covContainer{states: []string{"running"}}, conn: serverSide}
	h := New(im, v, db, silentLogger(), store)

	conn, done := dialTerminalDone(t, h)
	authWithTicket(t, conn, v, sid, map[string]any{"crew_id": "c1", "crew_slug": "crew-a"})

	got := readJSONFrame(t, conn)
	if got["type"] != "error" || got["message"] != "session_revoked" {
		t.Fatalf("expected session_revoked error, got %+v", got)
	}
	if im.config() != nil {
		t.Fatal("shell must not start for a revoked session")
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("handler did not return after rejecting revoked session")
	}
}

// (a') A sid with no matching row (session GC'd / never existed) is also
// rejected — ErrNotFound path.
func TestServeHTTP_ConnectRejectedForMissingSession(t *testing.T) {
	v := newTestValidator(t)
	db := seedTerminalDB(t)
	store := sessions.NewDBStore(db)

	h := New(&mockContainer{state: "running"}, v, db, silentLogger(), store)

	conn, done := dialTerminalDone(t, h)
	authWithTicket(t, conn, v, "s_does_not_exist", map[string]any{"crew_id": "c1", "crew_slug": "crew-a"})

	got := readJSONFrame(t, conn)
	if got["type"] != "error" || got["message"] != "session_revoked" {
		t.Fatalf("expected session_revoked error, got %+v", got)
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("handler did not return after rejecting missing session")
	}
}

// (b) The revoke-poll tears down an ESTABLISHED session when the row
// becomes revoked mid-session.
func TestServeHTTP_PollTearsDownRevokedSession(t *testing.T) {
	v := newTestValidator(t)
	db := seedTerminalDB(t)
	store := sessions.NewDBStore(db)
	sid := activeSession(t, store)

	serverSide, testSide := net.Pipe()
	im := &covInteractive{covContainer: &covContainer{states: []string{"running"}}, conn: serverSide}
	h := New(im, v, db, silentLogger(), store)
	h.revokePollInterval = 20 * time.Millisecond // fast poll for the test

	conn, done := dialTerminalDone(t, h)
	authWithTicket(t, conn, v, sid, map[string]any{"crew_id": "c1", "crew_slug": "crew-a"})

	// Prove the session established (connect-time check passed, bridge running).
	go func() {
		testSide.SetWriteDeadline(time.Now().Add(3 * time.Second))
		_, _ = testSide.Write([]byte("ready"))
	}()
	if got := readBinaryFrame(t, conn); string(got) != "ready" {
		t.Fatalf("expected established shell, got %q", got)
	}

	// Now revoke mid-session; the poll must tear the shell down.
	if err := store.Revoke(context.Background(), sid, sessions.ReasonLogout); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("live shell was not torn down after session revocation")
	}
}

// (c) A ticket with empty sid (CLI-derived) still connects — the check is
// skipped even when the store would reject every lookup.
func TestServeHTTP_EmptySidSkipsRevocationCheck(t *testing.T) {
	v := newTestValidator(t)
	db := seedTerminalDB(t)
	// A store whose Get always fails: proves the empty-sid path never
	// consults it.
	rejectStore := &fakeSessionStore{getFunc: func(int) (*sessions.Session, error) {
		return nil, sessions.ErrNotFound
	}}

	serverSide, testSide := net.Pipe()
	im := &covInteractive{covContainer: &covContainer{states: []string{"running"}}, conn: serverSide}
	h := New(im, v, db, silentLogger(), rejectStore)

	conn, _ := dialTerminalDone(t, h)
	authWithTicket(t, conn, v, "", map[string]any{"crew_id": "c1", "crew_slug": "crew-a"})

	go func() {
		testSide.SetWriteDeadline(time.Now().Add(3 * time.Second))
		_, _ = testSide.Write([]byte("cli-ok"))
	}()
	if got := readBinaryFrame(t, conn); string(got) != "cli-ok" {
		t.Fatalf("CLI ticket (empty sid) should connect, got %q", got)
	}
	if rejectStore.callCount() != 0 {
		t.Fatalf("empty-sid ticket must not consult the sessions store, got %d Get calls", rejectStore.callCount())
	}
}

// (d) A transient (non-ErrNotFound) DB error during the poll does NOT kill
// an established connection — a DB blip must not evict live shells.
func TestServeHTTP_TransientPollErrorKeepsConnection(t *testing.T) {
	v := newTestValidator(t)
	db := seedTerminalDB(t)

	now := time.Now()
	activeSess := &sessions.Session{
		ID:        "s_live",
		UserID:    "u1",
		CreatedAt: now,
		ExpiresAt: now.Add(time.Hour),
	}
	// First Get (connect-time) returns an active session; every later Get
	// (the poll) returns a transient error that must be tolerated.
	store := &fakeSessionStore{getFunc: func(call int) (*sessions.Session, error) {
		if call == 1 {
			return activeSess, nil
		}
		return nil, errors.New("db timeout under load")
	}}

	serverSide, testSide := net.Pipe()
	im := &covInteractive{covContainer: &covContainer{states: []string{"running"}}, conn: serverSide}
	h := New(im, v, db, silentLogger(), store)
	h.revokePollInterval = 20 * time.Millisecond

	conn, done := dialTerminalDone(t, h)
	authWithTicket(t, conn, v, "s_live", map[string]any{"crew_id": "c1", "crew_slug": "crew-a"})

	go func() {
		testSide.SetWriteDeadline(time.Now().Add(3 * time.Second))
		_, _ = testSide.Write([]byte("live"))
	}()
	if got := readBinaryFrame(t, conn); string(got) != "live" {
		t.Fatalf("expected established shell, got %q", got)
	}

	// Span many poll ticks; the connection must stay up despite the
	// repeated transient errors.
	select {
	case <-done:
		t.Fatal("transient poll error must NOT tear down a live shell")
	case <-time.After(300 * time.Millisecond):
	}
	if store.callCount() < 2 {
		t.Fatalf("expected the poll to have run at least once, got %d Get calls", store.callCount())
	}

	// Clean teardown: closing the client ends the session.
	_ = conn.Close()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("handler did not return after client close")
	}
}
