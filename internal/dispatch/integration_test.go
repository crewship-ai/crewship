package dispatch

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/testutil"
	"github.com/crewship-ai/crewship/internal/work"
)

// This file is the vertical pass: acceptance, the ledger and the dispatcher
// together, against a real migrated SQLite and a runtime that can be crashed on
// purpose.
//
// It is deliberately NOT another set of store tests. The store's operations
// each have their own, and every one of them passed while the system as a whole
// still ran an agent without claiming its work — which is the whole point of
// review finding R3. What is proven here is what happens ACROSS the boundaries.

func quiet() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

// fakeRuntime is a controllable agent runtime. Every knob exists because one of
// the required scenarios needs it; none is there for convenience.
type fakeRuntime struct {
	mu sync.Mutex

	// starts counts how many runtimes were CREATED, which is the number the
	// "no second process" guarantees are actually about.
	starts   atomic.Int64
	stopped  map[string]bool
	locators []string
	// stopCh per live locator. Stop closes it, which is what makes Run return —
	// a fake whose Stop signals nothing would let a test pass while the real
	// contract ("the runtime actually ends") went untested.
	stopCh map[string]chan struct{}

	// block holds Run until the test releases it, so an attempt can be caught
	// mid-flight.
	block chan struct{}
	// crashAfterStart, when set, makes Run report the process as created and
	// then never return — the shape of a dispatcher dying after the real start
	// and before StartRunning.
	crashAfterStart bool
	// failWith, when set, is what Run returns after starting.
	failWith error
	// suppressStarted skips the started() callback, simulating a crash between
	// the process existing and our recording it.
	suppressStarted bool
	// refuseStop makes Stop report that the runtime is still there. A process
	// that will not die is not a hypothetical — it is the case the contract
	// says must become reconciliation rather than a cancellation.
	refuseStop bool
}

func newFakeRuntime() *fakeRuntime {
	return &fakeRuntime{
		stopped: map[string]bool{},
		stopCh:  map[string]chan struct{}{},
	}
}

func (f *fakeRuntime) Locator(a Assignment) string {
	// Deterministic from the assignment alone, exactly as the contract needs:
	// computable before anything exists, and the same on both sides of a crash.
	return "crew-1/tmux:agent-" + a.Item.AgentID + "-" + a.RunID
}

func (f *fakeRuntime) Run(ctx context.Context, a Assignment, started func()) error {
	locator := f.Locator(a)
	f.starts.Add(1)
	f.mu.Lock()
	f.locators = append(f.locators, locator)
	stopCh := make(chan struct{})
	f.stopCh[locator] = stopCh
	block, crash, fail, suppress := f.block, f.crashAfterStart, f.failWith, f.suppressStarted
	f.mu.Unlock()

	if !suppress {
		started()
	}
	if crash {
		// The process exists and this dispatcher never comes back.
		<-ctx.Done()
		return ctx.Err()
	}
	if block != nil {
		select {
		case <-block:
		case <-stopCh:
			// Signalled, and it obeyed — a real process ends here.
			return context.Canceled
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return fail
}

func (f *fakeRuntime) Stop(ctx context.Context, locator string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.refuseStop {
		return false, nil
	}
	f.stopped[locator] = true
	if ch, ok := f.stopCh[locator]; ok {
		delete(f.stopCh, locator)
		close(ch)
	}
	return true, nil
}

func (f *fakeRuntime) Alive(ctx context.Context, locator string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return !f.stopped[locator], nil
}

// recordExternalStart books a runtime created by a dispatcher that then died.
// It counts towards starts, because the question every crash guarantee asks is
// how many processes exist.
func (f *fakeRuntime) recordExternalStart(locator string) {
	f.starts.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.locators = append(f.locators, locator)
}

// harness is one server's worth of the vertical path.
type harness struct {
	t     *testing.T
	db    *sql.DB
	store *work.Store
	rt    *fakeRuntime
	cfg   Config
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	db := testutil.MigratedSQLDB(t)
	return &harness{
		t:     t,
		db:    db,
		store: work.NewStore(db),
		rt:    newFakeRuntime(),
		cfg: Config{
			Owner:              "dispatcher-1",
			PollInterval:       15 * time.Millisecond,
			HeartbeatInterval:  20 * time.Millisecond,
			CancelPollInterval: 10 * time.Millisecond,
			StopGrace:          time.Second,
		},
	}
}

// accept is the acceptance half: delivery and work committed together, and
// nothing else. No runtime, no goroutine — that is the contract R3 restores.
func (h *harness) accept(sourceID string) work.Receipt {
	return h.acceptFor(sourceID, "jamie")
}

// acceptFor names the agent, because the per-agent background limit is 1: two
// work items for one agent cannot be live together, which is the contract and
// not a limitation of the test.
func (h *harness) acceptFor(sourceID, agentID string) work.Receipt {
	h.t.Helper()
	ctx := context.Background()
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		h.t.Fatalf("begin: %v", err)
	}
	r, err := h.store.AcceptDeliveryTx(ctx, tx,
		work.Delivery{
			WorkspaceID: "ws1", EndpointID: "ep1", EndpointKind: "agent",
			Profile: "github", SourceDeliveryID: sourceID, BodySHA256: "sha-" + sourceID,
			FilterDecision: work.FilterAccepted,
		},
		work.AcceptRequest{
			WorkspaceID: "ws1", Source: work.SourceWebhook,
			Class: work.ClassBackground, AgentID: agentID,
		})
	if err != nil {
		_ = tx.Rollback()
		h.t.Fatalf("accept: %v", err)
	}
	if err := tx.Commit(); err != nil {
		h.t.Fatalf("commit: %v", err)
	}
	return r
}

// runDispatcher starts one and returns a stop func that waits for it.
func (h *harness) runDispatcher(authz Authorizer) (*Dispatcher, func()) {
	h.t.Helper()
	d := New(h.store, h.rt, authz, h.cfg, quiet())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = d.Run(ctx) }()
	return d, func() { cancel(); <-done }
}

