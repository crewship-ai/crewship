package notifyroute

import "testing"

// TestDynamicRateLimiter_ReadsLiveCapacity proves the runtime-tunable notify
// limiter picks up a capacity change on the very next Allow — the property the
// admin Rate Limiters console relies on to retune notifications without a
// restart.
func TestDynamicRateLimiter_ReadsLiveCapacity(t *testing.T) {
	capacity := 2.0
	// refill 0 keeps the test deterministic: no tokens come back between calls,
	// so the only thing that changes the verdict is the live capacity read.
	rl := NewDynamicRateLimiter(func() float64 { return capacity }, func() float64 { return 0 })

	// With capacity 2, the first two deliveries pass and the third is dropped.
	// (Separate statements, not `a || a`: each call consumes a token, so the
	// two reads are NOT the redundant expression staticcheck's SA4000 assumes.)
	if !rl.Allow("u", "c", "cat") {
		t.Fatal("first delivery should pass at capacity 2")
	}
	if !rl.Allow("u", "c", "cat") {
		t.Fatal("second delivery should pass at capacity 2")
	}
	if rl.Allow("u", "c", "cat") {
		t.Fatal("third delivery should be throttled at capacity 2")
	}

	// Raise the capacity at runtime. A brand-new key must now absorb a larger
	// burst — proof the limiter re-read the function rather than freezing the
	// seed value.
	capacity = 5.0
	for i := 0; i < 5; i++ {
		if !rl.Allow("u2", "c", "cat") {
			t.Fatalf("delivery %d should pass after raising capacity to 5", i+1)
		}
	}
	if rl.Allow("u2", "c", "cat") {
		t.Fatal("delivery past the raised capacity should be throttled")
	}
}
