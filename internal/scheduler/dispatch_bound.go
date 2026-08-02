package scheduler

import "sync"

// defaultMaxConcurrentDispatches bounds simultaneous scheduled fires (#1668).
//
// There was no bound at all. robfig/cron runs every due entry in its own
// goroutine, so twenty crews sharing a cron minute produced twenty
// simultaneous fires — twenty chats created, twenty 45-minute contexts, and
// twenty EnsureCrewRuntime calls landing on the daemon in the same
// millisecond.
//
// This is NOT a second copy of the container-start bound. Admission control
// (internal/admission, consulted inside the providers) already caps and
// staggers the container starts themselves, and it does so across every wake
// source rather than only cron. What this bounds is the scheduler's own
// fan-out: the chat rows, the resolver round-trips, and the goroutines that
// hold a 45-minute context each. Four keeps a burst of due schedules moving
// while leaving the daemon's exec capacity for interactive work.
const defaultMaxConcurrentDispatches = 4

// initDispatchBound sizes the dispatch semaphore. Called from New; separate so
// tests can build a Scheduler by hand and still exercise the bound.
func (s *Scheduler) initDispatchBound(n int) {
	if n <= 0 {
		n = defaultMaxConcurrentDispatches
	}
	s.dispatchSem = make(chan struct{}, n)
}

// acquireDispatchSlot blocks until a scheduled fire may proceed. ok is false
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
