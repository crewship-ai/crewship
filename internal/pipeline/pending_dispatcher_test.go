package pipeline

import (
	"context"
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
	delay     time.Duration
	gate      <-chan struct{}
	inFlight  int32
	maxInWork int32
	completed int32
}

func (f *fakeExecutor) Run(ctx context.Context, in RunInput) (*RunResult, error) {
	cur := atomic.AddInt32(&f.inFlight, 1)
	for {
		hi := atomic.LoadInt32(&f.maxInWork)
		if cur <= hi || atomic.CompareAndSwapInt32(&f.maxInWork, hi, cur) {
			break
		}
	}
	switch {
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

// TestPendingDispatcher_ConcurrentDispatch: 6 co-due runs each sleeping
// 200ms must all start within ~one sweep, not drain serially at
// 6×200ms. This is the #834 throughput cliff regression guard.
func TestPendingDispatcher_ConcurrentDispatch(t *testing.T) {
	store := enqueueDue(t, 6)
	exec := &fakeExecutor{delay: 200 * time.Millisecond}
	d := NewPendingRunDispatcher(store, exec, nil)

	start := time.Now()
	d.Start(context.Background())
	if !waitFor(t, 3*time.Second, func() bool { return atomic.LoadInt32(&exec.completed) == 6 }) {
		t.Fatalf("only %d/6 runs completed", atomic.LoadInt32(&exec.completed))
	}
	elapsed := time.Since(start)
	d.Stop()

	// Serial would be ~1200ms; concurrent (pool ≥6) is ~200ms. Assert
	// well under the serial floor.
	if elapsed > 800*time.Millisecond {
		t.Fatalf("dispatch took %v, expected concurrent (~200ms), not serial (~1200ms)", elapsed)
	}
	if hw := atomic.LoadInt32(&exec.maxInWork); hw < 2 {
		t.Fatalf("expected overlapping runs, max concurrency was %d", hw)
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
