package api

import (
	"net"
	"net/http"
	"net/http/httptest"
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

// TestExtractIP_XForwardedFor_FromTrustedProxy validates that XFF is honoured
// when the immediate hop is loopback (the default trusted CIDR).
func TestExtractIP_XForwardedFor_FromTrustedProxy(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "127.0.0.1:54321" // loopback = trusted
	req.Header.Set("X-Forwarded-For", "203.0.113.50, 70.41.3.18, 150.172.238.178")
	assert.Equal(t, "203.0.113.50", extractIP(req))
}

// TestExtractIP_XRealIP_FromTrustedProxy validates X-Real-IP from loopback.
func TestExtractIP_XRealIP_FromTrustedProxy(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "127.0.0.1:54321"
	req.Header.Set("X-Real-IP", "203.0.113.50")
	assert.Equal(t, "203.0.113.50", extractIP(req))
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
