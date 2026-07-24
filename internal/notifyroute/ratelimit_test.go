package notifyroute

import (
	"testing"
	"time"
)

func TestRateLimiter_Allow_TokenBucket(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	r := NewRateLimiter(3, 1) // capacity 3, refill 1 token/sec
	r.nowFunc = func() time.Time { return now }

	// Burst of 3 within capacity: all allowed.
	for i := 0; i < 3; i++ {
		if !r.Allow("u1", "c1", "security") {
			t.Fatalf("call %d within capacity should be allowed", i)
		}
	}
	// 4th immediate call exceeds capacity.
	if r.Allow("u1", "c1", "security") {
		t.Fatal("4th immediate call should be rate-limited")
	}
	// After 1s, one token refills.
	now = now.Add(1 * time.Second)
	if !r.Allow("u1", "c1", "security") {
		t.Fatal("after 1s refill, one more call should be allowed")
	}
	if r.Allow("u1", "c1", "security") {
		t.Fatal("bucket should be empty again immediately after consuming the refilled token")
	}
}

func TestRateLimiter_Allow_KeyedPerRecipientChannelCategory(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	r := NewRateLimiter(1, 0) // capacity 1, no refill — exhaust immediately
	r.nowFunc = func() time.Time { return now }

	if !r.Allow("u1", "c1", "security") {
		t.Fatal("first call for (u1,c1,security) should be allowed")
	}
	if r.Allow("u1", "c1", "security") {
		t.Fatal("second call for the SAME key should be rate-limited")
	}
	// Different recipient, channel, or category is an independent bucket.
	if !r.Allow("u2", "c1", "security") {
		t.Fatal("different recipient should have its own bucket")
	}
	if !r.Allow("u1", "c2", "security") {
		t.Fatal("different channel should have its own bucket")
	}
	if !r.Allow("u1", "c1", "budget") {
		t.Fatal("different category should have its own bucket")
	}
}

func TestRateLimiter_EvictsIdleBucketsLosslessly(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	r := NewRateLimiter(5, 1) // capacity 5, refill 1/s → full refill in 5s
	r.nowFunc = func() time.Time { return now }

	// Two buckets created now; both will go idle.
	r.Allow("u_idle1", "c", "security")
	r.Allow("u_idle2", "c", "security")
	if got := len(r.buckets); got != 2 {
		t.Fatalf("want 2 buckets seeded, got %d", got)
	}

	// Advance past the full-refill window, then touch a fresh key so it is
	// NOT idle at eviction time.
	now = now.Add(6 * time.Second)
	r.Allow("u_recent", "c", "security")

	r.mu.Lock()
	r.evictIdleLocked(now)
	kept := len(r.buckets)
	_, recentKept := r.buckets["u_recent\x00c\x00security"]
	r.mu.Unlock()

	if !recentKept {
		t.Error("the recently-touched bucket must survive eviction")
	}
	if kept != 1 {
		t.Errorf("idle buckets should be evicted: want 1 kept, got %d", kept)
	}

	// Lossless: an evicted key is recreated at full capacity, so a fresh
	// burst of `capacity` calls all succeed — exactly as if it were never
	// evicted.
	for i := 0; i < 5; i++ {
		if !r.Allow("u_idle1", "c", "security") {
			t.Fatalf("re-created bucket must start at full capacity; call %d denied", i)
		}
	}
}

// evictIdleLocked must never touch buckets that carry real state (not yet
// refilled), or eviction would silently reset someone's throttle.
func TestRateLimiter_EvictKeepsActiveBuckets(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	r := NewRateLimiter(5, 1) // full refill in 5s
	r.nowFunc = func() time.Time { return now }
	r.Allow("u", "c", "security") // consumes 1 → 4 tokens left, not full

	now = now.Add(2 * time.Second) // idle 2s < 5s full-refill window → must be kept
	r.mu.Lock()
	r.evictIdleLocked(now)
	_, kept := r.buckets["u\x00c\x00security"]
	r.mu.Unlock()
	if !kept {
		t.Error("a bucket idle for less than the full-refill window must not be evicted")
	}
}

func TestRateLimiter_NilLimiterNeverBlocks(t *testing.T) {
	var r *RateLimiter
	for i := 0; i < 100; i++ {
		if !r.Allow("u1", "c1", "security") {
			t.Fatal("a nil *RateLimiter must never block (rate limiting disabled)")
		}
	}
}
