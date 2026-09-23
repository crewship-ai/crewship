package scheduler

import "sync"

// defaultMaxConcurrentDispatches bounds simultaneous scheduled fires (#1668).
//
// robfig/cron runs every due entry in its own goroutine. After the durable
// cutover this bounds simultaneous acceptance attempts against SQLite's one
// writer; the dispatcher separately bounds execution.
//
// Four keeps a burst moving without a thundering herd at the dedicated
// acceptance handle. It is not an execution limit or a second agent slot.
const defaultMaxConcurrentDispatches = 4

// initDispatchBound sizes the dispatch semaphore. Called from New; separate so
// tests can build a Scheduler by hand and still exercise the bound.
func (s *Scheduler) initDispatchBound(n int) {
	if n <= 0 {
		n = defaultMaxConcurrentDispatches
	}
	s.dispatchSem = make(chan struct{}, n)
}

// acquireDispatchSlot blocks until a scheduled acceptance may proceed. ok is false
// only when the scheduler is shutting down, in which case the caller must
// return without firing — a cron goroutine parked forever on a semaphore that
// will never be released is how a clean shutdown turns into a hang.
//
// The returned release is idempotent, because callers defer it on paths that
// also return early.
//
// A nil semaphore (a Scheduler built outside New) is a pass-through, so no
// existing construction path can deadlock on this.
func (s *Scheduler) acquireDispatchSlot() (release func(), ok bool) {
	if s.dispatchSem == nil {
		return func() {}, true
	}
	select {
	case s.dispatchSem <- struct{}{}:
		var once sync.Once
		return func() { once.Do(func() { <-s.dispatchSem }) }, true
	case <-s.ctx.Done():
		return func() {}, false
	}
}
