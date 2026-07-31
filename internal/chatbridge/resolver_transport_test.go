package chatbridge

import (
	"log/slog"
	"net/http"
	"testing"
)

// The resolver must not dial on http.DefaultTransport.
//
// This looks like a style preference and is not. httptest.Server.Close() ends
// with, verbatim from net/http/httptest/server.go:
//
//	// Not part of httptest.Server's correctness, but assume most
//	// users of httptest.Server will be using the standard
//	// transport, so help them out and close any idle connections for them.
//	if t, ok := http.DefaultTransport.(closeIdleTransport); ok {
//		t.CloseIdleConnections()
//	}
//
// So EVERY httptest server anywhere in the test binary, on close, reaches into
// the process-global pool and closes idle connections belonging to everybody.
// A resolver built with `&http.Client{Timeout: ...}` and no Transport shares
// that pool, so a parallel test's cleanup can pull the connection out from
// under an in-flight request the moment it is taken from the pool:
//
//	--- FAIL: TestIncrementMessageCount
//	    Patch "…/message-count": net/http: HTTP/1.x transport connection
//	    broken: http: CloseIdleConnections called
//
// which is how it failed on the linux-arm64 runner (#1553, job 91000482088) —
// slower box, wider window, same bug everywhere else.
//
// Owning the transport is also the right production shape: a component with
// its own 10s timeout should not have its connection pool sized, tuned or
// drained by whatever else in the process happens to touch the default.
func TestResolverDoesNotShareTheGlobalTransport(t *testing.T) {
	t.Parallel()
	r := NewIPCResolver("http://127.0.0.1:1", "tok", slog.Default())

	if r.httpClient.Transport == nil {
		t.Fatal("resolver uses http.DefaultTransport (Transport is nil) — a parallel test's httptest.Server.Close() can close its connections mid-request")
	}
	if r.httpClient.Transport == http.DefaultTransport {
		t.Fatal("resolver was handed http.DefaultTransport explicitly — same exposure, stated out loud")
	}
}
