package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// clientIP is the helper that produces user_sessions.ip from a Request.
// It MUST handle IPv6 correctly — the previous LastIndexByte(':') split
// returned "[::1" for "[::1]:8080" because IPv6 has multiple colons.
// Audit rows with malformed addresses can't be queried back later, so
// the bug bites silently. CodeRabbit caught it on PR #233.
func TestClientIP(t *testing.T) {
	cases := []struct {
		name       string
		remoteAddr string
		xff        string
		want       string
	}{
		{"IPv4 with port", "1.2.3.4:5678", "", "1.2.3.4"},
		{"IPv4 no port", "1.2.3.4", "", "1.2.3.4"},
		{"IPv6 loopback with port", "[::1]:8080", "", "::1"},
		{"IPv6 link-local with port", "[fe80::1]:443", "", "fe80::1"},
		{"IPv6 full address with port", "[2001:db8::1]:8080", "", "2001:db8::1"},
		{"X-Forwarded-For single", "1.2.3.4:5678", "9.9.9.9", "9.9.9.9"},
		{"X-Forwarded-For chain — first hop wins", "1.2.3.4:5678", "203.0.113.7, 198.51.100.2, 1.2.3.4", "203.0.113.7"},
		{"X-Forwarded-For with whitespace", "x", "  4.4.4.4  , y", "4.4.4.4"},
		{"empty RemoteAddr no XFF", "", "", ""},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.RemoteAddr = c.remoteAddr
			if c.xff != "" {
				req.Header.Set("X-Forwarded-For", c.xff)
			}
			got := clientIP(req)
			if got != c.want {
				t.Errorf("clientIP(remote=%q, xff=%q) = %q, want %q",
					c.remoteAddr, c.xff, got, c.want)
			}
		})
	}
}