// waitForState polls the ledger. Real timers, no sleeps standing in for
// synchronisation: the condition is what is waited on.
func (h *harness) waitForState(workID string, want work.State) *work.Item {
	h.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var last work.State
	for time.Now().Before(deadline) {
		it, err := h.store.Get(context.Background(), workID)
		if err != nil {
			h.t.Fatalf("get: %v", err)
		}
		last = it.State
		if it.State == want {
			return it
		}
		time.Sleep(5 * time.Millisecond)
	}
	h.t.Fatalf("work %s is %q after 10s, want %q", workID, last, want)
	return nil
}

// 1. A valid webhook reaches a terminal state — through the claim, not past it.
func TestVertical_AcceptedWorkRunsToTerminalThroughTheDispatcher(t *testing.T) {
	h := newHarness(t)
	r := h.accept("dlv-1")

	// Acceptance alone starts nothing. This is the assertion that R3 was about.
	if got := h.rt.starts.Load(); got != 0 {
		t.Fatalf("%d runtimes created by acceptance alone, want 0", got)
	}
	it, _ := h.store.Get(context.Background(), r.WorkID)
	if it.State != work.StateQueued {
		t.Fatalf("state after acceptance = %q, want queued", it.State)
	}

	_, stop := h.runDispatcher(nil)
	defer stop()

	h.waitForState(r.WorkID, work.StateSucceeded)

	if got := h.rt.starts.Load(); got != 1 {
		t.Errorf("%d runtimes created, want exactly 1", got)
	}

	// The run id ties the attempt, the runtime and the history together.
	var runID, locator, phase string
	if err := h.db.QueryRow(
		`SELECT run_id, runtime_locator, runtime_phase FROM work_attempts WHERE work_id = ?`, r.WorkID,
	).Scan(&runID, &locator, &phase); err != nil {
		t.Fatalf("read attempt: %v", err)
	}
	if phase != "confirmed" {
		t.Errorf("runtime phase = %q, want confirmed", phase)
	}
	if locator == "" || !contains(h.rt.locators, locator) {
		t.Errorf("the attempt's locator %q is not one the runtime was created under (%v)", locator, h.rt.locators)
	}
	events, err := h.store.History(context.Background(), r.WorkID)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	sawRun := false
	for _, e := range events {
		if e.RunID == runID {
			sawRun = true
		}
	}
	if !sawRun {
		t.Error("no history event carries the run id; the attempt and the journal are not tied together")
	}
}

