package sidecar

import (
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func newExecTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// TestMemoryExecutor_SubmitDoesNotBlock verifies a slow task does not block
// the submitting goroutine: submit must return well before the task finishes.
func TestMemoryExecutor_SubmitDoesNotBlock(t *testing.T) {
	ex := newMemoryExecutor(newExecTestLogger())
	defer ex.Close(time.Second)

	release := make(chan struct{})
	started := make(chan struct{})
	t0 := time.Now()
	ok := ex.submit(func() {
		close(started)
		<-release // hold the worker until the test releases it
	})
	if !ok {
		t.Fatalf("submit returned false, want true")
	}
	elapsed := time.Since(t0)
	if elapsed > 200*time.Millisecond {
		t.Fatalf("submit blocked for %v, expected near-instant return", elapsed)
	}
	<-started // confirm the task actually ran
	close(release)
}

// TestMemoryExecutor_Ordering verifies strict FIFO: task N runs to completion
// before task N+1 starts (single worker, cap=1, no reordering).
func TestMemoryExecutor_Ordering(t *testing.T) {
	ex := newMemoryExecutor(newExecTestLogger())

	const n = 50
	var mu sync.Mutex
	order := make([]int, 0, n)
	for i := 0; i < n; i++ {
		i := i
		if !ex.submit(func() {
			mu.Lock()
			order = append(order, i)
			mu.Unlock()
		}) {
			t.Fatalf("submit %d returned false", i)
		}
	}
	if !ex.Flush(2 * time.Second) {
		t.Fatalf("Flush timed out")
	}
	ex.Close(time.Second)

	mu.Lock()
	defer mu.Unlock()
	if len(order) != n {
		t.Fatalf("ran %d tasks, want %d", len(order), n)
	}
	for i, v := range order {
		if v != i {
			t.Fatalf("out-of-order at index %d: got %d", i, v)
		}
	}
}

// TestMemoryExecutor_FlushBarrier verifies Flush blocks until all queued work
// has drained, then returns true.
func TestMemoryExecutor_FlushBarrier(t *testing.T) {
	ex := newMemoryExecutor(newExecTestLogger())
	defer ex.Close(time.Second)

	var done atomic.Int32
	for i := 0; i < 10; i++ {
		ex.submit(func() {
			time.Sleep(5 * time.Millisecond)
			done.Add(1)
		})
	}
	if !ex.Flush(2 * time.Second) {
		t.Fatalf("Flush timed out")
	}
	if got := done.Load(); got != 10 {
		t.Fatalf("after Flush, completed = %d, want 10", got)
	}
}

// TestMemoryExecutor_FlushTimeout verifies Flush returns false when work
// does not drain within the timeout, without losing the in-flight task.
func TestMemoryExecutor_FlushTimeout(t *testing.T) {
	ex := newMemoryExecutor(newExecTestLogger())

	release := make(chan struct{})
	ex.submit(func() { <-release })
	if ex.Flush(50 * time.Millisecond) {
		t.Fatalf("Flush returned true, want false (task still blocked)")
	}
	close(release)
	if !ex.Flush(2 * time.Second) {
		t.Fatalf("Flush after release timed out")
	}
	ex.Close(time.Second)
}

// TestMemoryExecutor_BoundedDrainOnClose verifies Close drains the already
// queued tasks (bounded by timeout) before the worker exits.
func TestMemoryExecutor_BoundedDrainOnClose(t *testing.T) {
	ex := newMemoryExecutor(newExecTestLogger())

	var ran atomic.Int32
	for i := 0; i < 5; i++ {
		ex.submit(func() {
			time.Sleep(2 * time.Millisecond)
			ran.Add(1)
		})
	}
	// Close with a generous timeout should drain all queued work.
	if !ex.Close(2 * time.Second) {
		t.Fatalf("Close timed out before drain completed")
	}
	if got := ran.Load(); got != 5 {
		t.Fatalf("drained %d tasks on close, want 5", got)
	}
}

// TestMemoryExecutor_SubmitAfterClose verifies submit fails closed once the
// executor has been shut down (no-drop guarantee does not resurrect a dead
// worker).
func TestMemoryExecutor_SubmitAfterClose(t *testing.T) {
	ex := newMemoryExecutor(newExecTestLogger())
	ex.Close(time.Second)
	if ex.submit(func() {}) {
		t.Fatalf("submit returned true after Close, want false")
	}
}

// TestMemoryExecutor_CloseIdempotent verifies Close can be called twice safely.
func TestMemoryExecutor_CloseIdempotent(t *testing.T) {
	ex := newMemoryExecutor(newExecTestLogger())
	ex.submit(func() {})
	if !ex.Close(time.Second) {
		t.Fatalf("first Close timed out")
	}
	// Second Close is a no-op and must not panic or block.
	if !ex.Close(time.Second) {
		t.Fatalf("second Close returned false")
	}
}

// TestMemoryExecutor_NilLoggerDefaulted verifies the constructor tolerates a
// nil logger by falling back to slog.Default rather than panicking on first use.
func TestMemoryExecutor_NilLogger(t *testing.T) {
	ex := newMemoryExecutor(nil)
	defer ex.Close(time.Second)
	done := make(chan struct{})
	if !ex.submit(func() { close(done) }) {
		t.Fatalf("submit returned false")
	}
	<-done
}

// TestMemoryExecutor_NilTaskRejected verifies submit refuses a nil task.
func TestMemoryExecutor_NilTaskRejected(t *testing.T) {
	ex := newMemoryExecutor(newExecTestLogger())
	defer ex.Close(time.Second)
	if ex.submit(nil) {
		t.Fatalf("submit(nil) returned true, want false")
	}
}

// TestMemoryExecutor_PanicRecovered verifies a panicking task does not kill the
// worker: a subsequent task still runs.
func TestMemoryExecutor_PanicRecovered(t *testing.T) {
	ex := newMemoryExecutor(newExecTestLogger())

	if !ex.submit(func() { panic("boom") }) {
		t.Fatalf("submit panic task returned false")
	}
	survived := make(chan struct{})
	if !ex.submit(func() { close(survived) }) {
		t.Fatalf("submit follow-up task returned false")
	}
	select {
	case <-survived:
	case <-time.After(2 * time.Second):
		t.Fatalf("worker died after panic; follow-up task never ran")
	}
	ex.Close(time.Second)
}

// TestMemoryExecutor_FlushAfterClose verifies Flush uses the done-signal
// barrier once the executor is closed, returning true after the drain.
func TestMemoryExecutor_FlushAfterClose(t *testing.T) {
	ex := newMemoryExecutor(newExecTestLogger())
	var ran atomic.Int32
	ex.submit(func() { ran.Add(1) })
	// Close in the background-ish: close triggers drain; then Flush on a
	// closed executor must observe the done barrier.
	go ex.Close(2 * time.Second)
	if !ex.Flush(2 * time.Second) {
		t.Fatalf("Flush after close timed out")
	}
	if ran.Load() != 1 {
		t.Fatalf("queued task did not run before drain barrier")
	}
}

// TestMemoryExecutor_FlushAfterClose_Timeout verifies the closed-path Flush
// honours its timeout when the drain is wedged.
func TestMemoryExecutor_FlushAfterClose_Timeout(t *testing.T) {
	ex := newMemoryExecutor(newExecTestLogger())
	release := make(chan struct{})
	ex.submit(func() { <-release })
	go ex.Close(5 * time.Second)
	// Worker is blocked on the wedged task, so the closed-path Flush must
	// time out rather than block forever.
	if ex.Flush(50 * time.Millisecond) {
		t.Fatalf("Flush returned true on wedged drain, want false")
	}
	close(release)
}

// TestMemoryExecutor_FlushOnFullyClosed verifies Flush returns true via the
// done-signal barrier once the executor is fully closed and drained (submit
// fails, done already closed).
func TestMemoryExecutor_FlushOnFullyClosed(t *testing.T) {
	ex := newMemoryExecutor(newExecTestLogger())
	var ran atomic.Int32
	ex.submit(func() { ran.Add(1) })
	if !ex.Close(2 * time.Second) {
		t.Fatalf("Close timed out")
	}
	// Worker has exited; submit fails, done is closed → Flush true.
	if !ex.Flush(time.Second) {
		t.Fatalf("Flush on fully-closed executor returned false")
	}
	if ran.Load() != 1 {
		t.Fatalf("queued task did not run before close drained")
	}
}

// TestMemoryExecutor_FlushTimeoutOnClosedWedged verifies the closed-path Flush
// honours the timeout when the worker never exits (wedged drain), returning
// false rather than blocking on done forever.
func TestMemoryExecutor_FlushTimeoutOnClosedWedged(t *testing.T) {
	ex := newMemoryExecutor(newExecTestLogger())
	release := make(chan struct{})
	ex.submit(func() { <-release })
	// Close in the background; the worker is wedged so it won't exit.
	go ex.Close(5 * time.Second)
	// Give Close time to flip closed=true so submit fails and Flush takes
	// the done-barrier path, which must then time out on the wedge.
	deadline := time.Now().Add(time.Second)
	for {
		if ex.submit(func() {}) == false {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("executor never reported closed")
		}
	}
	if ex.Flush(50 * time.Millisecond) {
		t.Fatalf("Flush returned true on wedged closed drain, want false")
	}
	close(release)
}

// TestMemoryExecutor_CloseAlreadyClosedTimeout verifies a second Close that
// observes an already-wedged drain is bounded by its own timeout.
func TestMemoryExecutor_CloseAlreadyClosedTimeout(t *testing.T) {
	ex := newMemoryExecutor(newExecTestLogger())
	release := make(chan struct{})
	ex.submit(func() { <-release })
	go ex.Close(5 * time.Second) // first Close, wedged
	// Spin until closed flips, then a second Close hits the idempotent
	// branch and must time out (worker still wedged).
	deadline := time.Now().Add(time.Second)
	for ex.submit(func() {}) {
		if time.Now().After(deadline) {
			t.Fatalf("executor never reported closed")
		}
	}
	if ex.Close(50 * time.Millisecond) {
		t.Fatalf("second Close returned true on wedged drain, want false")
	}
	close(release)
}

// TestMemoryExecutor_CloseTimeoutBounded verifies Close returns false (bounded)
// when a task refuses to finish, rather than hanging forever on teardown.
func TestMemoryExecutor_CloseTimeoutBounded(t *testing.T) {
	ex := newMemoryExecutor(newExecTestLogger())
	release := make(chan struct{})
	ex.submit(func() { <-release })
	t0 := time.Now()
	if ex.Close(50 * time.Millisecond) {
		t.Fatalf("Close returned true, want false (task hung)")
	}
	if time.Since(t0) > 500*time.Millisecond {
		t.Fatalf("Close was not bounded by timeout")
	}
	close(release)
}
