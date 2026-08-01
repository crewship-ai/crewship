package api

import (
	"sync"
	"sync/atomic"
	"time"
)

// Detached background work started by request handlers, and the one
// place that can wait for it.
//
// Handlers in this package deliberately spawn goroutines that outlive
// the request: a terminal status write must survive the client
// disconnecting, a lesson write must not stall the response on a slow
// disk, a mail send must not hold the connection open. Each of those
// call sites derives a context with context.WithoutCancel and returns
// before the work finishes. That is correct for production and it is
// the root of #1596 in tests.
//
// In a test the request is not the outermost scope — the test is. A
// handler-spawned goroutine that is still running when the test body
// returns races that test's own teardown:
//
//   - it writes into a storagePath the test took from t.TempDir(), whose
//     cleanup contract is "if RemoveAll fails, FAIL the test" — 103
//     tests in this package pass a t.TempDir() to a handler
//   - it queries a *sql.DB the fixture is about to close, and the query
//     that has not started yet when Close() returns gets
//     "sql: database is closed" instead of doing its work
//
// Both surface as a test failing for something it did not do, in a
// family that shifts run to run, self-healing on re-run — the exact
// shape reported in #1596. internal/testutil/migrateddb.go already
// solved the DB-file half of this by refusing t.TempDir()'s
// fail-the-test cleanup contract for the database directory; that fixed
// where the fixture puts its own files and could not fix the work still
// in flight.
//
// This is the missing half: the work registers here, and the fixture
// waits for it before tearing anything down. The property restored is
// the one #1596 asks for — a test's teardown actually ends the work
// that test started.
//
// The package had already grown two bespoke versions of this
// (AuthHandler.mailWG behind WaitForPendingMail, and the escalation
// waiter) — one per site, each invented after that site flaked. A
// third would have been the wrong answer; the class needs one
// chokepoint, not another special case.
//
// Production cost is one atomic increment/decrement per detached
// goroutine and nothing else: nothing in crewshipd ever calls the wait,
// so the counter is written and never read outside tests.
//
// NOT a sync.WaitGroup, and this is not a style choice. A WaitGroup has
// generation semantics — "calls with a positive delta that start when
// the counter is zero must happen before a Wait" — and this counter has
// no generations at all: handlers add work continuously while test
// teardowns wait continuously, with nothing ordering "counter reached
// zero" against "next request starts work". That is precisely the
// interleaving the runtime refuses, and the first draft of this file
// got it wrong. Reproduced with the same shape (8 producers registering
// work, 8 waiters draining):
//
//	panic: sync: WaitGroup is reused before previous Wait has returned
//
// It panics the whole test BINARY, so it would have been a far worse
// flake than the one this file exists to remove — and internal/api runs
// 479 parallel tests, every one of which registers a drain. An atomic
// counter has no generations, so there is nothing to misuse.
var backgroundWorkInFlight atomic.Int64

// beginBackgroundWork registers one detached goroutine and returns the
// function that marks it finished. Call it on the REQUEST goroutine,
// before the `go` statement, and defer the returned func as the first
// line of the goroutine body:
//
//	finish := beginBackgroundWork()
//	go func() {
//		defer finish()
//		...
//	}()
//
// Registering before the spawn is what makes the count meaningful: a
// waiter that runs between `go` and the goroutine's first line would
// otherwise see zero outstanding work and return while the goroutine
// is about to start — which is precisely the window that produces
// "sql: database is closed" today.
func beginBackgroundWork() func() {
	backgroundWorkInFlight.Add(1)
	var once sync.Once
	return func() { once.Do(func() { backgroundWorkInFlight.Add(-1) }) }
}

// waitForBackgroundWork blocks until every goroutine registered with
// beginBackgroundWork has returned, and reports whether it drained
// within timeout.
//
// It returns false rather than blocking forever on purpose. A detached
// worker that genuinely hangs is a bug worth a loud, attributable test
// failure; converting it into a suite that stops producing output
// until CI's job cap fires would hide it. Callers in tests should fail
// the test on false and say which test was waiting.
func waitForBackgroundWork(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if backgroundWorkInFlight.Load() == 0 {
			return true
		}
		if !time.Now().Before(deadline) {
			return backgroundWorkInFlight.Load() == 0
		}
		time.Sleep(time.Millisecond)
	}
}
