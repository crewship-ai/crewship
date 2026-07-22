package api

import (
	"crypto/sha256"
	"sync"
	"time"
)

// The #1333 authenticated-CLI rate-limit exemption checks token validity
// BEFORE the per-IP limiter (routeWithRateLimiting), which is deliberate:
// exempt CLI traffic must not drain the per-IP bucket it shares with a
// browser on the same NAT. The flip side is a DoS-amplification surface —
// every request bearing a spoofed `crewship_cli_…`/`crewship_admin_…`
// bearer would force an unthrottled hash + DB lookup. cliExemptNegCache
// closes that: a small, bounded, TTL'd negative cache of token hashes whose
// lookup FAILED. A cache hit skips the DB entirely and the request falls
// through to the normal per-IP bucket.
//
// Only failures are cached. A positive result must never be — revocation
// and expiry have to take effect on the very next request, so valid tokens
// pay the (indexed, cheap) lookup every time. The worst a stale negative
// entry can do is keep a just-issued token inside the ordinary rate limit
// for up to cliExemptNegTTL, which is harmless.
const (
	cliExemptNegTTL = 30 * time.Second
	cliExemptNegMax = 1024
)

// cliExemptDBLookupHook is a test-only instrumentation point invoked each
// time the exemption path is about to run the real DB-backed token lookup.
// Tests swap in a counter to prove the negative cache collapses repeated
// spoofed-token lookups to one per TTL window. Nil (the default) in
// production; not synchronized, so only tests may set it.
var cliExemptDBLookupHook func()

// cliExemptNegCache is the bounded negative cache described above. Keys are
// SHA-256 of the presented bearer so the plaintext token never sits in
// memory beyond the request; values are entry expiry times.
type cliExemptNegCache struct {
	mu      sync.Mutex
	entries map[[sha256.Size]byte]time.Time
}

func newCLIExemptNegCache() *cliExemptNegCache {
	return &cliExemptNegCache{entries: make(map[[sha256.Size]byte]time.Time)}
}

// has reports whether key is a still-fresh known lookup failure. An expired
// entry is pruned on access and reported as a miss.
func (c *cliExemptNegCache) has(key [sha256.Size]byte, now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	exp, ok := c.entries[key]
	if !ok {
		return false
	}
	if now.After(exp) {
		delete(c.entries, key)
		return false
	}
	return true
}

// put records a failed lookup. At capacity it first sweeps expired entries;
// if the map is still full it evicts arbitrary entries (plain map-range
// eviction — the cache is an amplification damper, not an exact LRU, so
// dropping a random victim only costs one extra DB lookup for that token).
func (c *cliExemptNegCache) put(key [sha256.Size]byte, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) >= cliExemptNegMax {
		for k, exp := range c.entries {
			if now.After(exp) {
				delete(c.entries, k)
			}
		}
		for k := range c.entries {
			if len(c.entries) < cliExemptNegMax {
				break
			}
			delete(c.entries, k)
		}
	}
	c.entries[key] = now.Add(cliExemptNegTTL)
}
