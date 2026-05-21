package api

import (
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRateLimiter_AllowsWithinLimit(t *testing.T) {
	rl := NewRateLimiter(10) // 10 req/min
	handler := rl.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// First 10 requests should succeed (burst = 10)
	for i := range 10 {
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		req.RemoteAddr = "192.168.1.1:12345"
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code, "request %d should succeed", i+1)
	}
}

func TestRateLimiter_BlocksOverLimit(t *testing.T) {
	rl := NewRateLimiter(5) // 5 req/min, burst = 5
	handler := rl.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// Exhaust burst
	for range 5 {
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		req.RemoteAddr = "10.0.0.1:12345"
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code)
	}

	// Next request should be rate limited
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.RemoteAddr = "10.0.0.1:12345"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusTooManyRequests, rec.Code)
	assert.Equal(t, "60", rec.Header().Get("Retry-After"))
	// X-RateLimit-* trio (de-facto convention, audit M8). Clients use
	// these to back off intelligently rather than retry blindly.
	assert.Equal(t, "5", rec.Header().Get("X-RateLimit-Limit"),
		"X-RateLimit-Limit should reflect the burst capacity")
	assert.Equal(t, "0", rec.Header().Get("X-RateLimit-Remaining"),
		"X-RateLimit-Remaining must be 0 when limiter blocks")
	// X-RateLimit-Reset is a unix timestamp; just assert it parses and
	// lands in a plausible future window (now + Retry-After ± 5s).
	resetStr := rec.Header().Get("X-RateLimit-Reset")
	require.NotEmpty(t, resetStr, "X-RateLimit-Reset header must be present")
	reset, err := strconv.ParseInt(resetStr, 10, 64)
	require.NoError(t, err, "X-RateLimit-Reset must be a parseable unix timestamp")
	delta := reset - time.Now().Unix()
	assert.GreaterOrEqual(t, delta, int64(55), "Reset should be ~60s in the future")
	assert.LessOrEqual(t, delta, int64(65), "Reset should be ~60s in the future")
}

func TestRateLimiter_SeparateIPsIndependent(t *testing.T) {
	rl := NewRateLimiter(2) // 2 req/min, burst = 2
	handler := rl.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// Exhaust IP1's burst
	for range 2 {
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		req.RemoteAddr = "1.1.1.1:1234"
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code)
	}

	// IP1 should be blocked
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.RemoteAddr = "1.1.1.1:1234"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusTooManyRequests, rec.Code)

	// IP2 should still work
	req = httptest.NewRequest(http.MethodGet, "/test", nil)
	req.RemoteAddr = "2.2.2.2:1234"
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
}

// TestExtractIP_XForwardedFor_FromTrustedProxy validates that XFF is
// honoured when the immediate hop is loopback (the default trusted CIDR)
// AND the chain's intermediate hops are also trusted, so right-to-left
// parsing walks past them to the true client.
func TestExtractIP_XForwardedFor_FromTrustedProxy(t *testing.T) {
	// The chain represents: client → cdn (70.41.3.18) → nginx (150.172.238.178) → us (loopback)
	withTrustedProxyCIDRs(t, parseTrustedProxies("70.41.3.18,150.172.238.178"))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "127.0.0.1:54321" // loopback = trusted (default)
	req.Header.Set("X-Forwarded-For", "203.0.113.50, 70.41.3.18, 150.172.238.178")
	assert.Equal(t, "203.0.113.50", extractIP(req),
		"with both intermediate hops trusted, right-to-left skips them and returns the originating client")
}

// TestExtractIP_XRealIP_FromTrustedProxy validates X-Real-IP from loopback.
func TestExtractIP_XRealIP_FromTrustedProxy(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "127.0.0.1:54321"
	req.Header.Set("X-Real-IP", "203.0.113.50")
	assert.Equal(t, "203.0.113.50", extractIP(req))
}

// TestExtractIP_AppendProxyChain is the F-007 / Rabbit-Critical follow-up.
// nginx's default `proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for`
// APPENDS the immediate hop to whatever the client sent. An attacker that
// pre-seeds `X-Forwarded-For: 8.8.8.8` then has nginx tack on the real IP,
// producing `X-Forwarded-For: 8.8.8.8, <real-attacker>`. The previous
// leftmost-reading code returned 8.8.8.8 — the spoof — and let the
// attacker pop fresh per-IP token buckets. Right-to-left walks past the
// trusted nginx hop and surfaces the real attacker IP for bucketing.
func TestExtractIP_AppendProxyChain(t *testing.T) {
	// Trust the nginx proxy at 10.0.0.5/32 in addition to the loopback default.
	withTrustedProxyCIDRs(t, parseTrustedProxies("10.0.0.5/32"))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "10.0.0.5:54321" // the nginx hop — trusted
	// Attacker pre-seeded "8.8.8.8"; nginx appended the real client IP.
	req.Header.Set("X-Forwarded-For", "8.8.8.8, 198.51.100.42, 10.0.0.5")
	got := extractIP(req)
	assert.Equal(t, "198.51.100.42", got,
		"right-to-left parse must skip the trusted nginx hop and return the real attacker IP, not the leftmost spoof")
}

