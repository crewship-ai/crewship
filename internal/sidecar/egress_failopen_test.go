package sidecar

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// These tests lock finding S1 (HIGH) from the 2026-06 security audit
// (.claude/context/SECURITY-AUDIT-2026-06.md): NewServer (server.go) used to
// initialize freeMode:=true when cfg.NetworkPolicy is nil. A crew that never
// declared a network policy therefore ran the sidecar proxy in "free" mode,
// where the allowlist check (proxy.go:149 for HTTP, proxy.go:233 for CONNECT)
// was skipped entirely — the agent could reach ANY host and exfiltrate data.
// The egress control failed OPEN by default.
//
// The fix flips the nil-policy default to RESTRICTED (fail closed). These tests
// now assert the SECURE behavior: a nil NetworkPolicy → freeMode=false, and a
// non-allowlisted host is BLOCKED with 403 on both the HTTP and CONNECT arms.
// They would FAIL again if the default ever regressed to free.

// TestEgressFailOpen_NilPolicy_DefaultsRestricted proves the construction path:
// a Server built with no NetworkPolicy comes up with the proxy in RESTRICTED
// mode, so the allowlist is consulted on every request.
func TestEgressFailOpen_NilPolicy_DefaultsRestricted(t *testing.T) {
	srv := NewServer(ServerConfig{
		Addr:          "127.0.0.1:0",
		NetworkPolicy: nil, // a default crew: no policy declared
		Logger:        testLogger(),
	})

	if srv.proxy.freeMode {
		t.Fatal("S1 regression: nil NetworkPolicy must default to restricted (freeMode=false), got free mode")
	}
}

// TestEgressFailOpen_HTTPToNonAllowlistedHost_Blocked drives a live HTTP proxy
// request through the default (nil-policy) server to a host that is NOT on the
// allowlist. Fail-closed: the allowlist branch must reject it with 403 before
// any upstream dial.
func TestEgressFailOpen_HTTPToNonAllowlistedHost_Blocked(t *testing.T) {
	srv := NewServer(ServerConfig{
		Addr:          "127.0.0.1:0",
		NetworkPolicy: nil,
		Logger:        testLogger(),
	})

	req := httptest.NewRequest(http.MethodGet, "http://exfil.not-on-allowlist.test/leak", nil)
	rec := httptest.NewRecorder()
	srv.proxy.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("S1 regression: default (nil-policy) server must block HTTP to a non-allowlisted host with 403, got %d", rec.Code)
	}
}

// TestEgressFailOpen_ConnectBlocked proves the CONNECT (HTTPS tunnel) arm.
// A restricted proxy — and now the default nil-policy server — rejects a
// CONNECT to a non-allowlisted host with 403 before any dial, keeping the test
// hermetic (no network).
func TestEgressFailOpen_ConnectBlocked(t *testing.T) {
	// Restricted contrast: the allowlist DOES block CONNECT (fail-closed path
	// works when a policy is set — this is the regression guard for it).
	restricted := newTestProxy(nil, []string{"api.anthropic.com"})
	req := httptest.NewRequest(http.MethodConnect, "evil.not-on-allowlist.test:443", nil)
	req.Host = "evil.not-on-allowlist.test:443"
	rec := httptest.NewRecorder()
	restricted.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("regression: restricted proxy should 403 a CONNECT to a non-allowlisted host, got %d", rec.Code)
	}

	// Default server (nil policy): now fail-closed, so the identical CONNECT
	// must also be rejected with 403 before any tunnel is established.
	srv := NewServer(ServerConfig{
		Addr:          "127.0.0.1:0",
		NetworkPolicy: nil,
		Logger:        testLogger(),
	})
	creq := httptest.NewRequest(http.MethodConnect, "evil.not-on-allowlist.test:443", nil)
	creq.Host = "evil.not-on-allowlist.test:443"
	crec := httptest.NewRecorder()
	srv.proxy.ServeHTTP(crec, creq)
	if crec.Code != http.StatusForbidden {
		t.Fatalf("S1 regression: default (nil-policy) server must block CONNECT to a non-allowlisted host with 403, got %d", crec.Code)
	}
}

// --- Secure target (end-to-end regression guard for the fail-closed default) -
//
// TestEgressFailOpen_SecureTarget asserts the full post-fix invariant: a nil
// NetworkPolicy resolves to "restricted" (freeMode=false) so a crew must opt
// INTO unrestricted egress, and both HTTP and CONNECT to a non-allowlisted host
// are rejected with 403.
func TestEgressFailOpen_SecureTarget(t *testing.T) {
	srv := NewServer(ServerConfig{
		Addr:          "127.0.0.1:0",
		NetworkPolicy: nil,
		Logger:        testLogger(),
	})
	if srv.proxy.freeMode {
		t.Fatal("nil NetworkPolicy must default to restricted (freeMode=false)")
	}

	// HTTP to a non-allowlisted host must be rejected with 403.
	req := httptest.NewRequest(http.MethodGet, "http://exfil.not-on-allowlist.test/leak", nil)
	rec := httptest.NewRecorder()
	srv.proxy.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("default server must block HTTP to non-allowlisted host, got %d", rec.Code)
	}

	// CONNECT to a non-allowlisted host must likewise be rejected with 403.
	creq := httptest.NewRequest(http.MethodConnect, "evil.not-on-allowlist.test:443", nil)
	creq.Host = "evil.not-on-allowlist.test:443"
	crec := httptest.NewRecorder()
	srv.proxy.ServeHTTP(crec, creq)
	if crec.Code != http.StatusForbidden {
		t.Fatalf("default server must block CONNECT to non-allowlisted host, got %d", crec.Code)
	}
}
