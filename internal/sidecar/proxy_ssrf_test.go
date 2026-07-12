package sidecar

import (
	"context"
	"strings"
	"testing"
)

// TestSSRFDialContext locks the sidecar's resolve-then-pin guard (#961). Using
// literal IPs, net.DefaultResolver.LookupIPAddr returns the address without a
// network lookup, so the block decision is exercised before any real dial.
func TestSSRFDialContext(t *testing.T) {
	cases := []struct {
		name         string
		addr         string
		allowPrivate bool
		wantBlocked  bool
	}{
		{"metadata, private off", "169.254.169.254:80", false, true},
		{"metadata, private ON → still blocked", "169.254.169.254:80", true, true},
		{"ipv4-mapped metadata blocked", "[::ffff:169.254.169.254]:80", true, true},
		{"rfc1918, private off → blocked", "192.168.1.222:11434", false, true},
		{"loopback, private off → blocked", "127.0.0.1:11434", false, true},
		// private-on cases would proceed to a real dial (and fail to connect in
		// CI) — we only assert the guard does NOT reject them with the SSRF error.
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dial := ssrfDialContext(c.allowPrivate)
			_, err := dial(context.Background(), "tcp", c.addr)
			blocked := err != nil && strings.Contains(err.Error(), "refusing to dial blocked address")
			if blocked != c.wantBlocked {
				t.Fatalf("dial(%s, allowPrivate=%v) blocked=%v (err=%v), want blocked=%v",
					c.addr, c.allowPrivate, blocked, err, c.wantBlocked)
			}
		})
	}
}

// TestSSRFDialContext_PrivateAllowed confirms a private target is NOT rejected
// by the SSRF guard when the crew opted in — it proceeds to a real dial (which
// then fails to connect to an unused port, a different, non-SSRF error).
func TestSSRFDialContext_PrivateAllowed(t *testing.T) {
	dial := ssrfDialContext(true)
	_, err := dial(context.Background(), "tcp", "127.0.0.1:1") // port 1: connection refused, not SSRF-blocked
	if err != nil && strings.Contains(err.Error(), "refusing to dial blocked address") {
		t.Fatalf("loopback with allowPrivate=true was SSRF-blocked, want allowed through to dial: %v", err)
	}
}
