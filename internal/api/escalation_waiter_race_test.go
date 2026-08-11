package api

import (
	"testing"
	"time"
)

// The waiter map used to hold one channel per escalation and overwrite it, so
// a second request for the same id silently stole the first one's wakeup: the
// incumbent blocked until its context expired and reported TIMEOUT for an
// escalation a human had already answered.
//
// Scoping the wait endpoint to the caller's token binding made that reachable
// from outside: a cross-tenant probe registers a channel, is refused 404, and
// its deferred cleanup takes the legitimate waiter's channel with it. The same
// happens with no attacker at all, from a sidecar retrying its own long poll.
//
// These exercise the registry directly. The HTTP path is covered by
// escalation_waiter_authz_test.go; what is at stake here is the bookkeeping
// underneath it, and it is invisible from the outside until someone waits
// forever.

func TestEscalationWaiters_ASecondRegistrationDoesNotStealTheFirstWakeup(t *testing.T) {
	t.Parallel()
	h := &QueryHandler{escalationWaiters: make(map[string][]chan escalationResult)}

	first := h.registerEscalationWaiter("esc-1")
	second := h.registerEscalationWaiter("esc-1")

	h.notifyEscalationWaiter("esc-1", escalationResult{Resolution: "approved"})

	for name, ch := range map[string]chan escalationResult{"first": first, "second": second} {
		select {
		case got := <-ch:
			if got.Resolution != "approved" {
				t.Errorf("%s waiter got %q, want approved", name, got.Resolution)
			}
		case <-time.After(time.Second):
			t.Errorf("%s waiter never received the result — its registration was evicted", name)
		}
	}
}

// A refused caller tearing down its own registration must not cancel anyone
// else's. This is the exact shape the authorization predicate introduced.
func TestEscalationWaiters_ARefusedCallerDoesNotCancelTheRealWaiter(t *testing.T) {
	t.Parallel()
	h := &QueryHandler{escalationWaiters: make(map[string][]chan escalationResult)}

	legit := h.registerEscalationWaiter("esc-1")

	// The refused caller: registers, then unwinds via the same deferred
	// removal the handler uses.
	refused := h.registerEscalationWaiter("esc-1")
	h.removeEscalationWaiter("esc-1", refused)

	h.notifyEscalationWaiter("esc-1", escalationResult{Resolution: "the answer"})

	select {
	case got := <-legit:
		if got.Resolution != "the answer" {
			t.Errorf("resolution = %q, want %q", got.Resolution, "the answer")
		}
	case <-time.After(time.Second):
		t.Fatal("the legitimate waiter never woke — a refused caller's cleanup removed its channel, so the agent would time out on an escalation a human had already approved")
	}

	// The refused caller must get nothing.
	select {
	case got := <-refused:
		t.Errorf("a removed waiter still received %+v", got)
	default:
	}
}

// Removing the last waiter must not leave an empty slice behind for every
// escalation the process ever served.
func TestEscalationWaiters_TheMapDrains(t *testing.T) {
	t.Parallel()
	h := &QueryHandler{escalationWaiters: make(map[string][]chan escalationResult)}

	a := h.registerEscalationWaiter("esc-1")
	b := h.registerEscalationWaiter("esc-1")
	h.removeEscalationWaiter("esc-1", a)

	h.escalationMu.Lock()
	n := len(h.escalationWaiters["esc-1"])
	h.escalationMu.Unlock()
	if n != 1 {
		t.Errorf("after removing one of two waiters, %d remain, want 1", n)
	}

	h.removeEscalationWaiter("esc-1", b)
	h.escalationMu.Lock()
	_, present := h.escalationWaiters["esc-1"]
	h.escalationMu.Unlock()
	if present {
		t.Error("the map still holds an entry after the last waiter left")
	}
}
