package pipeline

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeExecutor is a runExecutor that sleeps for a fixed delay per Run,
// recording how many Runs overlapped (high-water mark) and how many
// completed. It lets the dispatcher tests prove that co-due rows fire
// concurrently instead of serially.
//
// gate, when set, replaces the delay with a barrier the test opens by hand.
// A duration says "probably still running when I look"; a barrier says "still
// running until I say otherwise", which is the difference between a test that
// usually exercises a drain and one that always does.
type fakeExecutor struct {
	delay time.Duration
	gate  <-chan struct{}

	// barrierN > 0 turns Run into a rendezvous: every call blocks until
	// barrierN of them are in flight AT ONCE, then all are released.
	//
	// This exists so a concurrency test can assert concurrency instead of
	// timing it. A dispatcher that serialises its work can never assemble
	// the rendezvous, however fast or slow the machine is, so the
	// assertion holds under `-race` on a loaded runner exactly as it does
	// on an idle laptop — which a wall-clock upper bound does not (#1597).
	barrierN    int32
	barrierCh   chan struct{}
	barrierOnce sync.Once

	inFlight  int32
	maxInWork int32
	completed int32

	// seenMu/seen record what the dispatcher actually asked the executor
	// for. Concurrency tests ignore it; the attribution test is about the
	// CONTENT of the request rather than its timing.
	seenMu sync.Mutex
	seen   []RunInput
}

// barrierAbandonAfter releases a rendezvous that will never assemble, so
// a failing test reports its own assertion instead of hanging until the
// package -timeout kills every other test's output with it.
//
// It is a FAILURE deadline, not a success one: on the passing path the
// barrier closes as soon as the last participant arrives and this timer
// is never read. Nothing about the assertion depends on its value.
const barrierAbandonAfter = 10 * time.Second

func (f *fakeExecutor) Run(ctx context.Context, in RunInput) (*RunResult, error) {
	f.seenMu.Lock()
	f.seen = append(f.seen, in)
	f.seenMu.Unlock()
	cur := atomic.AddInt32(&f.inFlight, 1)
	for {
		hi := atomic.LoadInt32(&f.maxInWork)
		if cur <= hi || atomic.CompareAndSwapInt32(&f.maxInWork, hi, cur) {
			break
		}
	}
	switch {
	case f.barrierN > 0:
		if cur >= f.barrierN {
			f.barrierOnce.Do(func() { close(f.barrierCh) })
		}
		select {
		case <-f.barrierCh:
		case <-ctx.Done():
		case <-time.After(barrierAbandonAfter):
		}
	case f.gate != nil:
		select {
		case <-f.gate:
		case <-ctx.Done():
		}
	case f.delay > 0:
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
		}
	}
	atomic.AddInt32(&f.inFlight, -1)
	atomic.AddInt32(&f.completed, 1)
	return &RunResult{RunID: "run_" + in.TriggeredByID, Status: "COMPLETED"}, nil
}

// enqueueDue seeds n due pending rows and returns the store.
func enqueueDue(t *testing.T, n int) *PendingRunStore {
	t.Helper()
	s := NewPendingRunStore(newPendingDB(t))
	ctx := context.Background()
	past := time.Now().Add(-time.Minute)
	for i := 0; i < n; i++ {
		id := "p" + string(rune('a'+i))
		if _, _, err := s.Enqueue(ctx, PendingRun{
			ID: id, WorkspaceID: "w", PipelineID: "pl", PipelineSlug: "s", FireAt: past,
		}); err != nil {
			t.Fatalf("enqueue %s: %v", id, err)
		}
	}
	return s
}

