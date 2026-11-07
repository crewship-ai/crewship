package dispatch

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
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
	var runStatus string
	if err := h.db.QueryRowContext(t.Context(), `SELECT run_status FROM work_attempts WHERE work_id = ? ORDER BY attempt DESC LIMIT 1`, r.WorkID).Scan(&runStatus); err != nil {
		t.Fatal(err)
	}
	if runStatus != "" {
		t.Fatalf("unconfirmed shutdown projected terminal %q", runStatus)
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

// A cancel that arrives after the run completed did not cancel anything. The
// runtime reports success; recording `cancelled` over it would hide effects
// that happened. The cancel is noted, the state is the truth.
func TestSettle_CompletionThatBeatsACancelIsRecordedAsSucceeded(t *testing.T) {
	h := newHarness(t)
	h.rt.block = make(chan struct{})
	h.cfg.CancelPollInterval = time.Hour // the cancel is never delivered to the runtime
	_, stop := h.runDispatcher(nil)
	defer stop()
	r := h.accept("cancel-after-completion")
	h.waitForState(r.WorkID, work.StateRunning)
	if _, err := h.store.RequestCancel(context.Background(), r.WorkID, "operator", "too late"); err != nil {
		t.Fatal(err)
	}
	close(h.rt.block) // the run finishes on its own, successfully
	it := h.waitForState(r.WorkID, work.StateSucceeded)
	if it.State != work.StateSucceeded {
		t.Fatalf("state = %s, want succeeded", it.State)
	}
	var cancelled int
	if err := h.db.QueryRow("SELECT COUNT(*) FROM work_events WHERE work_id=? AND to_state='cancelled'", r.WorkID).Scan(&cancelled); err != nil {
		t.Fatal(err)
	}
	if cancelled != 0 {
		t.Fatalf("%d cancelled transitions recorded over a completed run", cancelled)
	}
	var runStatus string
	if err := h.db.QueryRowContext(t.Context(), `SELECT run_status FROM work_attempts WHERE work_id = ? ORDER BY attempt DESC LIMIT 1`, r.WorkID).Scan(&runStatus); err != nil {
		t.Fatal(err)
	}
	if runStatus != "COMPLETED" {
		t.Fatalf("late cancel changed run projection to %q", runStatus)
	}

}

func TestSettle_ResultStorageFailureCannotInventFailureOrCancellation(t *testing.T) {
	for _, cancelRequested := range []bool{false, true} {
		t.Run(map[bool]string{false: "no cancel", true: "late cancel"}[cancelRequested], func(t *testing.T) {
			h := newHarness(t)
			r := h.accept("result-storage-uncertain")
			a, err := h.store.Claim(t.Context(), work.ClaimOptions{LeaseOwner: "owner"})
			if err != nil {
				t.Fatal(err)
			}
			// Model a successful capture whose acknowledgement was lost. The runtime
			// returned successfully before storage failed; neither FAILED nor CANCELLED
			// may be inferred from that storage error, even with a late stop request.
			zero := 0
			if err := h.store.StageRunResult(t.Context(), r.WorkID, a.RunID, a.Generation, work.RunResult{ExitCode: &zero}); err != nil {
				t.Fatal(err)
			}
			if cancelRequested {
				if _, err := h.store.RequestCancel(t.Context(), r.WorkID, "operator", "late stop"); err != nil {
					t.Fatal(err)
				}
			}
			d := New(h.store, h.rt, nil, h.cfg, quiet())
			d.settle(t.Context(), &liveAttempt{assignment: Assignment{Item: a.Item, RunID: a.RunID, Generation: a.Generation}}, work.ErrRunResultUnstored)
			it, err := h.store.Get(t.Context(), r.WorkID)
			if err != nil || it.State != work.StateNeedsReconciliation {
				t.Fatalf("state=%v err=%v", it, err)
			}
			p, _, err := h.store.RunProjection(t.Context(), a.RunID)
			if err != nil || p.Ready || p.Status != "" {
				t.Fatalf("storage failure invented execution outcome: %+v %v", p, err)
			}
		})
	}
}

func TestShutdown_ResultBeforeLateCancellation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		runErr error
		state  work.State
		status string
	}{
		{"unconfirmed capture", work.ErrRunResultUnstored, work.StateNeedsReconciliation, ""},
		{"completed execution", nil, work.StateSucceeded, "COMPLETED"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			r := h.accept("shutdown-late-cancel")
			a, err := h.store.Claim(t.Context(), work.ClaimOptions{LeaseOwner: "owner"})
			if err != nil {
				t.Fatal(err)
			}
			assignment := Assignment{Item: a.Item, RunID: a.RunID, Generation: a.Generation}
			if err := h.store.Transition(t.Context(), work.TransitionRequest{WorkID: r.WorkID, RunID: a.RunID, Generation: a.Generation, To: work.StateRunning}); err != nil {
				t.Fatal(err)
			}
			if err := h.rt.Run(t.Context(), assignment, func() {}); err != nil {
				t.Fatal(err)
			}
			zero := 0
			if err := h.store.StageRunResult(t.Context(), r.WorkID, a.RunID, a.Generation, work.RunResult{ExitCode: &zero}); err != nil {
				t.Fatal(err)
			}
			if _, err := h.store.RequestCancel(t.Context(), r.WorkID, "operator", "late stop"); err != nil {
				t.Fatal(err)
			}
			runDone := make(chan error, 1)
			runDone <- tc.runErr
			close(runDone)
			d := New(h.store, h.rt, nil, h.cfg, quiet())
			d.shutdownAttempt(&liveAttempt{assignment: assignment, locator: h.rt.Locator(assignment), cancel: func() {}}, runDone)
			it, err := h.store.Get(t.Context(), r.WorkID)
			if err != nil || it.State != tc.state {
				t.Fatalf("shutdown state=%v err=%v", it, err)
			}
			p, _, err := h.store.RunProjection(t.Context(), a.RunID)
			if err != nil || p.Status != tc.status {
				t.Fatalf("shutdown invented outcome: %+v %v", p, err)
			}
			if h.rt.starts.Load() != 1 {
				t.Fatal("shutdown repeated execution")
			}
		})
	}
}

