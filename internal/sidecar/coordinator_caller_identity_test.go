package sidecar

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestProxyToAPI_PropagatesCallerUserID asserts the sidecar
// pass-through of X-Caller-User-Id from inbound chat-bridge/CLI
// request to outbound crewshipd request. Without this header the
// backend handler can't distinguish a user-initiated slash command
// from an autonomous agent tool call, and the dual-path enforcement
// collapses to the autonomy_level path for everyone.
func TestProxyToAPI_PropagatesCallerUserID(t *testing.T) {
	var seenCaller, seenSource, seenInternal, seenSig string
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenCaller = r.Header.Get("X-Caller-User-Id")
		seenSource = r.Header.Get("X-Caller-Source")
		seenInternal = r.Header.Get("X-Internal-Token")
		seenSig = r.Header.Get("X-Caller-Signature")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer mock.Close()

	srv := newQueryServer(t, &IPCConfig{BaseURL: mock.URL, Token: "tok", WorkspaceID: "ws-1"}, nil)

	req := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`{}`))
	req.Header.Set("X-Caller-User-Id", "ludmila-id")
	req.Header.Set("X-Caller-Source", "chat-ui")
	w := httptest.NewRecorder()

	srv.proxyToAPI(w, req, http.MethodPost, "/api/v1/internal/test")

	if seenCaller != "ludmila-id" {
		t.Errorf("X-Caller-User-Id: got %q, want ludmila-id", seenCaller)
	}
	if seenSource != "chat-ui" {
		t.Errorf("X-Caller-Source: got %q, want chat-ui", seenSource)
	}
	if seenInternal != "tok" {
		t.Errorf("X-Internal-Token regression: got %q, want tok", seenInternal)
	}
	// ID1: the sidecar must NOT sign the caller id on this hop. The port is
	// reachable by the (untrusted) agent, which can set any X-Caller-User-Id;
	// signing it here would make the sidecar a signing oracle and defeat the
	// backend's X-Caller-Signature gate entirely. The id is still forwarded for
	// non-privileged attribution, but no signature is attached — so the backend
	// rejects any privileged credential mutation that arrives through this path.
	if seenSig != "" {
		t.Errorf("X-Caller-Signature must NOT be stamped on the agent-reachable hop (signing oracle); got %q", seenSig)
	}
}

// TestProxyToAPI_OmitsHeadersWhenAbsent ensures we don't stamp empty
// headers onto the outbound request when the inbound didn't carry
// them — autonomous-agent path must look identical to pre-PR
// behaviour so the backend autonomy gate runs unchanged.
//
// assert upstreamReached so a future regression
// that short-circuits proxyToAPI before reaching upstream doesn't
// let the header-absence assertion pass vacuously.
func TestProxyToAPI_OmitsHeadersWhenAbsent(t *testing.T) {
	var hadCaller, hadSource, hadSig, upstreamReached bool
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamReached = true
		_, hadCaller = r.Header["X-Caller-User-Id"]
		_, hadSource = r.Header["X-Caller-Source"]
		_, hadSig = r.Header["X-Caller-Signature"]
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer mock.Close()

	srv := newQueryServer(t, &IPCConfig{BaseURL: mock.URL, Token: "tok", WorkspaceID: "ws-1"}, nil)

	req := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`{}`))
	w := httptest.NewRecorder()

	srv.proxyToAPI(w, req, http.MethodPost, "/api/v1/internal/test")

	if !upstreamReached {
		t.Fatal("proxyToAPI never reached upstream — header-absence assertion would pass vacuously")
	}
	if hadCaller {
		t.Error("X-Caller-User-Id leaked onto outbound when inbound didn't set it")
	}
	if hadSource {
		t.Error("X-Caller-Source leaked onto outbound when inbound didn't set it")
	}
	if hadSig {
		t.Error("X-Caller-Signature stamped without an inbound caller id — nothing to sign")
	}
}
