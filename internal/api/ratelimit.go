package api

import (
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// rateLimitDisabled is true when the operator has explicitly toggled the
// rate limiter off. Two env vars are honoured, with strict precedence:
//
//  1. CREWSHIP_RATELIMIT_DISABLED — the canonical, current name. If set
//     (any value, even empty), it is authoritative.
//  2. CREWSHIP_DISABLE_RATELIMIT — the legacy alias kept for one release
//     to ease the rename. Only consulted when the canonical var is
//     **not set at all**.
//
// CodeRabbit's R2 review caught the ambiguity in the previous OR-form:
// a stale `CREWSHIP_DISABLE_RATELIMIT=true` left over from before the
// rename would silently override an explicit
// `CREWSHIP_RATELIMIT_DISABLED=false`, and would also trip the
// MustNotDisableRateLimitInProd guard after the rename.
//
// In production (CREWSHIP_ENV=prod or production), this flag is ignored
// regardless of which env var set it — the limiter always runs. See
// MustNotDisableRateLimitInProd.
var rateLimitDisabled = resolveRateLimitDisabled()

func resolveRateLimitDisabled() bool {
	if v, ok := lookupBoolEnv("CREWSHIP_RATELIMIT_DISABLED"); ok {
		return v
	}
	v, _ := lookupBoolEnv("CREWSHIP_DISABLE_RATELIMIT")
	return v
}

// trustedProxyCIDRs lists the CIDRs of reverse proxies whose
// X-Forwarded-For/X-Real-IP headers we trust. Set via
// CREWSHIP_TRUSTED_PROXY_CIDRS as a comma-separated list. When unset, only
// loopback (127.0.0.0/8 and ::1/128) is trusted — that covers the common
// case of a local nginx/Caddy on the same host. Anything else MUST be
// explicit.
//
// The previous implementation read XFF from any client unconditionally,
// which let an attacker present a fresh fake IP per request and get an
// uncapped token bucket — bypassing credential-stuffing throttles, the
// `/credentials/test` validation oracle, and the general API limit alike.
var trustedProxyCIDRs = parseTrustedProxies(os.Getenv("CREWSHIP_TRUSTED_PROXY_CIDRS"))

// lookupBoolEnv reads a boolean env var with three-state semantics:
//   - (false, false) → variable is not set at all (unset)
//   - (true,  true)  → variable is set to a truthy value
//   - (false, true)  → variable is set to a falsy / unparseable value
//
// The "set" bit is what `resolveRateLimitDisabled` uses to give the
// canonical name precedence over the legacy alias even when its value
// is `false`.
func lookupBoolEnv(name string) (bool, bool) {
	raw, ok := os.LookupEnv(name)
	if !ok {
		return false, false
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		// Set but blank — treat as "not enabled", but still authoritative.
		return false, true
	}
	b, err := strconv.ParseBool(raw)
	if err != nil {
		return false, true
	}
	return b, true
}

// parseTrustedProxies parses a comma-separated CIDR list. Defaults to
// loopback when the env var is empty. Invalid entries are dropped with a
// startup-time stderr message so operators notice typos but the server
// still runs with the surviving entries (loopback as a floor).
func parseTrustedProxies(raw string) []*net.IPNet {
	loopback := mustCIDR("127.0.0.0/8")
	loopbackV6 := mustCIDR("::1/128")
	out := []*net.IPNet{loopback, loopbackV6}

	if strings.TrimSpace(raw) == "" {
		return out
	}
	for _, item := range strings.Split(raw, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		// Accept bare IPs as /32 or /128 for ergonomics
		if !strings.Contains(item, "/") {
			ip := net.ParseIP(item)
			if ip == nil {
				continue
			}
			if ip.To4() != nil {
				item += "/32"
			} else {
				item += "/128"
			}
		}
		_, ipnet, err := net.ParseCIDR(item)
		if err != nil {
			continue
		}
		out = append(out, ipnet)
	}
	return out
}

func mustCIDR(s string) *net.IPNet {
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		panic("ratelimit: invalid hardcoded CIDR " + s + ": " + err.Error())
	}
	return n
}

// ipLimiter tracks a per-IP rate limiter and when it was last seen.
type ipLimiter struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// RateLimiter provides per-IP HTTP rate limiting middleware.
type RateLimiter struct {
	mu       sync.Mutex
	visitors map[string]*ipLimiter
	rps      rate.Limit // requests per second
	burst    int
}

// NewRateLimiter creates a rate limiter that allows reqPerMin requests per
// minute per IP, with a burst equal to reqPerMin (token bucket).
func NewRateLimiter(reqPerMin int) *RateLimiter {
	rl := &RateLimiter{
		visitors: make(map[string]*ipLimiter),
		rps:      rate.Limit(float64(reqPerMin) / 60.0),
		burst:    reqPerMin,
	}
	go rl.cleanupLoop()
	return rl
}

// getLimiter returns the rate limiter for the given IP, creating one if needed.
func (rl *RateLimiter) getLimiter(ip string) *rate.Limiter {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	v, exists := rl.visitors[ip]
	if !exists {
		limiter := rate.NewLimiter(rl.rps, rl.burst)
		rl.visitors[ip] = &ipLimiter{limiter: limiter, lastSeen: time.Now()}
		return limiter
	}
	v.lastSeen = time.Now()
	return v.limiter
}

