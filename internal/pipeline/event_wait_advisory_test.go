package pipeline

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// Observe durable checks rather than sleeping to guess when the blocking
// waiter has armed, registered, or processed an advisory wake.
type advisoryObservedWaitStore struct {
	SignalWaitStore
	checks chan string
}

func (s *advisoryObservedWaitStore) Resolve(ctx context.Context, runID, stepID string) (string, error) {
	status, err := s.SignalWaitStore.Resolve(ctx, runID, stepID)
	if err == nil {
		s.checks <- status
	}
	return status, err
}

func TestEventWaitDeadline_AdvisorySiblingWake(t *testing.T) {
	for _, outcome := range []string{"own_delivery", "original_deadline", "cancellation"} {
		t.Run(outcome, func(t *testing.T) {
			db := openSignalWaitTestDB(t)
			store := NewSQLSignalWaitStore(db)
			observed := &advisoryObservedWaitStore{SignalWaitStore: store, checks: make(chan string, 16)}
			registry := NewSignalRegistry()
			exec := &Executor{signals: registry, signalWaits: observed}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if _, err := store.ArmWithTimeout(ctx, "ws", "run", "a", "event", time.Hour); err != nil {
				t.Fatal(err)
			}
			// Explicit ordering makes Deliver select sibling A without a sleep.
			if _, err := db.ExecContext(ctx, `UPDATE pipeline_signal_waits SET created_at='2000-01-01T00:00:00.000000000Z' WHERE step_id='a'`); err != nil {
				t.Fatal(err)
			}
			timeout := time.Hour
			if outcome == "original_deadline" {
				timeout = time.Second
			}
			deadline, err := store.ArmWithTimeout(ctx, "ws", "run", "b", "event", timeout)
			if err != nil {
				t.Fatal(err)
			}
			type result struct {
				output string
				err    error
			}
			done := make(chan result, 1)
			joined := make(chan struct{})
			go func() {
				defer close(joined)
				output, _, _, err := exec.runWaitStep(ctx,
					Step{ID: "b", Type: StepWait, TimeoutSec: 3600, Wait: &WaitStep{Kind: "event", EventType: "event"}},
					emptyRender(), RunInput{WorkspaceID: "ws"}, "run", 1)
				done <- result{output, err}
			}()
			t.Cleanup(func() {
				cancel()
				select {
				case <-joined:
				case <-time.After(2 * time.Second):
					t.Error("blocking event waiter did not join after cancellation")
				}
			})
			awaitCheck := func() {
				t.Helper()
				select {
				case status := <-observed.checks:
					if status != "pending" {
						t.Fatalf("expected pending own row, got %q", status)
					}
				case <-time.After(2 * time.Second):
					t.Fatal("waiter did not check its durable row")
				}
			}
			awaitCheck() // after arm
			awaitCheck() // after in-memory registration
			if ok, err := store.Deliver(ctx, "run", "event", "sibling payload"); err != nil || !ok {
				t.Fatalf("deliver sibling: %v %v", ok, err)
			}
			if !registry.Signal("run", "event", "sibling payload") {
				t.Fatal("blocking waiter not registered")
			}
			awaitCheck() // advisory wake observed, own B row is still pending
			select {
			case got := <-done:
				t.Fatalf("pending B finished on sibling A wake before original deadline: output=%q err=%v", got.output, got.err)
			case <-time.After(50 * time.Millisecond):
			}
			if status, err := store.Status(ctx, "run", "a"); err != nil || status != "delivered" {
				t.Fatalf("sibling delivery stolen: status=%q err=%v", status, err)
			}
			switch outcome {
			case "own_delivery":
				if ok, err := store.Deliver(ctx, "run", "event", "own payload"); err != nil || !ok {
					t.Fatalf("deliver own row: %v %v", ok, err)
				}
				registry.Signal("run", "event", "own payload")
			case "cancellation":
				cancel()
			}
			limit := 2 * time.Second
			if outcome == "original_deadline" {
				// The executed step asks for an hour, but the already-armed row
				// must keep its original one-second deadline after the wake.
				limit = time.Until(deadline) + 500*time.Millisecond
			}
			select {
			case got := <-done:
				switch outcome {
				case "own_delivery":
					if got.err != nil || got.output != "own payload" {
						t.Fatalf("own delivery: output=%q err=%v", got.output, got.err)
					}
					if status, err := store.Status(ctx, "run", "b"); err != nil || status != "consumed" {
						t.Fatalf("own row not consumed: status=%q err=%v", status, err)
					}
				case "original_deadline":
					if got.err == nil || !strings.Contains(got.err.Error(), "timed out") || time.Now().Before(deadline) {
						t.Fatalf("original deadline: output=%q err=%v deadline=%s", got.output, got.err, deadline)
					}
				case "cancellation":
					if !errors.Is(got.err, context.Canceled) {
						t.Fatalf("cancellation: %v", got.err)
					}
				}
			case <-time.After(limit):
				t.Fatalf("waiter did not finish for %s; original deadline=%s", outcome, deadline)
			}
		})
	}
}
