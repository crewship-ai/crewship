package ws

import (
	"context"
	"testing"
	"time"
)

// Finding 1 (#1822 review). sweepChannelAuthorization iterated h.clients only,
// so an HTTP run-stream observer was authorized exactly once — at request time
// — and then never again. A member removed from the workspace kept receiving
// text/thinking/tool_call frames for as long as they held the connection,
// while a WebSocket subscriber on the SAME channel was unsubscribed on the
// next tick. The sweep is the enforcement point for membership changes; an
// observer that it cannot see is an observer outside the fence.
func TestSweepChannelAuthorizationRevokesObservers(t *testing.T) {
	hub := newRunningHub(t)
	hub.SetChannelAuthorizer(denyAllAuthorizer{})

	obs := hub.AddObserver("session:c1", "u1", 8)

	hub.sweepChannelAuthorization(context.Background())

	if !obs.Revoked() {
		t.Error("observer was not marked revoked by the authorization sweep")
	}
	select {
	case _, open := <-obs.Frames():
		if open {
			t.Error("frame channel still open after revocation")
		}
	case <-time.After(time.Second):
		t.Error("frame channel was not closed by the revocation sweep")
	}
	if n := hub.ObserverCount("session:c1"); n != 0 {
		t.Errorf("ObserverCount = %d after revocation, want 0 — a revoked observer must stop costing dispatch work", n)
	}
}

// A revoked observer must stop RECEIVING, not merely be flagged. This is the
// property the finding is actually about.
func TestRevokedObserverReceivesNoFurtherFrames(t *testing.T) {
	hub := newRunningHub(t)
	hub.SetChannelAuthorizer(denyAllAuthorizer{})
	obs := hub.AddObserver("session:c1", "u1", 8)

	hub.sweepChannelAuthorization(context.Background())
	// Drain the close signal so a stale frame would be visible as an open read.
	// Bounded: if the sweep never closed the channel this must fail, not hang.
	select {
	case <-obs.Frames():
	case <-time.After(time.Second):
		t.Fatal("revocation never closed the frame channel")
	}

	hub.Broadcast("session:c1", ServerMessage{Type: "chat_event", Channel: "session:c1", Payload: ChatEvent{Type: "text", Content: "after revocation"}})

	select {
	case data, open := <-obs.Frames():
		if open {
			t.Fatalf("revoked observer still received a frame: %s", data)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("expected a closed channel, not a blocked read")
	}
}

// Still-authorized observers must survive the sweep untouched — the fix must
// not become a periodic disconnect for everyone.
func TestSweepLeavesAuthorizedObserversAttached(t *testing.T) {
	hub := newRunningHub(t)
	hub.SetChannelAuthorizer(allowAllAuthorizer{})
	obs := hub.AddObserver("session:c1", "u1", 8)

	hub.sweepChannelAuthorization(context.Background())

	if obs.Revoked() {
		t.Fatal("an authorized observer was revoked by the sweep")
	}
	if n := hub.ObserverCount("session:c1"); n != 1 {
		t.Fatalf("ObserverCount = %d, want the authorized observer still attached", n)
	}
	hub.Broadcast("session:c1", ServerMessage{Type: "chat_event", Channel: "session:c1", Payload: ChatEvent{Type: "text", Content: "still here"}})
	if got := receiveFrame(t, obs); got.Type != "chat_event" {
		t.Errorf("frame type = %q, want chat_event to still flow", got.Type)
	}
}

// A FAILED check (DB hiccup) is not a deny. The client sweep already draws
// this distinction — revoking every subscription on one transient blip would
// mass-disconnect users who are still perfectly authorized — and the observer
// sweep must draw it the same way.
func TestSweepDoesNotRevokeObserversOnAuthorizerError(t *testing.T) {
	hub := newRunningHub(t)
	hub.SetChannelAuthorizer(&erroringAuthorizer{})
	obs := hub.AddObserver("session:c1", "u1", 8)

	hub.sweepChannelAuthorization(context.Background())

	if obs.Revoked() {
		t.Fatal("a transient authorizer error revoked the observer; only a definitive deny may")
	}
	if n := hub.ObserverCount("session:c1"); n != 1 {
		t.Fatalf("ObserverCount = %d, want 1 — the observer must survive a failed check", n)
	}
}

// Finding 2 (#1822 review). chatnotify treats IsUserSubscribed as "watching
// live, no bell needed" and skips inbox.UpsertMessage entirely — it drops the
// DURABLE record, not just an external push, and nothing backfills it. A
// wedged HTTP reader that never delivered a byte would therefore destroy the
// user's only fallback copy of the reply. A socket is a browser session
// actively rendering the transcript; an HTTP stream may be a script piping to
// a file, or blocked in a write. Losing the record is strictly worse than a
// redundant bell, so observers deliberately do NOT count as presence.
func TestObserverDoesNotCountAsPresence(t *testing.T) {
	hub := newRunningHub(t)
	obs := hub.AddObserver("session:c1", "u1", 8)
	defer hub.RemoveObserver("session:c1", obs)

	if hub.IsUserSubscribed("session:c1", "u1") {
		t.Fatal("an HTTP stream reader counted as presence; chatnotify would skip inbox.UpsertMessage " +
			"and the user would lose the durable record of the reply")
	}
}

// The presence signal must still work for real sockets — the fix above must
// not disable it wholesale.
func TestSocketStillCountsAsPresence(t *testing.T) {
	hub := newRunningHub(t)
	hub.SetChannelAuthorizer(allowAllAuthorizer{})
	c := newClient(t, hub, "u1")
	c.subscribe("session:c1")

	if !hub.IsUserSubscribed("session:c1", "u1") {
		t.Fatal("a subscribed socket no longer counts as presence")
	}
}
