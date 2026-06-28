package pipeline

import "testing"

func TestSignalRegistry_DeliverAndMiss(t *testing.T) {
	r := NewSignalRegistry()

	// No waiter yet → Signal misses.
	if r.Signal("run1", "go", "x") {
		t.Fatal("signal with no waiter should miss")
	}

	ch, cancel := r.Register("run1", "go")
	defer cancel()

	// Matching signal delivers the payload.
	if !r.Signal("run1", "go", "payload-1") {
		t.Fatal("signal with a waiter should deliver")
	}
	if got := <-ch; got != "payload-1" {
		t.Fatalf("payload: got %q", got)
	}

	// Wrong event_type misses.
	if r.Signal("run1", "other", "y") {
		t.Fatal("wrong event_type should miss")
	}

	// After cancel, the waiter is gone.
	cancel()
	if r.Signal("run1", "go", "z") {
		t.Fatal("signal after cancel should miss")
	}
}

func TestSignalRegistry_NilSafe(t *testing.T) {
	var r *SignalRegistry
	if r.Signal("a", "b", "c") {
		t.Fatal("nil registry Signal should be false")
	}
	ch, cancel := r.Register("a", "b")
	if ch != nil {
		t.Fatal("nil registry Register should return nil channel")
	}
	cancel() // must not panic
}
