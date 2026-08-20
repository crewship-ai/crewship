package clitest

import (
	"io"
	"net/http"
	"net/http/httptrace"
	"testing"
)

// deleteReusedConn issues a DELETE against the stub through c and reports
// whether the transport served it from its idle connection pool
// (httptrace.GotConnInfo.Reused) rather than dialling afresh.
func deleteReusedConn(t *testing.T, c *http.Client, url string) bool {
	t.Helper()
	req, err := http.NewRequest(http.MethodDelete, url, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	var reused bool
	req = req.WithContext(httptrace.WithClientTrace(req.Context(), &httptrace.ClientTrace{
		GotConn: func(gi httptrace.GotConnInfo) { reused = gi.Reused },
	}))
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("DELETE %s: %v", url, err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	return reused
}

// TestStubServer_ConnPoolSurvivesSiblingClose pins the isolation property
// that #2041 was about.
//
// httptest.Server.Close() does not only shut down its own listener — it
// also calls http.DefaultTransport.CloseIdleConnections(), on the
// assumption that "most users of httptest.Server will be using the
// standard transport". StubServer.Close delegates to it, so every
// `defer s.Close()` in this package detonates the process-global
// connection pool for whatever else is in flight.
//
// A test that rides http.DefaultClient (directly, or via the http.Get /
// http.Post package-level wrappers, which are the same client) therefore
// has its pooled connection yanked by unrelated parallel siblings. When
// the yank lands mid-request on a non-replayable verb — DELETE, POST —
// net/http cannot retry and the request dies with "net/http: HTTP/1.x
// transport connection broken: http: CloseIdleConnections called".
//
// The mid-flight symptom is timing-dependent, so this test pins the
// deterministic coupling that causes it: after an unrelated server
// closes, is our pooled connection still there? Through a dedicated
// per-server client it must be, because Server.Close touches only its own
// transport and DefaultTransport — never another server's.
func TestStubServer_ConnPoolSurvivesSiblingClose(t *testing.T) {
	t.Parallel()
	s := NewStubServer()
	defer s.Close()
	s.OnDelete("/x", EmptyResponse(204))

	c := s.Client()

	// Warm the pool, then confirm the connection really is being reused —
	// otherwise the assertion below would pass vacuously.
	deleteReusedConn(t, c, s.URL()+"/x")
	if !deleteReusedConn(t, c, s.URL()+"/x") {
		t.Fatal("precondition: second DELETE should have reused the pooled connection")
	}

	// A completely unrelated stub server closing. Nothing about it refers
	// to s, and no request of ours is addressed to it.
	sibling := NewStubServer()
	sibling.Close()

	if !deleteReusedConn(t, c, s.URL()+"/x") {
		t.Error("an unrelated StubServer.Close() evicted this test's pooled connection: " +
			"the request rode the process-global transport instead of a per-server client")
	}
}

// TestStubServer_ClientHasPrivateTransport is the cheap structural half of
// the guard above: it fails immediately if StubServer.Client is ever
// "simplified" into returning http.DefaultClient, without waiting for the
// pool-eviction behaviour to be exercised.
func TestStubServer_ClientHasPrivateTransport(t *testing.T) {
	t.Parallel()
	s := NewStubServer()
	defer s.Close()

	c := s.Client()
	if c == nil {
		t.Fatal("StubServer.Client() returned nil")
	}
	if c == http.DefaultClient {
		t.Error("StubServer.Client() must not be http.DefaultClient — parallel tests would share one pool")
	}
	if c.Transport == nil {
		t.Error("StubServer.Client().Transport is nil, which means http.DefaultTransport — the shared pool")
	}
	if c.Transport == http.DefaultTransport {
		t.Error("StubServer.Client() must not ride http.DefaultTransport: any sibling " +
			"httptest.Server.Close() calls CloseIdleConnections() on it")
	}

	// Two stubs must not share a transport either.
	other := NewStubServer()
	defer other.Close()
	if other.Client().Transport == c.Transport {
		t.Error("two StubServers share a transport; each must get its own pool")
	}
}
