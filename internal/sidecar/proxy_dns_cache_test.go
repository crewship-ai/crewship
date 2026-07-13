package sidecar

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http/httptest"
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

// TestDNSPositiveCache_BoundedByCap locks the #1139 review fix: in free
// network mode the agent picks the hostnames the sidecar resolves (e.g. a
// wildcard-DNS domain — a distinct hostname per request), so an unbounded
// cache map is a slow OOM of the credential-holding sidecar process. A long
// TTL keeps every entry "live" for the whole test, isolating the hard cap
// from the separate expired-entry eviction behavior.
func TestDNSPositiveCache_BoundedByCap(t *testing.T) {
	cache := newDNSPositiveCache(time.Minute)
	resolve := func(_ context.Context, _ string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("203.0.113.1")}}, nil
	}

	const distinctHosts = dnsCacheMaxEntries * 3
	for i := 0; i < distinctHosts; i++ {
		host := fmt.Sprintf("wildcard-%d.attacker.test", i)
		if _, err := cache.resolve(context.Background(), host, resolve); err != nil {
			t.Fatalf("resolve %d: %v", i, err)
		}
	}

	cache.mu.Lock()
	size := len(cache.m)
	cache.mu.Unlock()
	if size > dnsCacheMaxEntries {
		t.Fatalf("cache holds %d entries after %d distinct hosts, want <= %d (hard cap)", size, distinctHosts, dnsCacheMaxEntries)
	}
}

// TestDNSPositiveCache_EvictsExpiredEntriesOnInsert locks the other half of
// the #1139 review fix: a full cache reclaims already-expired entries before
// falling back to evicting a still-live one. Fill to the cap with a very
// short TTL, let everything expire, then insert one more distinct host — it
// must fit by reclaiming the dead entries rather than the insert silently
// evicting into an artificially permanent steady-state at the cap.
func TestDNSPositiveCache_EvictsExpiredEntriesOnInsert(t *testing.T) {
	cache := newDNSPositiveCache(5 * time.Millisecond)
	resolve := func(_ context.Context, _ string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("203.0.113.2")}}, nil
	}

	for i := 0; i < dnsCacheMaxEntries; i++ {
		host := fmt.Sprintf("expiring-%d.test", i)
		if _, err := cache.resolve(context.Background(), host, resolve); err != nil {
			t.Fatalf("resolve %d: %v", i, err)
		}
	}
	time.Sleep(20 * time.Millisecond) // every entry above is now expired

	if _, err := cache.resolve(context.Background(), "fresh.test", resolve); err != nil {
		t.Fatalf("resolve fresh host: %v", err)
	}

	cache.mu.Lock()
	defer cache.mu.Unlock()
	if _, ok := cache.m["fresh.test"]; !ok {
		t.Fatal("fresh entry missing after insert — expired entries should have been evicted to make room")
	}
	if len(cache.m) >= dnsCacheMaxEntries {
		t.Errorf("cache still holds %d entries right after an expiry sweep, want far fewer (expired entries not evicted on insert)", len(cache.m))
	}
}

// TestHandleConnect_SharesDNSCacheAcrossRequests locks the #1139 review fix
// for handleConnect: before the fix, each CONNECT built its own fresh
// dnsPositiveCache (via ssrfDialContext), so the cache never got a hit on the
// HTTPS-tunnel path. Two CONNECTs to the same host within the TTL must
// consult the resolver exactly once when the cache is actually shared.
func TestHandleConnect_SharesDNSCacheAcrossRequests(t *testing.T) {
	echoLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echoLn.Close()
	go func() {
		for {
			conn, err := echoLn.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				buf := make([]byte, 64)
				n, _ := c.Read(buf)
				if n > 0 {
					c.Write(buf[:n])
				}
			}(conn)
		}
	}()

	var lookups int64
	proxy := NewProxy(ProxyConfig{
		CredStore: NewCredStore(),
		Allowlist: NewDomainAllowlist(nil),
		Logger:    covLogger(),
		FreeMode:  true,
	})
	// Wrap the real resolver so we can count lookups without needing the
	// dialed IP to be anything other than the real loopback echo listener.
	proxy.dnsResolve = func(ctx context.Context, host string) ([]net.IPAddr, error) {
		atomic.AddInt64(&lookups, 1)
		return net.DefaultResolver.LookupIPAddr(ctx, host)
	}

	proxySrv := httptest.NewServer(proxy)
	defer proxySrv.Close()

	target := echoLn.Addr().String()
	for i := 0; i < 2; i++ {
		conn, err := net.Dial("tcp", strings.TrimPrefix(proxySrv.URL, "http://"))
		if err != nil {
			t.Fatalf("dial %d: %v", i, err)
		}
		conn.SetDeadline(time.Now().Add(10 * time.Second))
		fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target)

		br := bufio.NewReader(conn)
		statusLine, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("CONNECT %d: read status line: %v", i, err)
		}
		if !strings.HasPrefix(statusLine, "HTTP/1.1 200") {
			t.Fatalf("CONNECT %d status line = %q, want HTTP/1.1 200", i, statusLine)
		}
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				t.Fatalf("CONNECT %d: read headers: %v", i, err)
			}
			if line == "\r\n" || line == "\n" {
				break
			}
		}
		conn.Close()
	}

	if got := atomic.LoadInt64(&lookups); got != 1 {
		t.Fatalf("resolver invoked %d times across 2 CONNECTs to the same host, want 1 (shared DNS cache across the CONNECT path)", got)
	}
}
