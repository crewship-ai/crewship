package ws

import "testing"

// IsUserSubscribed powers the "don't notify a user who is watching the
// session live" check in the chat reply notifier.

func TestIsUserSubscribed(t *testing.T) {
	hub := newRunningHub(t)
	hub.SetChannelAuthorizer(allowAllAuthorizer{})
	c1 := newClient(t, hub, "u1")
	c1.subscribe("session:s1")

	if !hub.IsUserSubscribed("session:s1", "u1") {
		t.Error("u1 subscribed to session:s1, want true")
	}
	if hub.IsUserSubscribed("session:s1", "u2") {
		t.Error("u2 never subscribed, want false")
	}
	if hub.IsUserSubscribed("session:other", "u1") {
		t.Error("u1 not subscribed to session:other, want false")
	}

	c1.unsubscribe("session:s1")
	if hub.IsUserSubscribed("session:s1", "u1") {
		t.Error("u1 unsubscribed, want false")
	}
}

func TestIsUserSubscribed_NilHubSafe(t *testing.T) {
	var hub *Hub
	if hub.IsUserSubscribed("session:s1", "u1") {
		t.Error("nil hub must report false, not panic")
	}
}
