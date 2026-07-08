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
type fakeExecutor struct {
	delay     time.Duration
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
	if f.delay > 0 {
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
func TestPendingDispatcher_StopDrainsInFlight(t *testing.T) {
	store := enqueueDue(t, 4)
	exec := &fakeExecutor{delay: 150 * time.Millisecond}
	d := NewPendingRunDispatcher(store, exec, nil)

	d.Start(context.Background())
	// Give the sweep a beat to spawn the dispatch goroutines.
	waitFor(t, time.Second, func() bool { return atomic.LoadInt32(&exec.inFlight) > 0 })
	d.Stop() // must not return until in-flight runs complete

	if got := atomic.LoadInt32(&exec.inFlight); got != 0 {
		t.Fatalf("Stop returned with %d runs still in flight", got)
	}
	if got := atomic.LoadInt32(&exec.completed); got != 4 {
		t.Fatalf("expected 4 completed after Stop drain, got %d", got)
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
