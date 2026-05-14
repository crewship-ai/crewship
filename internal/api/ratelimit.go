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

// rateLimitDisabled is true when CREWSHIP_RATELIMIT_DISABLED parses as a
// truthy boolean. Typically only set in dev shells running E2E suites that
// would otherwise exhaust the 10/min auth bucket. Fails closed on parse
// error: empty string or unparseable value keeps rate-limiting engaged.
//
// Legacy alias: CREWSHIP_DISABLE_RATELIMIT is honoured for one release for
// existing dev environments. Logged at startup so the operator notices the
// rename. Will be removed in a follow-up release.
//
// In production (CREWSHIP_ENV=prod or production), this flag is ignored —
// the limiter always runs. See SecureStartupCheck.
var rateLimitDisabled = parseBoolEnv("CREWSHIP_RATELIMIT_DISABLED") ||
	parseBoolEnv("CREWSHIP_DISABLE_RATELIMIT")

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

func parseBoolEnv(name string) bool {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return false
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false
	}
	return b
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
func extractIP(r *http.Request) string {
	hop := remoteHopIP(r)

	if isTrustedProxy(hop) {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			// Per RFC 7239, the leftmost entry is the original client.
			if comma := strings.IndexByte(xff, ','); comma != -1 {
				xff = xff[:comma]
			}
			if ip := strings.TrimSpace(xff); ip != "" {
				return ip
			}
		}
		if xri := strings.TrimSpace(r.Header.Get("X-Real-IP")); xri != "" {
			return xri
		}
	}

	return hop
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

// RateLimitDisabled reports whether rate limiting is currently bypassed.
// Exposed for /api/health to surface the runtime state to scrapers.
func RateLimitDisabled() bool { return rateLimitDisabled }