// TestExtractIP_LeftmostStillUntrustedSingleProxy keeps the previous
// "single overwrite proxy" case green — when there's only one entry in
// the chain and it's not a trusted proxy, that's the client.
func TestExtractIP_LeftmostStillUntrustedSingleProxy(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "127.0.0.1:9999" // loopback proxy
	req.Header.Set("X-Forwarded-For", "203.0.113.50")
	assert.Equal(t, "203.0.113.50", extractIP(req))
}

// TestExtractIP_AllChainEntriesAreTrusted falls back to the connection IP.
func TestExtractIP_AllChainEntriesAreTrusted(t *testing.T) {
	withTrustedProxyCIDRs(t, parseTrustedProxies("10.0.0.0/8"))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "10.0.0.5:9999"
	req.Header.Set("X-Forwarded-For", "10.0.1.1, 10.0.2.2, 10.0.3.3")
	// All XFF entries are inside 10/8 — trusted. clientIPFromXFF returns "",
	// then X-Real-IP empty, then we fall back to RemoteAddr.
	assert.Equal(t, "10.0.0.5", extractIP(req))
}

// TestClientIPFromXFF_AcceptsHostPort handles real-world headers that
// include the source port on XFF entries.
func TestClientIPFromXFF_AcceptsHostPort(t *testing.T) {
	withTrustedProxyCIDRs(t, parseTrustedProxies("10.0.0.5/32"))

	got := clientIPFromXFF("8.8.8.8, 203.0.113.50:54321, 10.0.0.5:8080")
	assert.Equal(t, "203.0.113.50", got, "host:port entries should resolve to host before trust check")
}

// TestExtractIP_XRealIPFromTrustedProxyDoesNotSelfTrust keeps an attacker
// that knows the trusted-proxy IP from setting X-Real-IP=<that-IP> and
// having extractIP echo it back.
func TestExtractIP_XRealIPRejectsTrustedProxyValue(t *testing.T) {
	withTrustedProxyCIDRs(t, parseTrustedProxies("10.0.0.5/32"))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "10.0.0.5:9999"
	// XFF empty (so we fall to X-Real-IP), but the value points at
	// the trusted proxy itself — accepting that would let an
	// attacker who knows the proxy IP spoof.
	req.Header.Set("X-Real-IP", "10.0.0.5")
	assert.Equal(t, "10.0.0.5", extractIP(req),
		"X-Real-IP equal to a trusted proxy must fall back to RemoteAddr (which happens to also be 10.0.0.5 here)")
}

// TestExtractIP_XForwardedFor_FromUntrustedClient is the regression guard for
// the F-007 bypass: an untrusted public client sending XFF must NOT be granted
// a fake IP — the real connection IP must be used so per-IP buckets actually
// constrain the attacker.
func TestExtractIP_XForwardedFor_FromUntrustedClient(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "203.0.113.99:12345"        // public, NOT in trusted CIDR list
	req.Header.Set("X-Forwarded-For", "8.8.8.8") // attacker-supplied
	req.Header.Set("X-Real-IP", "1.1.1.1")       // attacker-supplied
	assert.Equal(t, "203.0.113.99", extractIP(req),
		"XFF/XRI from an untrusted hop must be ignored — otherwise an attacker rotates fake IPs to dodge the limiter")
}

// TestExtractIP_RemoteAddr stays the same — no proxy headers, plain connection IP.
func TestExtractIP_RemoteAddr(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "192.168.1.1:54321"
	assert.Equal(t, "192.168.1.1", extractIP(req))
}

// TestRateLimiter_XFFRotationDoesNotBypass is the end-to-end regression test
// for F-007. With the limiter set to burst=3 and the client sending a fresh
// XFF every request from a non-trusted hop, the limiter must still block at
// the 4th request because XFF is ignored.
func TestRateLimiter_XFFRotationDoesNotBypass(t *testing.T) {
	rl := NewRateLimiter(3)
	handler := rl.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	got := []int{}
	for i := 0; i < 6; i++ {
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		req.RemoteAddr = "203.0.113.50:1234" // public, not trusted
		req.Header.Set("X-Forwarded-For", "10.0.0."+itoa(i))
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		got = append(got, rec.Code)
	}
	// First 3 OK (burst), then 429 — proving XFF rotation is ignored.
	assert.Equal(t, []int{200, 200, 200, 429, 429, 429}, got)
}

