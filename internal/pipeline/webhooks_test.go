package pipeline

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestRateLimiter_NoLostUpdatesUnderConcurrency targets the bug fixed
// in the routines stabilization commit. The previous implementation
// took the mutex only for the windows[key] lookup and then ran
// counter.Add OUTSIDE the lock; under concurrent fires of the same
// token, two goroutines could both see an expired window, both install
// new windows, then both increment-against-stale counters, losing
// updates. With the fix (lock held through both window install AND
// counter increment), exactly `limit` calls return true and the rest
// return false.
func TestRateLimiter_NoLostUpdatesUnderConcurrency(t *testing.T) {
	rl := &rateLimiter{windows: map[string]*rateWindow{}}
	const (
		limit        = 100
		concurrency  = 50
		perGoroutine = 10 // 50*10 = 500 attempts; expect 100 allowed
	)

	var allowed atomic.Int64
	var wg sync.WaitGroup
	wg.Add(concurrency)
	start := make(chan struct{})
	for i := 0; i < concurrency; i++ {
		go func() {
			defer wg.Done()
			<-start
			for j := 0; j < perGoroutine; j++ {
				if rl.allow("token_x", limit) {
					allowed.Add(1)
				}
			}
		}()
	}
	close(start)
	wg.Wait()

	got := allowed.Load()
	// With the lock-during-increment fix, the count must be exactly
	// `limit`. Without the fix, this drifts above (lost increments)
	// or below (visibility races) — either is a bug.
	if got != int64(limit) {
		t.Errorf("rate limiter: expected exactly %d allowed under concurrent fire, got %d", limit, got)
	}
}

// TestRateLimiter_NewWindowAfterMinuteElapsed verifies the window
// cycles after time.Minute, allowing another `limit` calls. Uses a
// time-shifted window install so we don't actually wait 60s.
func TestRateLimiter_NewWindowAfterMinuteElapsed(t *testing.T) {
	rl := &rateLimiter{windows: map[string]*rateWindow{}}
	// Saturate limit
	for i := 0; i < 10; i++ {
		if !rl.allow("k", 10) {
			t.Fatalf("expected allow within limit, iter %d", i)
		}
	}
	if rl.allow("k", 10) {
		t.Fatal("expected reject above limit")
	}

	// Reach into the limiter and back-date the window so the next
	// allow() considers it expired and installs a fresh one.
	rl.mu.Lock()
	rl.windows["k"].startedAt = time.Now().Add(-2 * time.Minute)
	rl.mu.Unlock()

	if !rl.allow("k", 10) {
		t.Error("expected allow after window expiry installed fresh counter")
	}
}

// TestRateLimiter_ZeroLimitTreatedAsUnlimited verifies the documented
// behaviour: limit=0 bypasses the throttle, returning true unconditionally.
func TestRateLimiter_ZeroLimitTreatedAsUnlimited(t *testing.T) {
	rl := &rateLimiter{windows: map[string]*rateWindow{}}
	for i := 0; i < 100; i++ {
		if !rl.allow("k", 0) {
			t.Errorf("limit=0 must always allow, iter %d", i)
		}
	}
}
