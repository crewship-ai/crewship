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
	var seenCaller, seenSource, seenInternal string
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenCaller = r.Header.Get("X-Caller-User-Id")
		seenSource = r.Header.Get("X-Caller-Source")
		seenInternal = r.Header.Get("X-Internal-Token")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer mock.Close()

	srv := newQueryServer(t, &IPCConfig{BaseURL: mock.URL, Token: "tok"}, nil)

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
}

// TestProxyToAPI_OmitsHeadersWhenAbsent ensures we don't stamp empty
// headers onto the outbound request when the inbound didn't carry
// them — autonomous-agent path must look identical to pre-PR
// behaviour so the backend autonomy gate runs unchanged.
func TestProxyToAPI_OmitsHeadersWhenAbsent(t *testing.T) {
	var hadCaller, hadSource bool
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, hadCaller = r.Header["X-Caller-User-Id"]
		_, hadSource = r.Header["X-Caller-Source"]
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer mock.Close()

	srv := newQueryServer(t, &IPCConfig{BaseURL: mock.URL, Token: "tok"}, nil)

	req := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`{}`))
	w := httptest.NewRecorder()

	srv.proxyToAPI(w, req, http.MethodPost, "/api/v1/internal/test")

	if hadCaller {
		t.Error("X-Caller-User-Id leaked onto outbound when inbound didn't set it")
	}
	if hadSource {
		t.Error("X-Caller-Source leaked onto outbound when inbound didn't set it")
	}
}