// TestRateLimiter_XFFFromTrustedProxyStillSeparates verifies the legitimate
// case: a real reverse proxy at 127.0.0.1 forwarding requests for many users
// — each user's XFF gets its own bucket as expected.
func TestRateLimiter_XFFFromTrustedProxyStillSeparates(t *testing.T) {
	rl := NewRateLimiter(2)
	handler := rl.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// User A behind the proxy: 2 requests OK, 3rd blocked.
	for i, want := range []int{http.StatusOK, http.StatusOK, http.StatusTooManyRequests} {
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		req.RemoteAddr = "127.0.0.1:9999" // trusted
		req.Header.Set("X-Forwarded-For", "8.8.8.8")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		require.Equal(t, want, rec.Code, "user A request %d", i+1)
	}

	// User B behind the same proxy: independent bucket, still works.
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.RemoteAddr = "127.0.0.1:9999"
	req.Header.Set("X-Forwarded-For", "9.9.9.9")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestParseTrustedProxies_DefaultsLoopback(t *testing.T) {
	cidrs := parseTrustedProxies("")
	require.NotEmpty(t, cidrs)
	// 127.0.0.1 must be trusted.
	assert.True(t, anyContains(cidrs, net.ParseIP("127.0.0.1")))
	// ::1 must be trusted.
	assert.True(t, anyContains(cidrs, net.ParseIP("::1")))
	// A random public IP must not.
	assert.False(t, anyContains(cidrs, net.ParseIP("8.8.8.8")))
}

func TestParseTrustedProxies_AcceptsBareIPAndCIDR(t *testing.T) {
	cidrs := parseTrustedProxies("10.0.0.5,192.168.0.0/16,2001:db8::/32")
	assert.True(t, anyContains(cidrs, net.ParseIP("10.0.0.5")))
	assert.True(t, anyContains(cidrs, net.ParseIP("192.168.42.7")))
	assert.True(t, anyContains(cidrs, net.ParseIP("2001:db8::1")))
	assert.False(t, anyContains(cidrs, net.ParseIP("10.0.0.6"))) // /32 only matches the literal IP
}

func TestParseTrustedProxies_DropsInvalidEntries(t *testing.T) {
	cidrs := parseTrustedProxies("not-an-ip,10.0.0.1,also-bad/99")
	// Loopback defaults + the one valid entry survive; invalid entries don't crash.
	assert.True(t, anyContains(cidrs, net.ParseIP("127.0.0.1")))
	assert.True(t, anyContains(cidrs, net.ParseIP("10.0.0.1")))
}

// TestResolveRateLimitDisabled_NewVarBeatsLegacy is the regression
// guard for CodeRabbit's R2 "make the renamed env var authoritative"
// note. A stale CREWSHIP_DISABLE_RATELIMIT=true left in the environment
// from before the rename must not silently override an explicit
// CREWSHIP_RATELIMIT_DISABLED=false.
func TestResolveRateLimitDisabled_NewVarBeatsLegacy(t *testing.T) {
	cases := []struct {
		name      string
		newVar    *string // nil → unset
		legacyVar *string
		want      bool
	}{
		{"both unset", nil, nil, false},
		{"new=true legacy=true", strPtr("true"), strPtr("true"), true},
		{"new=true legacy=false", strPtr("true"), strPtr("false"), true},
		{"new=false legacy=true (legacy must NOT win)", strPtr("false"), strPtr("true"), false},
		{"new=false legacy unset", strPtr("false"), nil, false},
		{"new unset legacy=true (legacy honoured)", nil, strPtr("true"), true},
		{"new unset legacy=false", nil, strPtr("false"), false},
		{"new=garbage legacy=true (new is set => authoritative even when unparseable)", strPtr("not-a-bool"), strPtr("true"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.newVar != nil {
				t.Setenv("CREWSHIP_RATELIMIT_DISABLED", *tc.newVar)
			} else {
				_ = os.Unsetenv("CREWSHIP_RATELIMIT_DISABLED")
			}
			if tc.legacyVar != nil {
				t.Setenv("CREWSHIP_DISABLE_RATELIMIT", *tc.legacyVar)
			} else {
				_ = os.Unsetenv("CREWSHIP_DISABLE_RATELIMIT")
			}
			got := resolveRateLimitDisabled()
			assert.Equal(t, tc.want, got, "case %q", tc.name)
		})
	}
}

func anyContains(cidrs []*net.IPNet, ip net.IP) bool {
	if ip == nil {
		return false
	}
	for _, c := range cidrs {
		if c.Contains(ip) {
			return true
		}
	}
	return false
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	out := []byte{}
	for i > 0 {
		out = append([]byte{byte('0' + i%10)}, out...)
		i /= 10
	}
	return string(out)
}

func TestRateLimiter_Cleanup(t *testing.T) {
	rl := NewRateLimiter(10)

	// Add a visitor
	rl.getLimiter("10.0.0.1")
	rl.mu.Lock()
	// Backdate its lastSeen
	rl.visitors["10.0.0.1"].lastSeen = rl.visitors["10.0.0.1"].lastSeen.Add(-10 * time.Minute)
	rl.mu.Unlock()

	rl.cleanup()

	rl.mu.Lock()
	_, exists := rl.visitors["10.0.0.1"]
	rl.mu.Unlock()
	assert.False(t, exists, "stale visitor should be cleaned up")
}