// A provider outage must not keep settlement (and shutdown drain) blocked
// indefinitely after execution has returned.
type stalledOutcomeProbe struct{ *fakeRuntime }

func (r stalledOutcomeProbe) Alive(ctx context.Context, _ string) (bool, error) {
	<-ctx.Done()
	return false, ctx.Err()
}
func TestSettle_OutcomeProbeTimeoutStillPersistsReconciliation(t *testing.T) {
	h := newHarness(t)
	r := h.accept("stalled-outcome-probe")
	a, err := h.store.Claim(t.Context(), work.ClaimOptions{LeaseOwner: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	h.cfg.StopGrace = 200 * time.Millisecond
	d := New(h.store, stalledOutcomeProbe{h.rt}, nil, h.cfg, quiet())
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	d.settle(ctx, &liveAttempt{assignment: Assignment{Item: a.Item, RunID: a.RunID, Generation: a.Generation}}, errors.New("execution failed"))
	if ctx.Err() != nil {
		t.Fatal("provider probe consumed the parent settlement deadline")
	}
	it, err := h.store.Get(t.Context(), r.WorkID)
	if err != nil || it.State != work.StateNeedsReconciliation {
		t.Fatalf("state=%v err=%v", it, err)
	}
	p, _, err := h.store.RunProjection(t.Context(), a.RunID)
	if err != nil || p.Status != "" {
		t.Fatalf("timeout fabricated terminal: %+v %v", p, err)
	}
}

// hostStopRuntime answers like a protected host-captured run (#5081): the stop
// request ends the process tree, yet Stop itself reports an error because a
// leader exit does not prove descendant absence. Only once the capture records
// its terminal result and stop receipt does Alive report the runtime gone.
type hostStopRuntime struct {
	*fakeRuntime
	stopCalls  atomic.Int32
	terminalAt atomic.Int64 // unix nanos; zero = never
	terminal   time.Duration
}

func (r *hostStopRuntime) Stop(context.Context, string) (bool, error) {
	if r.stopCalls.Add(1) == 1 && r.terminal > 0 {
		r.terminalAt.Store(time.Now().Add(r.terminal).UnixNano())
	}
	return false, errors.New("leader exit does not prove descendant absence")
}

func (r *hostStopRuntime) Alive(context.Context, string) (bool, error) {
	at := r.terminalAt.Load()
	return at == 0 || time.Now().UnixNano() < at, nil
}

// A restart under a protected host-captured run must wait, within the stop
// grace, for the provider to confirm the stop it requested, then settle the
// run's own return. Parking at once with "runtime stop could not be confirmed"
// left a reason retained recovery refused and a slot held for good (#5081).
// Without confirmation the old park stays, and a return seen before the stop
// is confirmed (a detached hand-back) never settles as ended.
func TestShutdown_HostStopWaitsForConfirmedEnd(t *testing.T) {
	for _, tc := range []struct {
		name     string
		terminal time.Duration
		runErr   error
		classify Outcome
		returnAt time.Duration // after the stop request; negative = already returned
		state    work.State
		reason   string
	}{
		{"terminal capture settles through retained recovery", 300 * time.Millisecond, errors.New("capture unconfirmed"), OutcomeRetained, 350 * time.Millisecond, work.StateNeedsReconciliation, RetainedCapturePendingReason},
		{"a runtime-classified failure settles as failed", 300 * time.Millisecond, errors.New("exit 143"), OutcomeFailed, 350 * time.Millisecond, work.StateFailed, ""},
		{"no confirmation keeps the shutdown park", 0, errors.New("capture unconfirmed"), OutcomeRetained, 100 * time.Millisecond, work.StateNeedsReconciliation, "server shutdown: runtime stop could not be confirmed"},
		{"detached return before confirmation keeps the shutdown park", 0, errors.New("detached"), OutcomeDetached, -1, work.StateNeedsReconciliation, "server shutdown: runtime stop could not be confirmed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.rt.classifyAs = tc.classify
			r := h.accept("shutdown-host-stop")
			a, err := h.store.Claim(t.Context(), work.ClaimOptions{LeaseOwner: "owner"})
			if err != nil {
				t.Fatal(err)
			}
			assignment := Assignment{Item: a.Item, RunID: a.RunID, Generation: a.Generation}
			if err := h.store.Transition(t.Context(), work.TransitionRequest{WorkID: r.WorkID, RunID: a.RunID, Generation: a.Generation, To: work.StateRunning}); err != nil {
				t.Fatal(err)
			}
			if err := h.rt.Run(t.Context(), assignment, func() {}); err != nil {
				t.Fatal(err)
			}
			runtime := &hostStopRuntime{fakeRuntime: h.rt, terminal: tc.terminal}
			runDone := make(chan error, 1)
			if tc.returnAt < 0 {
				runDone <- tc.runErr
			} else {
				go func() {
					for runtime.stopCalls.Load() == 0 {
						time.Sleep(5 * time.Millisecond)
					}
					time.Sleep(tc.returnAt)
					runDone <- tc.runErr
				}()
			}
			var cancelled atomic.Bool
			d := New(h.store, runtime, nil, h.cfg, quiet())
			began := time.Now()
			d.shutdownAttempt(&liveAttempt{assignment: assignment, locator: h.rt.Locator(assignment), cancel: func() { cancelled.Store(true) }}, runDone)
			if elapsed := time.Since(began); elapsed > 3*h.cfg.StopGrace {
				t.Fatalf("shutdown settlement took %s, beyond its bounded budget", elapsed)
			}
			if !cancelled.Load() {
				t.Fatal("shutdown left local supervision running")
			}
			if n := runtime.stopCalls.Load(); n != 1 {
				t.Fatalf("shutdown signalled the runtime %d times, want once", n)
			}
			it, err := h.store.Get(t.Context(), r.WorkID)
			if err != nil || it.State != tc.state {
				t.Fatalf("shutdown state=%v err=%v, want %s", it.State, err, tc.state)
			}
			if tc.reason != "" && !strings.HasPrefix(it.StateReason, tc.reason) {
				t.Fatalf("shutdown reason %q, want %q", it.StateReason, tc.reason)
			}
			if h.rt.starts.Load() != 1 {
				t.Fatal("shutdown repeated execution")
			}
		})
	}
}
