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

// evictThreshold is the bucket-count at which Allow opportunistically
// sweeps fully-refilled (idle) buckets. Without this the map grows one
// entry per (recipient, channel, category) seen since boot and never
// shrinks — an unbounded leak on a workspace with churny recipients or
// many channels. A bucket idle long enough to have refilled to capacity
// is indistinguishable from a freshly-created one, so dropping it is
// lossless: the next Allow for that key recreates it at full capacity.
const evictThreshold = 2048

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
		if len(r.buckets) >= evictThreshold {
			r.evictIdleLocked(now)
		}
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

// evictIdleLocked drops every bucket that has been idle long enough to
// have refilled to full capacity. Such a bucket is byte-for-byte
// equivalent to a fresh one, so eviction never changes an allow/deny
// decision — it only reclaims memory. Caller must hold r.mu.
func (r *RateLimiter) evictIdleLocked(now time.Time) {
	if r.refillPS <= 0 {
		return // no refill → buckets never return to capacity; keep them
	}
	fullRefillSecs := r.capacity / r.refillPS // empty → full
	for k, b := range r.buckets {
		if now.Sub(b.lastRefill).Seconds() >= fullRefillSecs {
			delete(r.buckets, k)
		}
	}
}