// cleanupLoop removes stale entries every 3 minutes.
func (rl *RateLimiter) cleanupLoop() {
	ticker := time.NewTicker(3 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		rl.cleanup()
	}
}

// cleanup removes visitors not seen in the last 5 minutes.
func (rl *RateLimiter) cleanup() {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	cutoff := time.Now().Add(-5 * time.Minute)
	for ip, v := range rl.visitors {
		if v.lastSeen.Before(cutoff) {
			delete(rl.visitors, ip)
		}
	}
}

// Middleware returns an http.Handler that rate-limits requests by client IP.
// When the limit is exceeded, it responds with 429 Too Many Requests.
// When CREWSHIP_RATELIMIT_DISABLED is set the middleware is a no-op.
func (rl *RateLimiter) Middleware(next http.Handler) http.Handler {
	if rateLimitDisabled {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := extractIP(r)
		limiter := rl.getLimiter(ip)
		if !limiter.Allow() {
			w.Header().Set("Retry-After", "60")
			writeJSON(w, http.StatusTooManyRequests, map[string]string{
				"error": "Too many requests",
			})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// extractIP returns the client IP from the request. X-Forwarded-For and
// X-Real-IP headers are honoured ONLY when the immediate hop (r.RemoteAddr)
// is in the trusted-proxy CIDR list. Untrusted clients can set these
// headers freely; treating them as authoritative would let any attacker
// rotate fake IPs per request and pop their own fresh token bucket.
//
// Trust list defaults to loopback. Operators behind a non-local proxy must
// set CREWSHIP_TRUSTED_PROXY_CIDRS=<proxy-ip-or-cidr,...>.
//
// When XFF is honoured, the chain is parsed *right to left*, skipping
// trusted-proxy hops, returning the first untrusted IP. The naive
// "leftmost entry is the client" reading from the previous fix breaks
// when the immediate proxy *appends* to XFF instead of overwriting it
// — nginx with `proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for`
// (the default in many helm charts) does exactly this. With overwrite
// proxies, leftmost-vs-rightmost is the same value. With append proxies,
// an attacker can pre-seed `X-Forwarded-For: 8.8.8.8` and the proxy
// turns it into `8.8.8.8, <real-attacker-ip>` — leftmost reads back
// the spoof, rightmost-after-skip reads the real client. CodeRabbit
// caught this on the first PR pass.
func extractIP(r *http.Request) string {
	hop := remoteHopIP(r)

	if isTrustedProxy(hop) {
		if ip := clientIPFromXFF(r.Header.Get("X-Forwarded-For")); ip != "" {
			return ip
		}
		if xri := strings.TrimSpace(r.Header.Get("X-Real-IP")); xri != "" {
			if parsed := net.ParseIP(xri); parsed != nil && !isTrustedProxy(parsed.String()) {
				return parsed.String()
			}
		}
	}

	return hop
}

// clientIPFromXFF parses an X-Forwarded-For chain right to left, skips
// any entry that is itself a trusted proxy, and returns the first IP
// that isn't. Returns "" if no untrusted entry exists (which means the
// caller should fall back to r.RemoteAddr).
//
// Right-to-left order is RFC 7239 §5.3 — the rightmost entry is the
// proxy hop closest to us, the leftmost is whatever the originator
// chose to put there. Skipping trusted hops is what defeats the spoof:
// a forged leftmost entry is silently ignored because the real proxy
// hop sits to its right. With a single overwrite-style proxy the
// behaviour matches the previous leftmost reading; with an append-style
// proxy chain it correctly walks past the proxy IPs to the real client.
func clientIPFromXFF(xff string) string {
	if xff == "" {
		return ""
	}
	parts := strings.Split(xff, ",")
	for i := len(parts) - 1; i >= 0; i-- {
		raw := strings.TrimSpace(parts[i])
		if raw == "" {
			continue
		}
		// XFF entries can include a port in some proxy configurations
		// ("1.2.3.4:54321"). net.ParseIP rejects those, so try
		// SplitHostPort first; if it succeeds, use the host part.
		host := raw
		if h, _, err := net.SplitHostPort(raw); err == nil {
			host = h
		}
		ip := net.ParseIP(host)
		if ip == nil {
			continue
		}
		if isTrustedProxy(ip.String()) {
			continue
		}
		return ip.String()
	}
	return ""
}

// remoteHopIP extracts the connection-level peer IP from r.RemoteAddr,
// stripping any port. Falls back to the raw value if SplitHostPort fails
// (e.g. test fixtures that omit a port) so the limiter still buckets by
// something stable rather than crashing.
func remoteHopIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func isTrustedProxy(ip string) bool {
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return false
	}
	for _, cidr := range trustedProxyCIDRs {
		if cidr.Contains(parsed) {
			return true
		}
	}
	return false
}

// MustNotDisableRateLimitInProd panics during server boot if rate limiting
// is disabled while running in a production environment. Operators get a
// loud failure rather than discovering the gap from a credential-stuffing
// incident. Called from cmd_start before listeners start accepting traffic.
func MustNotDisableRateLimitInProd() {
	if !rateLimitDisabled {
		return
	}
	env := strings.ToLower(strings.TrimSpace(os.Getenv("CREWSHIP_ENV")))
	if env == "prod" || env == "production" {
		panic("rate limiting is disabled (CREWSHIP_RATELIMIT_DISABLED) but CREWSHIP_ENV=" + env +
			"; refusing to start. Unset the flag or change CREWSHIP_ENV to a non-prod value.")
	}
}
