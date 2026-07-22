package ws

// Tests for the hub's shared per-connection sweep (connSweepInterval,
// sweepRevokedSessions, sweepChannelAuthorization) — the #1255 item 3 /
// #1254 item 5 fix. The sweep functions are called directly (not via the
// ticker) so tests don't wait 30s; the ticker wiring itself is exercised
// implicitly by every other test that runs Run() without panicking.

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/auth/sessions"
)

// countingSessions wraps stubSessions' scriptable Get with a per-id call
// counter, so a test can assert "one query per distinct session" rather
// than "one query per connection".
type countingSessions struct {
	stubSessions
	mu    sync.Mutex
	calls map[string]int
}

func newCountingSessions(get func(ctx context.Context, id string) (*sessions.Session, error)) *countingSessions {
	return &countingSessions{stubSessions: stubSessions{get: get}, calls: map[string]int{}}
}

func (s *countingSessions) Get(ctx context.Context, id string) (*sessions.Session, error) {
	s.mu.Lock()
	s.calls[id]++
	s.mu.Unlock()
	return s.stubSessions.Get(ctx, id)
}

func (s *countingSessions) callCount(id string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls[id]
}

// newSweepClient registers a bare client (no real socket) with the given
// authSessionID, mirroring newClient but exposing the field the sweep
// groups by.
func newSweepClient(t *testing.T, hub *Hub, userID, authSessionID string) *Client {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	c := &Client{
		hub:           hub,
		userID:        userID,
		authSessionID: authSessionID,
		channels:      make(map[string]bool),
		send:          make(chan []byte, 64),
		ctx:           ctx,
		cancel:        cancel,
	}
	hub.register <- c
	waitFor(t, func() bool {
		hub.mu.RLock()
		defer hub.mu.RUnlock()
		return hub.clients[c]
	}, "client to register")
	t.Cleanup(cancel)
	return c
}

func activeSession(sid string) *sessions.Session {
	return &sessions.Session{ID: sid, ExpiresAt: time.Now().Add(time.Hour)}
}

func TestSweepRevokedSessions_DedupesQueriesAcrossSharedSession(t *testing.T) {
	t.Parallel()
	store := newCountingSessions(func(_ context.Context, id string) (*sessions.Session, error) {
		return activeSession(id), nil
	})
	hub := newRunningHub(t, withSessions(store))

	// Three tabs, same session — the old per-connection watcher would have
	// driven 3 independent Get calls per tick.
	newSweepClient(t, hub, "u1", "shared-sid")
	newSweepClient(t, hub, "u1", "shared-sid")
	newSweepClient(t, hub, "u1", "shared-sid")

	hub.sweepRevokedSessions(context.Background())

	if got := store.callCount("shared-sid"); got != 1 {
		t.Errorf("sessions.Get(shared-sid) called %d times for 3 connections sharing it, want exactly 1", got)
	}
}

func TestSweepRevokedSessions_ClosesAllClientsUnderRevokedSession(t *testing.T) {
	t.Parallel()
	store := newCountingSessions(func(_ context.Context, id string) (*sessions.Session, error) {
		if id == "revoked-sid" {
			return nil, sessions.ErrNotFound
		}
		return activeSession(id), nil
	})
	hub := newRunningHub(t, withSessions(store))

	c1 := newSweepClient(t, hub, "u1", "revoked-sid")
	c2 := newSweepClient(t, hub, "u1", "revoked-sid")
	other := newSweepClient(t, hub, "u2", "still-active-sid")

	hub.sweepRevokedSessions(context.Background())

	for i, c := range []*Client{c1, c2} {
		frame := recvOrTimeout(t, c.send)
		var msg ServerMessage
		if err := json.Unmarshal(frame, &msg); err != nil || msg.Type != "session_revoked" {
			t.Errorf("client %d: frame = %s, want session_revoked", i, frame)
		}
	}
	waitFor(t, func() bool { return c1.ctx.Err() != nil }, "c1 to be force-disconnected")
	waitFor(t, func() bool { return c2.ctx.Err() != nil }, "c2 to be force-disconnected")

	if other.ctx.Err() != nil {
		t.Error("client under an active session must not be disconnected")
	}
}

func TestSweepRevokedSessions_InactiveSessionCloses(t *testing.T) {
	t.Parallel()
	store := newCountingSessions(func(_ context.Context, id string) (*sessions.Session, error) {
		past := time.Now().Add(-time.Hour)
		return &sessions.Session{ID: id, ExpiresAt: past}, nil
	})
	hub := newRunningHub(t, withSessions(store))
	c := newSweepClient(t, hub, "u1", "expired-sid")

	hub.sweepRevokedSessions(context.Background())

	waitFor(t, func() bool { return c.ctx.Err() != nil }, "expired-session client to be disconnected")
}

