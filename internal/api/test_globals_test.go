package api

import (
	"net"
	"sync"
	"testing"
)

// testGlobalsMu serialises access to the package-level configuration
// vars that some tests need to override (allowedOriginSuffixes,
// trustedProxyCIDRs). The package coding-guideline rule is "no test
// pollution / parallel-safe" — without the mutex two tests calling
// `withAllowedOriginSuffixes(t, ...)` from concurrent t.Run/t.Parallel
// could interleave and observe each other's overrides. CodeRabbit
// raised this on the second-pass review.
//
// We don't t.Parallel() these tests today, but the mutex makes the
// override safe even if a future contributor adds it.
var testGlobalsMu sync.Mutex

// withTrustedProxyCIDRs swaps the package-level trustedProxyCIDRs for
// the duration of the test. Restores via t.Cleanup so a failing test
// can't leak the override into the next one.
func withTrustedProxyCIDRs(t *testing.T, value []*net.IPNet) {
	t.Helper()
	testGlobalsMu.Lock()
	orig := trustedProxyCIDRs
	trustedProxyCIDRs = value
	t.Cleanup(func() {
		trustedProxyCIDRs = orig
		testGlobalsMu.Unlock()
	})
}

// withAllowedOriginSuffixes — same pattern for allowedOriginSuffixes.
func withAllowedOriginSuffixes(t *testing.T, value []string) {
	t.Helper()
	testGlobalsMu.Lock()
	orig := allowedOriginSuffixes
	allowedOriginSuffixes = value
	t.Cleanup(func() {
		allowedOriginSuffixes = orig
		testGlobalsMu.Unlock()
	})
}
