package sidecar

import (
	"log/slog"
	"sync"
	"time"
)

// memoryExecutor runs post-write side effects (FTS reindex + the
// memory.updated journal emit) off the request hot path so a slow
// reindex never delays the 201 the agent is waiting on.
//
// Design — deliberately the mirror image of the MCP audit channel
// (mcp_gateway.go) with three intentional differences:
//
//   - cap=1, not 64. The post-write work is heavy (SQLite reindex +
//     an IPC round-trip) and a deep buffer would let many turns'
//     reindexes pile up behind one slow tick, blowing the memory
//     budget and the ordering guarantee. cap=1 keeps at most one task
//     queued behind the one in flight.
//   - ordered + no-drop. The audit channel drops on a full buffer
//     because a missing audit line is tolerable. A dropped reindex is
//     NOT: the search index would silently diverge from disk. So
//     submit blocks the caller briefly (only ever behind a single
//     queued task) rather than dropping. Strict FIFO across a single
//     worker means turn N's reindex always lands before turn N+1's.
//   - Flush barrier. Shutdown (and tests) need a way to wait for the
//     queue to drain to a known-good point; Close adds a bounded drain
//     on top so teardown can't hang on a wedged reindex.
//
// The executor is intentionally task-agnostic — it runs func() values.
// The write handler closes over the engine + journal emit so the
// executor has no dependency on the memory package, which keeps it
// trivially unit-testable with synthetic slow/ordered tasks.
type memoryExecutor struct {
	tasks  chan func()
	logger *slog.Logger

	// done is closed when the worker goroutine has fully exited.
	done chan struct{}

	// mu guards closed. The tasks channel is NEVER closed (closing it
	// would race a concurrent submit's send and panic); shutdown is
	// signalled purely via closeReq. The worker drains whatever is
	// buffered after closeReq fires, so no enqueued task is lost.
	mu       sync.Mutex
	closed   bool
	closeReq chan struct{} // closed by Close to signal shutdown
}

// memoryExecutorQueueCap is the buffer depth. 1 means: one task in
// flight on the worker plus at most one queued. See the type doc for
// why this is intentionally tiny.
const memoryExecutorQueueCap = 1

// newMemoryExecutor starts the single worker goroutine and returns a
// ready executor. The caller owns it and must call Close on teardown.
func newMemoryExecutor(logger *slog.Logger) *memoryExecutor {
	if logger == nil {
		logger = slog.Default()
	}
	ex := &memoryExecutor{
		tasks:    make(chan func(), memoryExecutorQueueCap),
		logger:   logger,
		done:     make(chan struct{}),
		closeReq: make(chan struct{}),
	}
	go ex.run()
	return ex
}

// run is the single worker. Strict FIFO: it pulls one task at a time
// and runs it to completion before pulling the next, so ordering is
// preserved with zero locking on the hot path.
//
// Shutdown: once closeReq fires, the worker drains every task still
// buffered in tasks (no-drop on teardown) and then exits. A task that
// happens to be sent concurrently with the close lands in the buffer
// and is caught by the drain loop, so the close signal never races a
// send into a lost task.
func (ex *memoryExecutor) run() {
	defer close(ex.done)
	for {
		select {
		case task := <-ex.tasks:
			ex.runOne(task)
		case <-ex.closeReq:
			// Drain whatever is buffered, then exit. cap=1 means at
			// most one task remains, but loop to be robust.
			for {
				select {
				case task := <-ex.tasks:
					ex.runOne(task)
				default:
					return
				}
			}
		}
	}
}

// runOne executes a single task, recovering from panics so one bad
// reindex closure can't tear down the worker (which would silently
// stop all future reindexing for the life of the container).
func (ex *memoryExecutor) runOne(task func()) {
	defer func() {
		if rec := recover(); rec != nil {
			ex.logger.Error("memory executor task panicked", "panic", rec)
		}
	}()
	task()
}

// submit enqueues a task for the worker. It returns false if the
// executor has been closed (fail-closed: a dead worker must not silently
// swallow work it will never run). On a live executor submit blocks only
// while the single-slot buffer is full behind the in-flight task — never
// longer than one task's runtime — which is the no-drop guarantee.
func (ex *memoryExecutor) submit(task func()) bool {
	if task == nil {
		return false
	}
	ex.mu.Lock()
	if ex.closed {
		ex.mu.Unlock()
		return false
	}
	ex.mu.Unlock()

	// Select on closeReq so a submit racing Close returns false instead
	// of blocking forever on a full buffer that will never drain. tasks
	// is never closed, so the send can't panic; a send that wins the
	// race lands in the buffer and the worker's drain loop still runs it.
	select {
	case <-ex.closeReq:
		return false
	case ex.tasks <- task:
		return true
	}
}

// Flush blocks until the worker has drained every task queued at the
// moment of the call, or until timeout elapses. Returns true if the
// barrier completed, false on timeout. It does NOT close the executor —
// further submits are still accepted after a successful Flush.
//
// Implementation: enqueue a sentinel that closes a local channel once
// the worker reaches it. Because the worker is strict FIFO, the sentinel
// running means everything submitted before it has already run.
func (ex *memoryExecutor) Flush(timeout time.Duration) bool {
	deadline := time.After(timeout)

	barrier := make(chan struct{})
	sentinel := func() { close(barrier) }

	// On a live executor the sentinel rides the FIFO queue: when it runs,
	// every task submitted before it has already run. If the executor is
	// closed (or races into Close so the submit fails), submit returns
	// false and we fall back to the worker's done signal — once the
	// worker has fully drained and exited, the barrier is trivially
	// satisfied. Both paths then wait on the same deadline.
	if !ex.submit(sentinel) {
		select {
		case <-ex.done:
			return true
		case <-deadline:
			return false
		}
	}

	// Wait on the sentinel OR the worker exiting. done covers the case
	// where Close drained the sentinel via its teardown drain loop and
	// the worker exited before the barrier select observed it.
	select {
	case <-barrier:
		return true
	case <-ex.done:
		return true
	case <-deadline:
		return false
	}
}

// Close stops accepting new work and performs a bounded drain of the
// already-queued tasks before the worker exits. Returns true if the
// worker fully drained and exited within timeout, false if the timeout
// fired first (a wedged reindex) — in which case teardown proceeds
// anyway so the container shutdown is never blocked indefinitely.
//
// Close is idempotent and safe to call from multiple goroutines.
func (ex *memoryExecutor) Close(timeout time.Duration) bool {
	ex.mu.Lock()
	if ex.closed {
		ex.mu.Unlock()
		// Already closing/closed; just wait on the existing drain.
		select {
		case <-ex.done:
			return true
		case <-time.After(timeout):
			return false
		}
	}
	ex.closed = true
	close(ex.closeReq) // signal the worker to drain buffered tasks and exit
	ex.mu.Unlock()

	select {
	case <-ex.done:
		return true
	case <-time.After(timeout):
		return false
	}
}
