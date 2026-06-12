package api

import (
	"context"
	"strings"
	"testing"
)

// integration_test_connection_cov_test.go covers the locally reachable
// branches of the MCP connection tester: transport dispatch, the
// invalid-URL guard, and the SSRF dialer's address validation. The
// post-connection response parsing branches are NOT testable here —
// ssrfSafeTransport blocks loopback/private IPs at dial time, so an
// httptest server can never answer (documented as skipped in the
// coverage task).

func TestCovITC_TestMCPConnection_HTTPTransportEmptyEndpoint(t *testing.T) {
	resp := testMCPConnection(context.Background(), "sse", "", newTestLogger())
	if resp.Status != "error" {
		t.Fatalf("status = %q, want error", resp.Status)
	}
	if !strings.Contains(resp.Message, "No endpoint configured") {
		t.Errorf("message = %q, want 'No endpoint configured...'", resp.Message)
	}
}

func TestCovITC_TestStreamableHTTP_InvalidURL(t *testing.T) {
	// Unclosed IPv6 bracket fails url.Parse before any network access.
	resp := testStreamableHTTPConnection(context.Background(), "http://[::1")
	if resp.Status != "error" {
		t.Fatalf("status = %q, want error", resp.Status)
	}
	if !strings.Contains(resp.Message, "Invalid endpoint URL") {
		t.Errorf("message = %q, want 'Invalid endpoint URL...'", resp.Message)
	}
}

func TestCovITC_TestStreamableHTTP_LoopbackBlockedBySSRFGuard(t *testing.T) {
	// Connection attempt to loopback is blocked at dial time by the
	// SSRF-safe transport — no listener needed, no real network I/O.
	resp := testStreamableHTTPConnection(context.Background(), "http://127.0.0.1:9/")
	if resp.Status != "error" {
		t.Fatalf("status = %q, want error", resp.Status)
	}
	if !strings.Contains(resp.Message, "Connection failed") {
		t.Errorf("message = %q, want 'Connection failed...'", resp.Message)
	}
}

func TestCovITC_SSRFSafeTransport_DialContextValidation(t *testing.T) {
	tr := ssrfSafeTransport()

	// Address without a port → SplitHostPort error.
	if _, err := tr.DialContext(context.Background(), "tcp", "127.0.0.1"); err == nil {
		t.Fatalf("dial without port succeeded, want invalid-address error")
	} else if !strings.Contains(err.Error(), "invalid address") {
		t.Errorf("err = %v, want invalid address", err)
	}

	// Private/loopback IP literal → blocked without DNS or network.
	if _, err := tr.DialContext(context.Background(), "tcp", "127.0.0.1:80"); err == nil {
		t.Fatalf("dial to loopback succeeded, want SSRF block")
	} else if !strings.Contains(err.Error(), "blocked connection to private/internal IP") {
		t.Errorf("err = %v, want private-IP block", err)
	}

	// 169.254/16 link-local literal hits the same guard via isPrivateIP.
	if _, err := tr.DialContext(context.Background(), "tcp", "169.254.1.2:80"); err == nil {
		t.Fatalf("dial to link-local succeeded, want SSRF block")
	}
}
