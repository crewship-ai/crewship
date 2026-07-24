package pipeline

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
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

// TestRateLimiter_PrunesStaleWindows pins the #1416 nit: the window map
// was never pruned, so a token fired once and never reused left a
// permanent entry. Once the map crosses the prune threshold, allow() must
// opportunistically sweep entries whose window has long since expired.
func TestRateLimiter_PrunesStaleWindows(t *testing.T) {
	rl := &rateLimiter{windows: map[string]*rateWindow{}}
	// Seed past the prune threshold with long-stale, distinct-token
	// entries (never reused, exactly the one-off-webhook-token scenario).
	for i := 0; i < rateLimiterPruneThreshold+10; i++ {
		rl.windows[fmt.Sprintf("stale-%d", i)] = &rateWindow{
			startedAt: time.Now().Add(-2 * time.Hour),
			count:     1,
		}
	}
	before := len(rl.windows)

	// A single allow() call (any key) must trigger the sweep once the
	// threshold is crossed.
	rl.allow("fresh", 10)

	after := len(rl.windows)
	if after >= before {
		t.Fatalf("expected stale windows to be pruned: before=%d after=%d", before, after)
	}
	// Only the just-inserted fresh window (and nothing stale) should
	// remain.
	if after != 1 {
		t.Errorf("expected exactly 1 surviving window (the fresh one), got %d", after)
	}
	if _, ok := rl.windows["fresh"]; !ok {
		t.Error("the fresh window itself must survive its own insertion")
	}
}

// TestWebhook_ValidateTimestampedSignature pins #1416 item 2: pipeline
// webhooks gain the same timestamped ("ts.body") HMAC scheme + freshness
// window internal/webhook/handler.go already enforces for agent webhooks —
// closing the gap where a captured signed request could be replayed any
// time within the (up to 24h, Forget-reopenable) idempotency window.
func TestWebhook_ValidateTimestampedSignature(t *testing.T) {
	w := &Webhook{SigningSecret: "s3cr3t"}
	body := []byte(`{"hello":"world"}`)
	now := time.Unix(1_700_000_000, 0)

	sign := func(ts string) string {
		mac := hmac.New(sha256.New, []byte(w.SigningSecret))
		mac.Write([]byte(ts + "."))
		mac.Write(body)
		return hex.EncodeToString(mac.Sum(nil))
	}

	t.Run("fresh timestamp with correct signature validates", func(t *testing.T) {
		ts := strconv.FormatInt(now.Unix(), 10)
		if !w.ValidateTimestampedSignature(body, ts, sign(ts), now, 0) {
			t.Error("expected a fresh, correctly-signed request to validate")
		}
	})
	t.Run("stale timestamp is rejected even with a correct signature", func(t *testing.T) {
		staleTS := strconv.FormatInt(now.Add(-1*time.Hour).Unix(), 10)
		if w.ValidateTimestampedSignature(body, staleTS, sign(staleTS), now, 0) {
			t.Error("expected a stale timestamp to be rejected (replay defense)")
		}
	})
	t.Run("captured request replayed later fails once outside tolerance", func(t *testing.T) {
		ts := strconv.FormatInt(now.Unix(), 10)
		sig := sign(ts) // captured at time `now`
		later := now.Add(10 * time.Minute)
		if w.ValidateTimestampedSignature(body, ts, sig, later, 0) {
			t.Error("expected a replayed signature to fail once the tolerance window has passed")
		}
	})
	t.Run("wrong signature is rejected regardless of timestamp freshness", func(t *testing.T) {
		ts := strconv.FormatInt(now.Unix(), 10)
		if w.ValidateTimestampedSignature(body, ts, "0000", now, 0) {
			t.Error("expected an incorrect signature to be rejected")
		}
	})
	t.Run("empty signing secret never validates", func(t *testing.T) {
		bare := &Webhook{}
		ts := strconv.FormatInt(now.Unix(), 10)
		mac := hmac.New(sha256.New, []byte(""))
		mac.Write([]byte(ts + "."))
		mac.Write(body)
		sig := hex.EncodeToString(mac.Sum(nil))
		if bare.ValidateTimestampedSignature(body, ts, sig, now, 0) {
			t.Error("expected empty signing secret to never validate")
		}
	})
	t.Run("malformed timestamp is rejected", func(t *testing.T) {
		if w.ValidateTimestampedSignature(body, "not-a-number", sign("not-a-number"), now, 0) {
			t.Error("expected a malformed timestamp to be rejected")
		}
	})
}
