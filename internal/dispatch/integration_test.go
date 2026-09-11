package dispatch

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
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
	// classifyAs is what Classify returns for a non-nil error. The zero value
	// is OutcomeUnclear, which is the production default too: a failure nobody
	// classified is one nobody can vouch for.
	classifyAs Outcome
	// onStart runs inside Run, at the instant the runtime is created, so a test
	// can observe the ledger from the middle of the start rather than after it.
	onStart func(Assignment)
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
	f.mu.Lock()
	block, crash, fail, suppress := f.block, f.crashAfterStart, f.failWith, f.suppressStarted
	onStart := f.onStart
	f.mu.Unlock()

	if onStart != nil {
		onStart(a)
	}
	// Publish existence only after the creation hook has inspected the durable
	// intent. A confirmation probe must not overtake creation in this fake.
	f.starts.Add(1)
	f.mu.Lock()
	f.locators = append(f.locators, locator)
	stopCh := make(chan struct{})
	f.stopCh[locator] = stopCh
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

func (f *fakeRuntime) Classify(a Assignment, err error) Outcome {
	if err == nil {
		return OutcomeSucceeded
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.classifyAs
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
	for _, existing := range f.locators {
		if existing == locator {
			return !f.stopped[locator], nil
		}
	}
	return false, nil
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
			Owner: "dispatcher-1",
			// Declared, because a dispatcher must be. The harness's acceptance
			// writes no domain kind, so this is the pair its own work really
			// has — stating it here keeps the test honest about what it is
			// claiming rather than relying on a filter that matches everything.
			Kinds:              []work.Kind{{Source: work.SourceWebhook}},
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
	h.rt.failWith = fmt.Errorf("the provider call may or may not have landed")

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

	_, stop := h.runDispatcher(AuthorizerFunc(func(context.Context, Assignment) (Decision, error) {
		return Refuse("the agent was deleted while this work waited"), nil
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

	_, stop := h.runDispatcher(AuthorizerFunc(func(context.Context, Assignment) (Decision, error) {
		return Decision{}, errors.New("the permissions service is unreachable")
	}))
	defer stop()

	h.waitForState(r.WorkID, work.StateNeedsReconciliation)
	if got := h.rt.starts.Load(); got != 0 {
		t.Errorf("%d runtimes created while authorization was undecidable, want 0", got)
	}
}

// Review follow-up 1. Recovery ran only at boot, so a server that restarted
// BEFORE an old lease expired skipped it on the way up — and nothing looked
// again. The work hung forever, and the symptom was silence.
func TestVertical_RecoveryRunsOnATimerNotOnlyAtBoot(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	r := h.accept("dlv-late-lease")

	// A previous process claimed this and died. Its lease is still VALID at
	// the moment the new dispatcher starts, so the boot pass cannot help.
	c, err := h.store.Claim(ctx, work.ClaimOptions{LeaseOwner: "dispatcher-gone"})
	if err != nil {
		t.Fatalf("claim: %v", err)
	}

	_, stop := h.runDispatcher(nil)
	defer stop()

	// The boot pass has certainly run by now and correctly did nothing.
	time.Sleep(100 * time.Millisecond)
	it, _ := h.store.Get(ctx, r.WorkID)
	if it.State != work.StateStarting {
		t.Fatalf("state = %q, want starting — the boot pass should not touch a live lease", it.State)
	}

	// Now the lease lapses, with no restart.
	clockPast(t, h.db, c.RunID)

	// A later pass has to notice. The attempt never left `planned`, so the
	// safe resolution is to put it back on the queue and run it.
	h.waitForState(r.WorkID, work.StateSucceeded)
}

// Review follow-up 2. Every unrecognised error used to become a retry, which
// reads "the run returned an error" as "nothing happened". An agent turn can
// make an external change and then fail; repeating it repeats whatever it did.
func TestVertical_OnlyAProvablySafeFailureIsRetried(t *testing.T) {
	tests := []struct {
		name      string
		classify  Outcome
		wantState work.State
		wantRuns  int64
	}{
		{
			name:      "a failure the runtime cannot vouch for goes to reconciliation",
			classify:  OutcomeUnclear,
			wantState: work.StateNeedsReconciliation,
			wantRuns:  1,
		},
		{
			name:      "a failure known to precede any external effect is retried",
			classify:  OutcomeRetryable,
			wantState: work.StateRetryWait,
			wantRuns:  1,
		},
		{
			name:      "a permanent failure is not retried",
			classify:  OutcomeFailed,
			wantState: work.StateFailed,
			wantRuns:  1,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.rt.failWith = errors.New("the run failed")
			h.rt.classifyAs = tc.classify

			r := h.accept("dlv-classify")
			_, stop := h.runDispatcher(nil)
			defer stop()

			h.waitForState(r.WorkID, tc.wantState)
			if got := h.rt.starts.Load(); got != tc.wantRuns {
				t.Errorf("%d runtimes created, want %d", got, tc.wantRuns)
			}
		})
	}
}

// Review follow-up 3. Cancel signalled once, ignored whether the runtime
// actually stopped, and returned. A runtime that ignored the signal was never
// followed up — and because Run never returned, nothing settled at all: the
// work sat `running` forever with a request against it nobody acted on.
func TestVertical_ACancelledRuntimeThatWillNotStopIsAbandonedNotForgotten(t *testing.T) {
	h := newHarness(t)
	h.cfg.StopGrace = 50 * time.Millisecond
	h.rt.block = make(chan struct{})
	defer close(h.rt.block)

	r := h.accept("dlv-stubborn")
	_, stop := h.runDispatcher(nil)
	defer stop()

	h.waitForState(r.WorkID, work.StateRunning)

	// The runtime will not die.
	h.rt.mu.Lock()
	h.rt.refuseStop = true
	h.rt.mu.Unlock()

	if _, err := h.store.RequestCancel(context.Background(), r.WorkID, "operator", "stop"); err != nil {
		t.Fatalf("cancel: %v", err)
	}

	// It must NOT be reported cancelled, and it must not sit running forever.
	it := h.waitForState(r.WorkID, work.StateNeedsReconciliation)
	if it.StateReason == "" {
		t.Error("the parked work has no reason; an operator has nothing to act on")
	}
}

// Review follow-up 4. Losing the lease stopped the heartbeat and left the AGENT
// running — so the old attempt kept executing beside the newer one that had
// taken the work. Two runtimes for one work item is the thing the fence exists
// to prevent, and stopping only the lease renewal does not prevent it.
func TestVertical_LosingTheLeaseStopsTheRunNotJustTheHeartbeat(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.rt.block = make(chan struct{})
	defer close(h.rt.block)

	r := h.accept("dlv-superseded")
	_, stop := h.runDispatcher(nil)
	defer stop()

	h.waitForState(r.WorkID, work.StateRunning)
	var firstRun string
	if err := h.db.QueryRow(`SELECT run_id FROM work_attempts WHERE work_id = ?`, r.WorkID).Scan(&firstRun); err != nil {
		t.Fatal(err)
	}
	locator := "crew-1/tmux:agent-jamie-" + firstRun

	// Another process takes the work: expire the lease and recover, then claim.
	clockPast(t, h.db, firstRun)
	if _, err := h.store.RecoverExpiredLeases(ctx); err != nil {
		t.Fatalf("recover: %v", err)
	}

	// The supervisor must notice it has been superseded and STOP the runtime.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		h.rt.mu.Lock()
		stopped := h.rt.stopped[locator]
		h.rt.mu.Unlock()
		if stopped {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the superseded attempt's runtime at %s was never stopped; it would keep executing "+
		"beside whichever attempt replaced it", locator)
}

// Review follow-up 9. The first stream event is a hint, not the condition.
//
// A silent process is a running one: an agent that starts, thinks, and prints
// nothing would otherwise never be recorded as running — and a dispatcher that
// reads "no output yet" as "not started" is inferring absence from silence,
// which is the mistake the whole start protocol exists to avoid.
func TestVertical_ASilentRuntimeIsStillConfirmedRunning(t *testing.T) {
	h := newHarness(t)
	h.cfg.ConfirmPollInterval = 10 * time.Millisecond
	// The runtime produces no stream events at all, and does not finish.
	h.rt.suppressStarted = true
	h.rt.block = make(chan struct{})
	defer close(h.rt.block)

	r := h.accept("dlv-silent")
	_, stop := h.runDispatcher(nil)
	defer stop()

	// Confirmation has to come from the provider's own answer.
	it := h.waitForState(r.WorkID, work.StateRunning)
	if it.State != work.StateRunning {
		t.Fatalf("state = %q, want running", it.State)
	}
	var phase string
	if err := h.db.QueryRow(
		`SELECT runtime_phase FROM work_attempts WHERE work_id = ?`, r.WorkID).Scan(&phase); err != nil {
		t.Fatal(err)
	}
	if phase != "confirmed" {
		t.Errorf("runtime phase = %q, want confirmed", phase)
	}
}

// And the order must hold: the intent is durable BEFORE the runtime exists,
// and confirmation comes after. A confirmation that overtook the intent would
// mean a locator written after the danger passed.
func TestVertical_IntentIsDurableBeforeTheRuntimeExists(t *testing.T) {
	h := newHarness(t)
	h.cfg.ConfirmPollInterval = 10 * time.Millisecond
	h.rt.block = make(chan struct{})
	defer close(h.rt.block)
	r := h.accept("dlv-order")

	phaseAtStart := make(chan string, 1)
	release := make(chan struct{})
	resume := sync.OnceFunc(func() { close(release) })
	h.rt.onStart = func(a Assignment) {
		var phase string
		if err := h.db.QueryRow(`SELECT runtime_phase FROM work_attempts WHERE run_id = ?`, a.RunID).Scan(&phase); err != nil {
			phase = "read failed: " + err.Error()
		}
		phaseAtStart <- phase
		<-release
	}
	_, stop := h.runDispatcher(nil)
	defer func() { resume(); stop() }()

	select {
	case phase := <-phaseAtStart:
		if phase != "starting" {
			t.Fatalf("before runtime creation the attempt was %q, want starting", phase)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the runtime creation hook was never reached")
	}
	var locator string
	if err := h.db.QueryRow(`SELECT runtime_locator FROM work_attempts WHERE work_id = ?`, r.WorkID).Scan(&locator); err != nil {
		t.Fatal(err)
	}
	// A durable locator names the future runtime; it is not evidence that the
	// runtime exists. The provider must still answer false while creation waits.
	if alive, err := h.rt.Alive(context.Background(), locator); err != nil || alive {
		t.Fatalf("before creation Alive(%q) = %v, %v; want false, nil", locator, alive, err)
	}
	resume()
	h.waitForState(r.WorkID, work.StateRunning)
}

// A run that finishes before the confirmation probe has polled still settles.
//
// The probe is a poll, so there is always a window in which a short run ends
// while the attempt is still `starting`. This is not an edge case dressed up as
// one: with a one-second production poll interval it is the COMMON case for
// anything quick. The end-to-end pass found it with nothing more exotic than a
// runtime that returned immediately.
func TestVertical_AShortRunSettlesEvenIfTheProbeNeverPolled(t *testing.T) {
	h := newHarness(t)
	// Long enough that the probe cannot possibly fire first, which is what makes
	// this test about the unconfirmed path rather than about timing luck.
	h.cfg.ConfirmPollInterval = time.Hour
	h.rt.suppressStarted = true

	rec := h.accept("dlv-short")
	_, stop := h.runDispatcher(nil)
	defer stop()

	it := h.waitForState(rec.WorkID, work.StateSucceeded)
	if it.StateReason == "" {
		t.Error("settled with no reason recorded")
	}
	// And the history does not invent an observation nobody made.
	events, err := h.store.History(context.Background(), rec.WorkID)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range events {
		if e.ToState == work.StateRunning {
			t.Error("a `running` transition was synthesised for a run nothing observed running")
		}
	}
}

// An outcome that cannot be written must not simply vanish.
//
// finish used to log a rejected transition and return, which left the attempt
// in a live state holding a slot, with its real result recorded nowhere — and
// the only thing that eventually noticed was lease expiry, sixty seconds later,
// which then described a finished run as an abandoned one. The backstop turns
// an unwritable outcome into a visible one immediately, and carries what it was
// trying to say.
func TestVertical_AnUnwritableOutcomeIsParkedRatherThanLost(t *testing.T) {
	h := newHarness(t)
	rec := h.accept("dlv-unwritable")
	d := New(h.store, h.rt, nil, h.cfg, quiet())

	// A real claim, so the attempt is properly bound and the fence has nothing
	// to object to — this must be about the EDGE being refused, not about an
	// unbound write, which is a different failure with a different answer.
	//
	// The edge is forced rather than provoked: the settle path targeted
	// `succeeded` from `starting` when this bug was live, and that edge now
	// exists. `waiting` stands in for the next missing one. The guard is generic
	// on purpose, because the thing it protects against is a settle target
	// somebody adds later without adding its edge.
	ctx := context.Background()
	claimed, err := h.store.Claim(ctx, work.ClaimOptions{
		LeaseOwner: "dispatcher-1", Limits: work.DefaultLimits(),
	})
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	d.finish(ctx, Assignment{
		Item: claimed.Item, RunID: claimed.RunID, Generation: claimed.Generation,
	}, work.StateWaiting, "completed")

	got, err := h.store.Get(ctx, rec.WorkID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != work.StateNeedsReconciliation {
		t.Fatalf("state = %q, want needs_reconciliation — an outcome the ledger refused must "+
			"become somebody's problem, not nobody's", got.State)
	}
	if !strings.Contains(got.StateReason, "waiting") {
		t.Errorf("reason = %q; it must name the outcome that could not be recorded", got.StateReason)
	}
	if !strings.Contains(got.StateReason, "completed") {
		t.Errorf("reason = %q; it must carry what the outcome was trying to say", got.StateReason)
	}
}

// A dispatcher that declares nothing must not start.
//
// The filter is what stands between this loop and another executor's work, and
// there is no safe default for it: only the caller knows what its Runtime can
// run. An empty declaration is not "run everything" — it is a caller who forgot,
// and the cost of guessing on their behalf is destroyed work.
func TestVertical_ADispatcherThatDeclaresNothingRefusesToStart(t *testing.T) {
	h := newHarness(t)
	h.cfg.Kinds = nil
	d := New(h.store, h.rt, nil, h.cfg, quiet())

	err := d.Run(context.Background())
	if err == nil {
		t.Fatal("a dispatcher with no declared kinds started; it would claim every producer's work")
	}
	if !strings.Contains(err.Error(), "executable kinds") {
		t.Errorf("err = %v; it must say what is missing", err)
	}
}

// "Not yet" is a third answer, and it must cost the work nothing.
//
// An authorizer that can only allow or refuse has to call a held agent's work
// FAILED, and then the operator's approval arrives at something that already
// gave up. Retrying it as an ordinary failure is no better: MaxAttempts of
// capped backoff is about twenty minutes, and the answer does not change on
// that timescale — it changes when a person acts.
//
// So a deferral returns the work to the queue with its attempt GIVEN BACK, and
// this asserts the whole of that: the state, the unspent budget, the fence that
// still moved forward, and that no runtime was created on the way.
func TestVertical_AHeldAuthorizationDefersWithoutSpendingAnAttempt(t *testing.T) {
	h := newHarness(t)
	r := h.accept("dlv-held")

	var held atomic.Bool
	held.Store(true)
	_, stop := h.runDispatcher(AuthorizerFunc(func(context.Context, Assignment) (Decision, error) {
		if held.Load() {
			// Short, so the test observes the requeue rather than the wait.
			return NotYet("agent is PENDING_REVIEW", 50*time.Millisecond), nil
		}
		return Allow(), nil
	}))
	defer stop()

	// It comes back to the queue rather than failing.
	deadline := time.Now().Add(10 * time.Second)
	var it *work.Item
	for time.Now().Before(deadline) {
		got, err := h.store.Get(context.Background(), r.WorkID)
		if err != nil {
			t.Fatal(err)
		}
		if got.State == work.StateQueued && got.Generation > 0 {
			it = got
			break
		}
		if got.State.Terminal() {
			t.Fatalf("held work went to %q; a deferral must not be terminal", got.State)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if it == nil {
		t.Fatal("held work never came back to the queue")
	}
	if it.Attempts != 0 {
		t.Errorf("attempts = %d after a deferral, want 0 — a held agent would burn its whole "+
			"budget waiting for a person", it.Attempts)
	}
	if it.StateReason == "" {
		t.Error("requeued with no reason; nothing tells an operator why it is waiting")
	}
	if got := h.rt.starts.Load(); got != 0 {
		t.Errorf("%d runtimes created for deferred work, want 0", got)
	}

	// And when the answer changes, it runs — on a fresh attempt, with the fence
	// ahead of where it was.
	before := it.Generation
	held.Store(false)
	done := h.waitForState(r.WorkID, work.StateSucceeded)
	if done.Generation <= before {
		t.Errorf("generation went from %d to %d across a deferral; the fence must never run "+
			"backwards, or a deferred attempt's run id could still report a result",
			before, done.Generation)
	}
	if got := h.rt.starts.Load(); got != 1 {
		t.Errorf("%d runtimes created once the hold cleared, want 1", got)
	}
}

// The attempt may only be given back when nothing could have been started.
//
// A deferral hands an attempt back, so it is the one operation that could let
// an item be retried forever. That is safe exactly while the attempt never
// declared a start intent, and the store checks that rather than trusting its
// caller — because the caller is the thing most likely to be wrong about it.
func TestVertical_ADeferralIsRefusedOnceAStartWasIntended(t *testing.T) {
	h := newHarness(t)
	r := h.accept("dlv-defer-late")
	ctx := context.Background()

	claimed, err := h.store.Claim(ctx, work.ClaimOptions{
		LeaseOwner: "dispatcher-1", Limits: work.DefaultLimits(),
		Kinds: []work.Kind{{Source: work.SourceWebhook}},
	})
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	// Past the point of no return: an identity is written down, so a runtime
	// may exist under it.
	if err := h.store.MarkStarting(ctx, r.WorkID, claimed.RunID, claimed.Generation,
		"fake-runtime:"+claimed.RunID); err != nil {
		t.Fatalf("mark starting: %v", err)
	}

	err = h.store.Defer(ctx, r.WorkID, claimed.RunID, claimed.Generation,
		time.Now().Add(time.Minute), "too late")
	if err == nil {
		t.Fatal("an attempt that declared a start intent was given back; it could now be retried " +
			"beside a runtime nobody stopped")
	}
	if !errors.Is(err, work.ErrNotBound) {
		t.Errorf("err = %v, want ErrNotBound", err)
	}
}

// Review P1 on the deferral work: a cancel that lands WHILE the authorizer is
// deciding must not be thrown away by the deferral that follows.
//
// The dispatcher checks for a cancel right after the claim; the authorizer runs
// after that; and the cancel is recorded on the attempt the deferral is about
// to close. So the only place the decision can be made without a race is
// inside the deferral's own transaction — which this drives end to end: the
// authorizer is paused on a channel, the cancel is requested through the store
// while it is paused, and then it answers "not yet".
func TestVertical_ACancelDuringAPausedAuthorizationSurvivesTheDeferral(t *testing.T) {
	h := newHarness(t)
	r := h.accept("dlv-cancel-while-held")

	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	_, stop := h.runDispatcher(AuthorizerFunc(func(ctx context.Context, a Assignment) (Decision, error) {
		once.Do(func() { close(entered) })
		select {
		case <-release:
		case <-ctx.Done():
		}
		return NotYet("agent is PENDING_REVIEW", 50*time.Millisecond), nil
	}))
	defer stop()

	// The attempt is claimed and the authorizer is mid-decision.
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the authorizer was never consulted")
	}

	out, err := h.store.RequestCancel(context.Background(), r.WorkID, "operator", "stop")
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if out.Outcome != work.CancelOutcomeRequested {
		t.Fatalf("cancel outcome = %q, want requested — the attempt is live while the authorizer thinks", out.Outcome)
	}

	// Now the authorizer says "not yet", and the deferral runs.
	close(release)

	it := h.waitForState(r.WorkID, work.StateCancelled)
	if !strings.Contains(it.StateReason, "cancelled while held") {
		t.Errorf("reason = %q, want it to say the cancel was honoured during the hold", it.StateReason)
	}
	if got := h.rt.starts.Load(); got != 0 {
		t.Errorf("%d runtimes created for work cancelled while held, want 0", got)
	}
	// And it stays cancelled: a later poll must not find it claimable.
	time.Sleep(200 * time.Millisecond)
	again, err := h.store.Get(context.Background(), r.WorkID)
	if err != nil {
		t.Fatal(err)
	}
	if again.State != work.StateCancelled {
		t.Errorf("state = %q after further polls, want cancelled", again.State)
	}
}