func TestSweepRevokedSessions_TransientErrorDoesNotClose(t *testing.T) {
	t.Parallel()
	store := newCountingSessions(func(_ context.Context, _ string) (*sessions.Session, error) {
		return nil, errors.New("db timeout")
	})
	hub := newRunningHub(t, withSessions(store))
	c := newSweepClient(t, hub, "u1", "some-sid")

	hub.sweepRevokedSessions(context.Background())

	// A transient lookup failure must never disconnect — only an explicit
	// not-found/inactive result does.
	select {
	case <-c.ctx.Done():
		t.Error("transient sessions.Get error must not disconnect the client")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestSweepRevokedSessions_NoSessionsStoreIsNoop(t *testing.T) {
	t.Parallel()
	hub := newRunningHub(t) // default NopSessionsForTests, non-nil — force nil explicitly
	hub.sessions = nil
	newSweepClient(t, hub, "u1", "sid-1")

	// Must not panic.
	hub.sweepRevokedSessions(context.Background())
}

func TestSweepRevokedSessions_ClientsWithNoAuthSessionAreIgnored(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	store := newCountingSessions(func(_ context.Context, id string) (*sessions.Session, error) {
		calls.Add(1)
		return activeSession(id), nil
	})
	hub := newRunningHub(t, withSessions(store))
	newSweepClient(t, hub, "u1", "") // CLI-token-derived ticket: no sid

	hub.sweepRevokedSessions(context.Background())

	if got := calls.Load(); got != 0 {
		t.Errorf("sessions.Get called %d times for a client with no authSessionID, want 0", got)
	}
}

// --- sweepChannelAuthorization (#1254 item 5) ---

// flippableAuthorizer lets a test change the CanSubscribe verdict between
// the initial subscribe and the sweep, simulating a membership change.
type flippableAuthorizer struct {
	allow atomic.Bool
	calls atomic.Int32
}

func (a *flippableAuthorizer) CanSubscribe(_ context.Context, _, _ string) bool {
	a.calls.Add(1)
	return a.allow.Load()
}

func TestSweepChannelAuthorization_UnsubscribesWhenAccessRevoked(t *testing.T) {
	t.Parallel()
	auth := &flippableAuthorizer{}
	auth.allow.Store(true)
	hub := newRunningHub(t)
	hub.SetChannelAuthorizer(auth)

	c := newSweepClient(t, hub, "u1", "")
	c.subscribe("workspace:w1")
	hub.mu.RLock()
	_, subscribed := hub.channels["workspace:w1"][c]
	hub.mu.RUnlock()
	if !subscribed {
		t.Fatal("setup: subscribe must have succeeded while access was allowed")
	}

	// Simulate the user being removed from the workspace between subscribe
	// and the next sweep tick.
	auth.allow.Store(false)
	hub.sweepChannelAuthorization(context.Background())

	c.mu.Lock()
	_, stillTracked := c.channels["workspace:w1"]
	c.mu.Unlock()
	if stillTracked {
		t.Error("client-side channel set must drop the revoked channel")
	}
	hub.mu.RLock()
	_, stillInHubMap := hub.channels["workspace:w1"][c]
	hub.mu.RUnlock()
	if stillInHubMap {
		t.Error("hub-side subscriber set must drop the client for the revoked channel")
	}

	frame := recvOrTimeout(t, c.send)
	var msg ServerMessage
	if err := json.Unmarshal(frame, &msg); err != nil || msg.Type != "error" || msg.Channel != "workspace:w1" {
		t.Errorf("frame = %s, want an error frame for workspace:w1", frame)
	}
}

func TestSweepChannelAuthorization_NoOpWhenStillAuthorized(t *testing.T) {
	t.Parallel()
	hub := newRunningHub(t)
	hub.SetChannelAuthorizer(allowAllAuthorizer{})

	c := newSweepClient(t, hub, "u1", "")
	c.subscribe("workspace:w1")

	hub.sweepChannelAuthorization(context.Background())

	c.mu.Lock()
	_, stillSubscribed := c.channels["workspace:w1"]
	c.mu.Unlock()
	if !stillSubscribed {
		t.Error("a still-authorized subscription must survive the sweep")
	}
	expectNothing(t, c.send, 50*time.Millisecond)
}

func TestSweepChannelAuthorization_NoAuthorizerIsNoop(t *testing.T) {
	t.Parallel()
	hub := newRunningHub(t)
	// hub.channelAuth left nil (SetChannelAuthorizer never called).
	newSweepClient(t, hub, "u1", "")

	// Must not panic.
	hub.sweepChannelAuthorization(context.Background())
}

func TestSweepChannelAuthorization_UnsubscribedClientsAreNotChecked(t *testing.T) {
	t.Parallel()
	auth := &flippableAuthorizer{}
	auth.allow.Store(true)
	hub := newRunningHub(t)
	hub.SetChannelAuthorizer(auth)
	newSweepClient(t, hub, "u1", "") // never subscribes to anything

	hub.sweepChannelAuthorization(context.Background())

	if got := auth.calls.Load(); got != 0 {
		t.Errorf("CanSubscribe called %d times for a client with no subscriptions, want 0", got)
	}
}
