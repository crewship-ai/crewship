package notifyroute

import (
	"sync"
	"time"
)

// RateLimiter is an in-process token bucket per (recipient, channel,
// category) — the anti-storm gate the design calls for, following the
// bounded-cap pattern internal/pipeline's perRunNotifyCap already
// established for a related problem (a run flooding one recipient's
// inbox). Overflow is a SOFT drop: the caller records a dropped_rate
// delivery-log row rather than failing anything.
//
// In-memory and per-process by design (issue #1412 MVP): the persistent
// piece the design requires is the delivery LOG (so retries survive a
// restart), not the bucket state itself — a process restart resetting
// everyone's burst allowance is an acceptable, conservative failure mode
// (worst case: a short window of no throttling right after boot, not
// under-delivery).
type RateLimiter struct {
	mu       sync.Mutex
	buckets  map[string]*bucket
	capacity float64
	refillPS float64 // tokens added per second
	nowFunc  func() time.Time
}

type bucket struct {
	tokens     float64
	lastRefill time.Time
}

// NewRateLimiter builds a limiter with the given per-key capacity and
// refill rate (tokens/second). Defaults (used by the production wiring):
// capacity 5, refill 1 token per 30s — a recipient can absorb a burst of 5
// notifications on one (channel, category) pair, then settles to 1 every
// 30s, generous enough for legitimate chat-reply bursts while still
// capping a misconfigured notify-step-in-a-loop routine.
func NewRateLimiter(capacity, refillPerSecond float64) *RateLimiter {
	return &RateLimiter{
		buckets:  make(map[string]*bucket),
		capacity: capacity,
		refillPS: refillPerSecond,
		nowFunc:  time.Now,
	}
}

// Allow reports whether a delivery to (recipientUserID, channelID,
// category) may proceed right now, consuming one token if so. Category is
// part of the key so a chatty chat.replies stream can't starve out a rare
// budget alert to the same channel.
func (r *RateLimiter) Allow(recipientUserID, channelID, category string) bool {
	if r == nil {
		return true // rate limiting disabled (nil limiter) never blocks
	}
	key := recipientUserID + "\x00" + channelID + "\x00" + category
	now := r.nowFunc()

	r.mu.Lock()
	defer r.mu.Unlock()
	b, ok := r.buckets[key]
	if !ok {
		b = &bucket{tokens: r.capacity, lastRefill: now}
		r.buckets[key] = b
	}
	elapsed := now.Sub(b.lastRefill).Seconds()
	if elapsed > 0 {
		b.tokens += elapsed * r.refillPS
		if b.tokens > r.capacity {
			b.tokens = r.capacity
		}
		b.lastRefill = now
	}
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}
