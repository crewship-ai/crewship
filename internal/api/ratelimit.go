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

// rateLimitDisabled is true when CREWSHIP_DISABLE_RATELIMIT parses as a truthy
// boolean (typically in dev shells running E2E suites against a real backend).
// When true the middleware is a pass-through. Fails closed: empty string or an
// unparseable value keeps rate-limiting engaged.
var rateLimitDisabled = parseBoolEnv("CREWSHIP_DISABLE_RATELIMIT")

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
// When CREWSHIP_DISABLE_RATELIMIT is set the middleware is a no-op.
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

// extractIP returns the client IP from the request, preferring
// X-Forwarded-For and X-Real-IP headers (trusted reverse proxy).
func extractIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		// First entry is the original client
		if comma := indexOf(xff, ','); comma != -1 {
			xff = xff[:comma]
		}
		if ip := trimSpace(xff); ip != "" {
			return ip
		}
	}
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		return trimSpace(xri)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func indexOf(s string, b byte) int {
	for i := range len(s) {
		if s[i] == b {
			return i
		}
	}
	return -1
}

func trimSpace(s string) string {
	start, end := 0, len(s)
	for start < end && s[start] == ' ' {
		start++
	}
	for end > start && s[end-1] == ' ' {
		end--
	}
	return s[start:end]
}