func TestN4RearmedPendingStartHasNewIdempotencyKey(t *testing.T) {
	s := enqueueDue(t, 1)
	exec := &fakeExecutor{}
	d := NewPendingRunDispatcher(s, exec, nil)
	ctx := t.Context()
	first := PendingRun{ID: "pa"}
	d.fireOne(ctx, first)
	secondAt := formatRFC3339(time.Now().Add(-time.Second))
	if _, err := s.db.ExecContext(ctx, `UPDATE pending_runs SET status='pending',fire_at=? WHERE id='pa'`, secondAt); err != nil {
		t.Fatal(err)
	}
	d.fireOne(ctx, first)
	if len(exec.seen) != 2 {
		t.Fatalf("dispatches=%d", len(exec.seen))
	}
	if exec.seen[0].IdempotencyKey == exec.seen[1].IdempotencyKey {
		t.Fatal("new scheduled start reuses consumed start identity")
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE pending_runs SET status='pending' WHERE id='pa'`); err != nil {
		t.Fatal(err)
	}
	d.fireOne(ctx, first)
	if len(exec.seen) != 3 || exec.seen[2].IdempotencyKey != exec.seen[1].IdempotencyKey {
		t.Fatal("retry must retain the same scheduled start identity")
	}
}

// waitFor polls cond until true or the deadline elapses.
func waitFor(t *testing.T, d time.Duration, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(2 * time.Millisecond)
	}
	return cond()
}

// TestPendingDispatcher_ConcurrentDispatch: 6 co-due runs must all be in
// flight at once, not drain serially. This is the #834 throughput cliff
// regression guard.
//
// It used to prove that by timing the drain — 6 runs × a 200ms sleep,
// asserting the whole thing finished inside 800ms. That measures the
// machine as much as the dispatcher: under `-race` on a loaded CI runner
// the same correct code exceeds the bound, which is a red `Go Race` that
// reports no race and names a package the diff never touched (#1597).
//
// The rendezvous asserts the property directly instead. Each run blocks
// until all six are in flight together, so a serialising dispatcher can
// never satisfy it and a slow one still can. There is no duration in the
// pass condition at all — the deadline below is only how long a FAILING
// run waits before saying so.
func TestPendingDispatcher_ConcurrentDispatch(t *testing.T) {
	const want = 6
	store := enqueueDue(t, want)
	exec := &fakeExecutor{barrierN: want, barrierCh: make(chan struct{})}
	d := NewPendingRunDispatcher(store, exec, nil)

	d.Start(context.Background())
	defer d.Stop()

	if !waitFor(t, barrierAbandonAfter, func() bool {
		return atomic.LoadInt32(&exec.maxInWork) >= want
	}) {
		t.Fatalf("max concurrent dispatches was %d, want %d — the dispatcher is serialising "+
			"(pool is %d, so all %d co-due runs should overlap)",
			atomic.LoadInt32(&exec.maxInWork), want, defaultDispatchConcurrency, want)
	}
	if !waitFor(t, barrierAbandonAfter, func() bool {
		return atomic.LoadInt32(&exec.completed) == want
	}) {
		t.Fatalf("only %d/%d runs completed after the rendezvous released",
			atomic.LoadInt32(&exec.completed), want)
	}
}

// TestPendingDispatcher_BoundedConcurrency: the worker pool must cap how
// many runs execute at once so a burst can't stampede the provider.
func TestPendingDispatcher_BoundedConcurrency(t *testing.T) {
	store := enqueueDue(t, 8)
	exec := &fakeExecutor{delay: 80 * time.Millisecond}
	d := NewPendingRunDispatcher(store, exec, nil)
	d.maxConcurrency = 3

	d.Start(context.Background())
	if !waitFor(t, 5*time.Second, func() bool { return atomic.LoadInt32(&exec.completed) == 8 }) {
		t.Fatalf("only %d/8 runs completed", atomic.LoadInt32(&exec.completed))
	}
	d.Stop()

	if hw := atomic.LoadInt32(&exec.maxInWork); hw > 3 {
		t.Fatalf("pool bound violated: max concurrency %d > 3", hw)
	}
}

// TestPendingDispatcher_StopDrainsInFlight: Stop() must block until every
// dispatched goroutine has finished (graceful shutdown / WaitGroup drain).
//
// Stop is not a flush. sweep() returns on stopCh and abandons the
// not-yet-dispatched tail on purpose, so anything still queued when Stop lands
// never runs. The original test waited for `inFlight > 0` — one goroutine of
// four — and then required all four to have completed, which asserts the
// opposite of that design and holds only when the sweep wins the race. Under
// -race on a contended runner it lost: `expected 4 completed, got 1`.
//
// Nothing here is timed. The runs block on a barrier this test opens by hand,
// so the drain is not something the test hopes to catch in progress — it is a
// state the test holds open. That makes the real contract observable: Stop is
// called while four goroutines are provably still running, and must not return
// until every one of them has finished.
func TestPendingDispatcher_StopDrainsInFlight(t *testing.T) {
	const n = 4
	store := enqueueDue(t, n)
	release := make(chan struct{})
	exec := &fakeExecutor{gate: release}
	d := NewPendingRunDispatcher(store, exec, nil)

	var releaseOnce, stopOnce sync.Once
	letGo := func() { releaseOnce.Do(func() { close(release) }) }
	stop := func() { stopOnce.Do(d.Stop) }

	d.Start(context.Background())
	// Registered before the first Fatalf can fire. Start took
	// context.Background(), so a dispatcher left running outlives this test and
	// leaks into whichever one runs next — the order-dependence class #1551
	// closed. Release first: Stop waits on the WaitGroup, and the gated runs
	// are what it is waiting for, so stopping without opening the barrier would
	// hang rather than clean up.
	t.Cleanup(func() { letGo(); stop() })

	if !waitFor(t, 5*time.Second, func() bool { return atomic.LoadInt32(&exec.inFlight) == n }) {
		t.Fatalf("precondition: only %d/%d runs reached the executor, so there is no full drain to test",
			atomic.LoadInt32(&exec.inFlight), n)
	}

	stopped := make(chan struct{})
	go func() { defer close(stopped); stop() }()

	// Stop must still be blocked: all four runs are parked on the barrier. This
	// is the assertion the previous version could not make, because it had no
	// way to keep a run in flight. A generous window — its only job is to catch
	// a Stop that returned early, and it cannot produce a false failure, since
	// a correct Stop stays blocked indefinitely.
	select {
	case <-stopped:
		t.Fatal("Stop returned while all runs were still in flight — it did not wait for the drain")
	case <-time.After(200 * time.Millisecond):
	}

	letGo()

	select {
	case <-stopped:
	case <-time.After(10 * time.Second):
		t.Fatal("Stop did not return after the in-flight runs were released")
	}

	if got := atomic.LoadInt32(&exec.inFlight); got != 0 {
		t.Fatalf("Stop returned with %d runs still in flight", got)
	}
	if got := atomic.LoadInt32(&exec.completed); got != n {
		t.Fatalf("expected %d completed after Stop drain, got %d", n, got)
	}
}

// TestPendingDispatcher_NoDoubleFire: overlapping sweeps must never
// dispatch the same claimed row twice — MarkFired is the single-claim
// guard. Each row's fired_run_id is backfilled exactly once.
func TestPendingDispatcher_NoDoubleFire(t *testing.T) {
	store := enqueueDue(t, 5)
	exec := &fakeExecutor{delay: 30 * time.Millisecond}
	d := NewPendingRunDispatcher(store, exec, nil)

	// Two concurrent sweeps racing over the same due set.
	ctx := context.Background()
	d.sem = make(chan struct{}, 8)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); d.sweep(ctx) }()
	}
	wg.Wait()
	d.wg.Wait()

	// Exactly 5 runs — never 10 — despite two sweeps seeing all 5 rows.
	if got := atomic.LoadInt32(&exec.completed); got != 5 {
		t.Fatalf("double-fire: expected 5 runs, got %d", got)
	}
}

// capturingExecutor records the RunInput of the last Run so a test can
// assert what the dispatcher threaded through.
type capturingExecutor struct {
	mu   sync.Mutex
	last RunInput
}

func (c *capturingExecutor) Run(_ context.Context, in RunInput) (*RunResult, error) {
	c.mu.Lock()
	c.last = in
	c.mu.Unlock()
	return &RunResult{RunID: "run_" + in.TriggeredByID, Status: "COMPLETED"}, nil
}

// TestDispatcher_ThreadsInvokingUser proves a deferred run's enqueuing user
// reaches the executor's RunInput, so a `to: trigger` notify in that run
// resolves to the real triggerer (#842 Phase 1, deferred half).
func TestDispatcher_ThreadsInvokingUser(t *testing.T) {
	store := NewPendingRunStore(newPendingDB(t))
	ctx := context.Background()
	if _, _, err := store.Enqueue(ctx, PendingRun{
		ID: "p1", WorkspaceID: "w", PipelineID: "pl", PipelineSlug: "s",
		InvokingUserID: "usr_trigger", FireAt: time.Now().Add(-time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	exec := &capturingExecutor{}
	d := NewPendingRunDispatcher(store, exec, nil)
	d.Start(ctx)
	waitFor(t, 2*time.Second, func() bool {
		exec.mu.Lock()
		defer exec.mu.Unlock()
		return exec.last.InvokingUserID != ""
	})
	d.Stop()
	if exec.last.InvokingUserID != "usr_trigger" {
		t.Errorf("dispatcher RunInput.InvokingUserID = %q, want usr_trigger", exec.last.InvokingUserID)
	}
}

// The dispatcher must ASK for what the row says, not just be able to.
//
// effectivePendingTrigger and the store round-trip are each pinned on their
// own, but "the dispatcher actually consults them" is a third fact: mutating
// the dispatcher back to a hard-coded schedule left both of those suites
// green, which is exactly how the original bug survived.
func TestPendingDispatcher_HonoursTheRowsAttribution(t *testing.T) {
	s := NewPendingRunStore(newPendingDB(t))
	ctx := context.Background()
	past := time.Now().Add(-time.Minute)

	if _, _, err := s.Enqueue(ctx, PendingRun{
		ID: "p_auto", WorkspaceID: "w", PipelineID: "pl", PipelineSlug: "s", FireAt: past,
		TriggeredVia: TriggeredViaAutomation, TriggeredByID: "aut_abc",
	}); err != nil {
		t.Fatalf("enqueue attributed: %v", err)
	}
	if _, _, err := s.Enqueue(ctx, PendingRun{
		ID: "p_plain", WorkspaceID: "w", PipelineID: "pl", PipelineSlug: "s", FireAt: past,
	}); err != nil {
		t.Fatalf("enqueue plain: %v", err)
	}

	exec := &fakeExecutor{}
	d := NewPendingRunDispatcher(s, exec, nil)
	d.Start(ctx)
	defer d.Stop()

	if !waitFor(t, barrierAbandonAfter, func() bool {
		exec.seenMu.Lock()
		defer exec.seenMu.Unlock()
		return len(exec.seen) >= 2
	}) {
		t.Fatal("dispatcher never fired both rows")
	}

	exec.seenMu.Lock()
	defer exec.seenMu.Unlock()
	var sawAutomation, sawSchedule bool
	for _, in := range exec.seen {
		switch in.TriggeredVia {
		case TriggeredViaAutomation:
			sawAutomation = true
			if in.TriggeredByID != "aut_abc" {
				t.Errorf("automation run points at %q, want the automation id", in.TriggeredByID)
			}
		case TriggeredViaSchedule:
			sawSchedule = true
			if in.TriggeredByID != "p_plain" {
				t.Errorf("unattributed run points at %q, want its own pending id", in.TriggeredByID)
			}
		default:
			t.Errorf("unexpected trigger %q", in.TriggeredVia)
		}
	}
	if !sawAutomation {
		t.Error("no run reported the automation — a rule fired and the dispatcher called it a cron")
	}
	if !sawSchedule {
		t.Error("the unattributed row stopped defaulting to schedule")
	}
}

func TestPendingDispatcherPreservesPinnedAndLegacyVersionPolicy(t *testing.T) {
	s := NewPendingRunStore(newPendingDB(t))
	ctx := context.Background()
	past := time.Now().Add(-time.Minute)
	version := 3
	for _, pr := range []PendingRun{{ID: "pinned", PinnedVersion: &version}, {ID: "legacy"}} {
		pr.WorkspaceID = "w"
		pr.PipelineID = "p"
		pr.PipelineSlug = "s"
		pr.FireAt = past
		if _, _, err := s.Enqueue(ctx, pr); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := s.DueRuns(ctx, time.Now(), 10)
	if err != nil {
		t.Fatal(err)
	}
	exec := &fakeExecutor{}
	d := NewPendingRunDispatcher(s, exec, nil)
	for _, pr := range rows {
		d.fireOne(ctx, pr)
	}
	if len(exec.seen) != 2 {
		t.Fatalf("runs=%d", len(exec.seen))
	}
	pins, legacy := 0, 0
	for _, in := range exec.seen {
		if in.PinnedVersion == nil {
			legacy++
		} else if *in.PinnedVersion == 3 {
			pins++
		}
	}
	if pins != 1 || legacy != 1 {
		t.Fatalf("pinned=%d legacy=%d", pins, legacy)
	}
}

// Coalescing after the due-list read must not dispatch the obsolete payload
// or ignore the newly postponed fire time.
func TestPendingDispatcherClaimsCurrentCoalescedState(t *testing.T) {
	for _, postpone := range []bool{false, true} {
		t.Run(fmt.Sprint(postpone), func(t *testing.T) {
			store := NewPendingRunStore(newPendingDB(t))
			first := PendingRun{ID: "first", WorkspaceID: "w", PipelineID: "p", PipelineSlug: "s", DebounceKey: "k", FireAt: time.Now().Add(-time.Minute), InputsJSON: `{"value":"old"}`, InvokingUserID: "alice"}
			if _, _, err := store.Enqueue(t.Context(), first); err != nil {
				t.Fatal(err)
			}
			due, err := store.DueRuns(t.Context(), time.Now(), 10)
			if err != nil || len(due) != 1 {
				t.Fatalf("due=%v err=%v", due, err)
			}
			second := first
			second.ID = "second"
			second.InputsJSON = `{"value":"new"}`
			second.InvokingUserID = "bob"
			if postpone {
				second.FireAt = time.Now().Add(time.Hour)
			}
			if _, _, err := store.Enqueue(t.Context(), second); err != nil {
				t.Fatal(err)
			}
			exec := &fakeExecutor{}
			d := NewPendingRunDispatcher(store, exec, nil)
			d.fireOne(t.Context(), due[0])
			if postpone {
				if len(exec.seen) != 0 {
					t.Fatal("dispatched a debounce window that was postponed after listing")
				}
				var status string
				if err := store.db.QueryRowContext(t.Context(), `SELECT status FROM pending_runs WHERE id='first'`).Scan(&status); err != nil {
					t.Fatal(err)
				}
				if status != "pending" {
					t.Fatalf("postponed row status=%s", status)
				}
			} else {
				if len(exec.seen) != 1 {
					t.Fatalf("dispatches=%d", len(exec.seen))
				}
				if got := exec.seen[0]; got.Inputs["value"] != "new" || got.InvokingUserID != "bob" {
					t.Fatalf("dispatched stale accepted state: %+v", got)
				}
			}
		})
	}
}
