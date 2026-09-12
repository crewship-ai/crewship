package dispatch

import (
	"context"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/work"
)

type contextObservedRuntime struct {
	*fakeRuntime
	returned  chan struct{}
	blockStop bool
}

func (r *contextObservedRuntime) Run(ctx context.Context, a Assignment, started func()) error {
	defer close(r.returned)
	return r.fakeRuntime.Run(ctx, a, started)
}

func (r *contextObservedRuntime) Stop(ctx context.Context, locator string) (bool, error) {
	if r.blockStop {
		<-ctx.Done()
		return false, ctx.Err()
	}
	return r.fakeRuntime.Stop(ctx, locator)
}

// Stopping an OS runtime and stopping the local supervisor are separate duties.
// A refused stop must park the work AND cancel local monitoring; relying on a
// future heartbeat to discover the ended attempt leaks it when shutdown closes
// the database before that heartbeat.
func TestShutdown_CancelsLocalSupervisionWhenRuntimeCannotBeStopped(t *testing.T) {
	testShutdownUnconfirmedStop(t, false)
}

func TestShutdown_StopTimeoutStillRecordsReconciliation(t *testing.T) {
	testShutdownUnconfirmedStop(t, true)
}

func testShutdownUnconfirmedStop(t *testing.T, blockStop bool) {
	t.Helper()
	h := newHarness(t)
	h.rt.crashAfterStart = true // Run only returns after its context is cancelled.
	h.rt.refuseStop = true
	h.cfg.HeartbeatInterval = time.Hour // No incidental heartbeat may clean up.
	runtime := &contextObservedRuntime{fakeRuntime: h.rt, returned: make(chan struct{}), blockStop: blockStop}
	d := New(h.store, runtime, nil, h.cfg, quiet())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = d.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		<-done
		// Keep the regression's failing version from leaking its own worker.
		d.mu.Lock()
		for _, live := range d.running {
			live.cancel()
		}
		d.mu.Unlock()
		select {
		case <-runtime.returned:
		case <-time.After(5 * time.Second):
			t.Error("runtime did not return during test cleanup")
		}
	})

	r := h.accept("shutdown-context")
	h.waitForState(r.WorkID, work.StateRunning)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("dispatcher shutdown did not return")
	}
	item, err := h.store.Get(context.Background(), r.WorkID)
	if err != nil {
		t.Fatal(err)
	}
	if item.State != work.StateNeedsReconciliation {
		t.Fatalf("shutdown left work %q after an unconfirmed stop, want needs_reconciliation", item.State)
	}
	select {
	case <-runtime.returned:
	case <-time.After(time.Second):
		t.Fatal("shutdown left the runtime context alive after parking the work")
	}
}

// A service restart is not a user cancellation. A stopped turn can already
// have external effects, so an unclassified interruption must remain visible
// for resolution, without a racing supervisor overwriting that outcome.
func TestShutdown_ConfirmedStopPreservesAcceptedWork(t *testing.T) {
	h := newHarness(t)
	h.rt.block = make(chan struct{})
	r := h.accept("shutdown-confirmed")
	d, stop := h.runDispatcher(nil)
	h.waitForState(r.WorkID, work.StateRunning)
	stop()
	item, err := h.store.Get(context.Background(), r.WorkID)
	if err != nil {
		t.Fatal(err)
	}
	if item.State != work.StateNeedsReconciliation {
		t.Fatalf("service shutdown lost accepted work: got %s, want needs_reconciliation", item.State)
	}
	d.mu.Lock()
	remaining := len(d.running)
	d.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("shutdown returned before %d supervisors finished", remaining)
	}
	var cancelled int
	if err := h.db.QueryRow("SELECT COUNT(*) FROM work_events WHERE work_id=? AND to_state='cancelled'", r.WorkID).Scan(&cancelled); err != nil {
		t.Fatal(err)
	}
	if cancelled != 0 {
		t.Fatalf("shutdown emitted %d user-cancellation transitions", cancelled)
	}
}
