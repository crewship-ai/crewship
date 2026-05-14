package skills

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsBlockedIP_TableDriven(t *testing.T) {
	cases := []struct {
		ip      string
		blocked bool
		why     string
	}{
		// loopback
		{"127.0.0.1", true, "v4 loopback"},
		{"127.255.255.255", true, "v4 loopback /8 edge"},
		{"::1", true, "v6 loopback"},
		// private networks
		{"10.0.0.1", true, "RFC1918 10/8"},
		{"172.16.0.1", true, "RFC1918 172.16/12"},
		{"172.31.255.255", true, "RFC1918 172.16/12 edge"},
		{"192.168.1.1", true, "RFC1918 192.168/16"},
		// link-local incl. cloud metadata
		{"169.254.0.1", true, "link-local"},
		{"169.254.169.254", true, "AWS/GCP IMDS"},
		// CGNAT
		{"100.64.0.1", true, "CGNAT 100.64/10"},
		// IPv4-mapped IPv6 of private addresses (DNS rebinding via IPv6 alias)
		{"::ffff:127.0.0.1", true, "v4-mapped v6 loopback"},
		{"::ffff:192.168.1.1", true, "v4-mapped v6 RFC1918"},
		{"::ffff:169.254.169.254", true, "v4-mapped v6 IMDS"},
		// IPv6 ULAs and link-local
		{"fc00::1", true, "v6 ULA"},
		{"fe80::1", true, "v6 link-local"},
		// Public — must NOT be blocked
		{"8.8.8.8", false, "Google DNS"},
		{"1.1.1.1", false, "Cloudflare DNS"},
		{"140.82.114.3", false, "github.com (current)"},
		{"2606:4700:4700::1111", false, "v6 Cloudflare"},
	}
	for _, tc := range cases {
		t.Run(tc.ip, func(t *testing.T) {
			ip := net.ParseIP(tc.ip)
			require.NotNil(t, ip, "test data must parse")
			assert.Equal(t, tc.blocked, isBlockedIP(ip), "%s: %s", tc.ip, tc.why)
		})
	}
}

// TestSafeDialContext_RefusesLoopback fires up an httptest server on
// loopback and asks the safeDialContext to connect to it. The connection
// must fail with ErrSSRFBlocked — proving the dial-time guard catches
// targets that survive the URL-string validator (the validator can't
// reject a hostname like "127.0.0.1.nip.io" because the literal IP isn't
// in the URL, but the resolved IP is in the blocked range).
func TestSafeDialContext_RefusesLoopback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// srv.URL is something like http://127.0.0.1:PORT
	addr := strings.TrimPrefix(srv.URL, "http://")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, err := safeDialContext()(ctx, "tcp", addr)
	if conn != nil {
		_ = conn.Close()
	}
	require.Error(t, err)
	// errors.Is unwraps net.OpError automatically.
	assert.ErrorIs(t, err, ErrSSRFBlocked, "loopback dial must surface ErrSSRFBlocked")
}

// TestSafeDialContext_AllowsPublicIP confirms the guard isn't a silent
// blanket reject — connecting to a non-private literal IP works. We can
// rely on a TCP connect attempt to a public IP succeeding at the syscall
// layer (it'll route out the default gateway); we don't care what the
// peer does afterwards, only that the Control hook approved.
//
// Skipped automatically when there's no IPv4 default route (CI sandbox).
func TestSafeDialContext_AllowsPublicIP(t *testing.T) {
	if !hasDefaultRoute() {
		t.Skip("no default route — running in sandbox without egress")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	// 1.1.1.1:443 is a stable, well-known target. Even if the firewall drops
	// the SYN, the Control hook still runs first.
	conn, err := safeDialContext()(ctx, "tcp", "1.1.1.1:443")
	if conn != nil {
		_ = conn.Close()
	}
	if err != nil {
		// If it failed, make sure it's NOT the SSRF guard — that would be a
		// false positive (1.1.1.1 is public).
		assert.NotErrorIs(t, err, ErrSSRFBlocked, "public IP must not trigger SSRF guard")
	}
}

func hasDefaultRoute() bool {
	conn, err := net.DialTimeout("udp", "1.1.1.1:53", 200*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func TestClassifyFetchError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"nil", nil, ""},
		{"ssrf", ErrSSRFBlocked, "blocked"},
		{"dns", &net.OpError{Err: &net.DNSError{Err: "no such host", Name: "x"}}, "dns_failed"},
		{"refused", &net.OpError{Err: &netStub{msg: "connection refused"}}, "unreachable"},
		{"timeout", &net.OpError{Err: &netStub{msg: "context deadline exceeded"}}, "timeout"},
		{"tls", &net.OpError{Err: &netStub{msg: "tls: handshake failure"}}, "tls_failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, ClassifyFetchError(tc.err))
		})
	}
}

// netStub lets the test produce a net.OpError with a controlled message.
type netStub struct{ msg string }

func (n *netStub) Error() string { return n.msg }