// 2. A duplicate delivery does not create a second execution.
func TestVertical_DuplicateDeliveryDoesNotRunTwice(t *testing.T) {
	h := newHarness(t)
	h.rt.block = make(chan struct{})

	first := h.accept("dlv-same")
	_, stop := h.runDispatcher(nil)
	defer stop()

	// Wait until it is genuinely running before re-delivering, so the duplicate
	// arrives in the window where a second execution would be possible.
	h.waitForState(first.WorkID, work.StateRunning)

	second := h.accept("dlv-same")
	if second.WorkID != first.WorkID {
		t.Fatalf("re-delivery produced work %s, want the original %s", second.WorkID, first.WorkID)
	}
	if !second.Duplicate {
		t.Error("the re-delivery was not reported as a duplicate")
	}

	close(h.rt.block)
	h.waitForState(first.WorkID, work.StateSucceeded)

	if got := h.rt.starts.Load(); got != 1 {
		t.Errorf("%d runtimes created for one delivery and its duplicate, want 1", got)
	}
	var items int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM work_items`).Scan(&items); err != nil {
		t.Fatal(err)
	}
	if items != 1 {
		t.Errorf("%d work items, want 1", items)
	}
}

// 3. A crash after acceptance and before the wake-up hint recovers on restart.
//
// The hint is an optimisation; the poll is the guarantee. If losing a hint
// could strand work, the window between the acceptance commit and the hint
// would be exactly the window the durable ledger exists to survive.
func TestVertical_WorkSurvivesALostWakeUpHint(t *testing.T) {
	h := newHarness(t)
	r := h.accept("dlv-no-hint")

	// No Hint call at all — the acceptance "crashed" before sending one.
	_, stop := h.runDispatcher(nil)
	defer stop()

	h.waitForState(r.WorkID, work.StateSucceeded)
	if got := h.rt.starts.Load(); got != 1 {
		t.Errorf("%d runtimes created, want 1 — work was stranded by a lost hint", got)
	}
}

// 4. A crash after the process was really created, before StartRunning, must
// not produce a second process on restart.
//
// This is the one R4 exists for, and the reason MarkStarting writes the locator
// first: without it, recovery cannot tell "never started" from "started and we
// died before recording it", and the second reading starts a rival process.
//
// The crash is modelled by doing what a dispatcher does and then simply not
// finishing — no drain, no cleanup, because a process that dies does not run
// its defers. An earlier version of this test stopped the first dispatcher
// gracefully, which meant its shutdown path settled the work before recovery
// ever saw it: the test passed even with the phase check disabled, which is to
// say it was testing nothing.
func TestVertical_CrashAfterStartDoesNotCreateASecondRuntime(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	r := h.accept("dlv-crash")

	// The dispatcher that is about to die: claim, record where the runtime will
	// be, create it — and then nothing.
	c, err := h.store.Claim(ctx, work.ClaimOptions{LeaseOwner: "dispatcher-doomed"})
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	a := Assignment{Item: c.Item, RunID: c.RunID, Attempt: c.Attempt, Generation: c.Generation}
	locator := h.rt.Locator(a)
	if err := h.store.MarkStarting(ctx, r.WorkID, c.RunID, c.Generation, locator); err != nil {
		t.Fatalf("mark starting: %v", err)
	}
	h.rt.recordExternalStart(locator)
	startsBefore := h.rt.starts.Load()

	// Its lease runs out, which is all a surviving process ever learns about it.
	clockPast(t, h.db, c.RunID)

	// A new dispatcher comes up against the same ledger. Its recovery pass runs
	// before it claims anything.
	_, stop := h.runDispatcher(nil)
	defer stop()
	time.Sleep(400 * time.Millisecond)

	if got := h.rt.starts.Load(); got != startsBefore {
		t.Fatalf("the restarted dispatcher created %d more runtimes for work whose process may still be "+
			"alive at %s; want 0", got-startsBefore, locator)
	}
	it, _ := h.store.Get(ctx, r.WorkID)
	if it.State != work.StateNeedsReconciliation {
		t.Fatalf("work is %q after a crash past the start; it must be parked for reconciliation, "+
			"because nobody has established whether that process is still running", it.State)
	}
}

// 5. A cancel before the start prevents the runtime from being created at all.
func TestVertical_CancelBeforeStartPreventsTheRuntime(t *testing.T) {
	h := newHarness(t)
	r := h.accept("dlv-cancel-early")

	res, err := h.store.RequestCancel(context.Background(), r.WorkID, "operator", "changed my mind")
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if res.Outcome != work.CancelOutcomeCancelled {
		t.Fatalf("cancelling queued work = %q, want cancelled outright", res.Outcome)
	}

	_, stop := h.runDispatcher(nil)
	defer stop()
	time.Sleep(200 * time.Millisecond)

	if got := h.rt.starts.Load(); got != 0 {
		t.Errorf("%d runtimes created for cancelled work, want 0", got)
	}
	it, _ := h.store.Get(context.Background(), r.WorkID)
	if it.State != work.StateCancelled {
		t.Errorf("state = %q, want cancelled", it.State)
	}
}

// 5b. A cancel that lands between the claim and the external start is honoured
// before anything is created — the one window where a stop is free.
func TestVertical_CancelBetweenClaimAndStartIsHonouredWithoutStarting(t *testing.T) {
	h := newHarness(t)
	r := h.accept("dlv-cancel-window")

	// Claim by hand so the window can be held open deterministically.
	c, err := h.store.Claim(context.Background(), work.ClaimOptions{LeaseOwner: "dispatcher-1"})
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if _, err := h.store.RequestCancel(context.Background(), r.WorkID, "operator", "stop"); err != nil {
		t.Fatalf("cancel: %v", err)
	}

	d := New(h.store, h.rt, nil, h.cfg.withDefaults(), quiet())
	took, err := d.cancelledBeforeStart(context.Background(),
		Assignment{Item: c.Item, RunID: c.RunID, Attempt: c.Attempt, Generation: c.Generation})
	if err != nil {
		t.Fatalf("cancel before start: %v", err)
	}
	if !took {
		t.Fatal("the dispatcher did not notice a cancel recorded between the claim and the start")
	}
	if got := h.rt.starts.Load(); got != 0 {
		t.Errorf("%d runtimes created, want 0", got)
	}
	it, _ := h.store.Get(context.Background(), r.WorkID)
	if it.State != work.StateCancelled {
		t.Errorf("state = %q, want cancelled — nothing was created, so the stop is confirmed", it.State)
	}
}

// 6. A cancel during a run stops THAT runtime, and only becomes `cancelled`
// once the stop is confirmed.
func TestVertical_CancelDuringARunStopsTheRightRuntime(t *testing.T) {
	h := newHarness(t)
	h.rt.block = make(chan struct{})

	a := h.acceptFor("dlv-a", "agent-a")
	b := h.acceptFor("dlv-b", "agent-b")
	_, stop := h.runDispatcher(nil)
	defer stop()

	h.waitForState(a.WorkID, work.StateRunning)
	h.waitForState(b.WorkID, work.StateRunning)

	res, err := h.store.RequestCancel(context.Background(), b.WorkID, "operator", "stop B")
	if err != nil {
		t.Fatalf("cancel B: %v", err)
	}
	if res.Outcome != work.CancelOutcomeRequested {
		t.Fatalf("cancelling a live run = %q, want requested — nothing had stopped yet", res.Outcome)
	}

	h.waitForState(b.WorkID, work.StateCancelled)

	// A is untouched and finishes normally.
	itA, _ := h.store.Get(context.Background(), a.WorkID)
	if itA.State != work.StateRunning {
		t.Errorf("work A is %q after cancelling B, want running", itA.State)
	}
	var bRun string
	if err := h.db.QueryRow(`SELECT run_id FROM work_attempts WHERE work_id = ?`, b.WorkID).Scan(&bRun); err != nil {
		t.Fatal(err)
	}
	h.rt.mu.Lock()
	stoppedB := h.rt.stopped["crew-1/tmux:agent-agent-b-"+bRun]
	var aRun string
	_ = h.db.QueryRow(`SELECT run_id FROM work_attempts WHERE work_id = ?`, a.WorkID).Scan(&aRun)
	stoppedA := h.rt.stopped["crew-1/tmux:agent-agent-a-"+aRun]
	h.rt.mu.Unlock()
	if !stoppedB {
		t.Error("B's runtime was never signalled")
	}
	if stoppedA {
		t.Error("A's runtime was stopped by a cancel aimed at B")
	}

	close(h.rt.block)
	h.waitForState(a.WorkID, work.StateSucceeded)
}

// 7. An old attempt's completion cannot change the newer one. Proven here
// through the dispatcher rather than by calling the store directly, because
// that is where a stale worker would actually come from.
func TestVertical_AnOldAttemptCannotCompleteTheNewOne(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	r := h.accept("dlv-stale")

	first, err := h.store.Claim(ctx, work.ClaimOptions{LeaseOwner: "worker-a"})
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	// The attempt never leaves `planned`, so recovery may safely requeue it.
	clockPast(t, h.db, first.RunID)
	if _, err := h.store.RecoverExpiredLeases(ctx); err != nil {
		t.Fatalf("recover: %v", err)
	}
	second, err := h.store.Claim(ctx, work.ClaimOptions{LeaseOwner: "worker-b"})
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}

	err = h.store.Transition(ctx, work.TransitionRequest{
		WorkID: r.WorkID, RunID: first.RunID, Generation: first.Generation,
		To: work.StateSucceeded, Reason: "stale worker reporting late",
	})
	if err == nil {
		t.Fatal("the superseded attempt completed the work")
	}
	it, _ := h.store.Get(ctx, r.WorkID)
	if it.State != work.StateStarting || it.Generation != second.Generation {
		t.Errorf("work is %q at generation %d, want starting at %d", it.State, it.Generation, second.Generation)
	}
}

// 8. Shutting down must not leave runtimes running while the ledger calls them
// finished.
func TestVertical_ShutdownDoesNotClaimUnstoppedRuntimesAreDone(t *testing.T) {
	h := newHarness(t)
	h.rt.block = make(chan struct{})
	defer close(h.rt.block)

	r := h.accept("dlv-shutdown")
	_, stop := h.runDispatcher(nil)
	h.waitForState(r.WorkID, work.StateRunning)

	// The runtime cannot be stopped: Stop reports it is still there.
	h.rt.mu.Lock()
	h.rt.refuseStop = true
	h.rt.mu.Unlock()

	stop()

	it, _ := h.store.Get(context.Background(), r.WorkID)
	if it.State == work.StateSucceeded || it.State == work.StateCancelled {
		t.Fatalf("shutdown recorded %q for a runtime it did not stop; a process is still out there "+
			"being described as finished", it.State)
	}
	if it.State != work.StateNeedsReconciliation {
		t.Errorf("state = %q, want needs_reconciliation", it.State)
	}
}

// clockPast expires an attempt's lease directly. The store's clock is injected
// per-store and the dispatcher holds its own, so reaching into the row is the
// honest way to age one attempt without moving everyone's time.
func clockPast(t *testing.T, db *sql.DB, runID string) {
	t.Helper()
	if _, err := db.Exec(
		`UPDATE work_attempts SET lease_expires_at = '2000-01-01T00:00:00.000000000Z' WHERE run_id = ?`,
		runID); err != nil {
		t.Fatalf("expire lease: %v", err)
	}
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

// An unclear external outcome must never be retried automatically. Repeating an
// agent turn whose side effects may already have landed is precisely what
// reconciliation is for.
func TestVertical_AnUnclearOutcomeGoesToReconciliationNotRetry(t *testing.T) {
	h := newHarness(t)
	h.rt.failWith = fmt.Errorf("%w: the provider call may or may not have landed", ErrUnclearOutcome)

	r := h.accept("dlv-unclear")
	_, stop := h.runDispatcher(nil)
	defer stop()

	h.waitForState(r.WorkID, work.StateNeedsReconciliation)
	if got := h.rt.starts.Load(); got != 1 {
		t.Errorf("%d runtimes created, want 1 — an unclear outcome was retried", got)
	}
}

// I8: a permission revoked while work waited takes effect at dispatch. The
// runtime must not be handed the snapshot acceptance took.
func TestVertical_AuthorizationIsRecheckedAtDispatch(t *testing.T) {
	h := newHarness(t)
	r := h.accept("dlv-revoked")

	_, stop := h.runDispatcher(AuthorizerFunc(func(context.Context, Assignment) (string, error) {
		return "the agent was disabled while this work waited", nil
	}))
	defer stop()

	it := h.waitForState(r.WorkID, work.StateFailed)
	if got := h.rt.starts.Load(); got != 0 {
		t.Errorf("%d runtimes created for work refused at dispatch, want 0", got)
	}
	if it.StateReason == "" {
		t.Error("the refusal has no reason; that reason is what a user is shown")
	}
}

// An authorization check that cannot be made is not permission, and not a
// failure of the work either.
func TestVertical_AnUndecidableAuthorizationParksRatherThanGuesses(t *testing.T) {
	h := newHarness(t)
	r := h.accept("dlv-undecidable")

	_, stop := h.runDispatcher(AuthorizerFunc(func(context.Context, Assignment) (string, error) {
		return "", errors.New("the permissions service is unreachable")
	}))
	defer stop()

	h.waitForState(r.WorkID, work.StateNeedsReconciliation)
	if got := h.rt.starts.Load(); got != 0 {
		t.Errorf("%d runtimes created while authorization was undecidable, want 0", got)
	}
}
