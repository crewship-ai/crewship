package sidecar

import (
	"context"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestSSRFDialContext_PositiveDNSCache locks the #1081 hot-path optimization:
// a successful host→IP resolution is cached for a short TTL so a chatty agent
// making repeated cold dials to the same host does not pay a DNS lookup every
// time. Two dials to the same host within the TTL must consult the resolver
// exactly once.
func TestSSRFDialContext_PositiveDNSCache(t *testing.T) {
	var calls int64
	resolve := func(_ context.Context, _ string) ([]net.IPAddr, error) {
		atomic.AddInt64(&calls, 1)
		// TEST-NET-3 (203.0.113.0/24): public, not blocked, non-routable so the
		// dial itself fails fast under the short ctx deadline below.
		return []net.IPAddr{{IP: net.ParseIP("203.0.113.10")}}, nil
	}
	dial := ssrfDialContextWithResolver(false, resolve)
	for i := 0; i < 2; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		_, _ = dial(ctx, "tcp", "example.test:9") // connect fails/timeouts — irrelevant
		cancel()
	}
	if got := atomic.LoadInt64(&calls); got != 1 {
		t.Fatalf("resolver called %d times across 2 same-host dials, want 1 (positive DNS cache)", got)
	}
}

// TestSSRFDialContext_CacheStillValidatesBlocklist proves the cache stores only
// the resolution, never the block decision: a host that resolves to a blocked
// IP is rejected on EVERY dial, including a cache hit. The anti-rebind security
// property must survive the optimization.
func TestSSRFDialContext_CacheStillValidatesBlocklist(t *testing.T) {
	resolve := func(_ context.Context, _ string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("169.254.169.254")}}, nil // cloud metadata — always blocked
	}
	dial := ssrfDialContextWithResolver(false, resolve)
	for i := 0; i < 2; i++ { // second iteration is a cache hit
		_, err := dial(context.Background(), "tcp", "metadata.test:80")
		if err == nil || !strings.Contains(err.Error(), "refusing to dial blocked address") {
			t.Fatalf("dial %d: want SSRF block on cached resolution, got err=%v", i, err)
		}
	}
}

// TestSSRFDialContext_CacheExpiryReResolves confirms the positive cache honors
// its TTL — after expiry the resolver is consulted again (so a legitimately
// changed record is eventually picked up).
func TestSSRFDialContext_CacheExpiryReResolves(t *testing.T) {
	var calls int64
	resolve := func(_ context.Context, _ string) ([]net.IPAddr, error) {
		atomic.AddInt64(&calls, 1)
		return []net.IPAddr{{IP: net.ParseIP("203.0.113.11")}}, nil
	}
	cache := newDNSPositiveCache(10 * time.Millisecond)
	if _, err := cache.resolve(context.Background(), "h.test", resolve); err != nil {
		t.Fatal(err)
	}
	if _, err := cache.resolve(context.Background(), "h.test", resolve); err != nil { // hit
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)                                                 // let the entry expire
	if _, err := cache.resolve(context.Background(), "h.test", resolve); err != nil { // miss again
		t.Fatal(err)
	}
	if got := atomic.LoadInt64(&calls); got != 2 {
		t.Fatalf("resolver called %d times (hit + expiry), want 2", got)
	}
}
