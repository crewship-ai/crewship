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

func TestRateLimiter_NilLimiterNeverBlocks(t *testing.T) {
	var r *RateLimiter
	for i := 0; i < 100; i++ {
		if !r.Allow("u1", "c1", "security") {
			t.Fatal("a nil *RateLimiter must never block (rate limiting disabled)")
		}
	}
}
