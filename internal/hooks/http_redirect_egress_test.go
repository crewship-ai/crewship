package hooks

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// TestHTTPHandlerCrewEgressRedirectBlocked is the redirect-bypass proof for
// #1367: a restricted crew's hook posts to an allowlisted host that 302-redirects
// to a NON-allowlisted host. Before the shared gated client, the handler's
// http.Client followed the redirect with no per-hop crew-allowlist re-check, so
// the signed hook body leaked to a host outside the crew boundary. Now the
// redirect is refused as a policy Block on the hop.
//
// (The stronger "no bytes reach the blocked host" proof — with the redirect
// target actually served — lives at the shared layer,
// egresspolicy.TestClient_CheckRedirect_ReGatesCrewAllowlist, which can route a
// second logical host to a loopback server via RewriteRoundTripper; the handler
// builds its client internally, so here we assert the hop is refused + not
// followed by classification.)
func TestHTTPHandlerCrewEgressRedirectBlocked(t *testing.T) {
	t.Setenv(allowPrivateEnvVar, "true") // let the SSRF dialer reach the loopback first hop
	db := openEgressTestDB(t)

	var collectHit int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/collect":
			atomic.AddInt32(&collectHit, 1)
			w.WriteHeader(http.StatusOK)
		default:
			// Redirect the allowlisted first hop to a host the crew does NOT allow.
			http.Redirect(w, r, "http://blocked.invalid/collect", http.StatusFound)
		}
	}))
	defer ts.Close()

	// crew_allowed allowlists 127.0.0.1 (the test server), so the first hop
	// passes the pre-flight check; the redirect target host is not allowlisted.
	h := Hook{
		HandlerKind:   HandlerKindHTTP,
		HandlerConfig: map[string]any{"url": ts.URL + "/start"},
	}
	res, err := httpHandler(context.Background(), db, h, EventContext{
		WorkspaceID: "ws_test",
		CrewID:      "crew_allowed",
	})
	if err == nil {
		t.Fatal("expected the cross-host redirect to be refused, got nil error")
	}
	if res.Outcome != OutcomeBlock {
		t.Errorf("outcome = %s, want Block (a refused redirect is a policy decision)", res.Outcome)
	}
	if !strings.Contains(res.Message, "redirect") {
		t.Errorf("message = %q, want a redirect-policy reason", res.Message)
	}
	if n := atomic.LoadInt32(&collectHit); n != 0 {
		t.Errorf("blocked redirect target was reached %d time(s); allowlist bypassed via redirect", n)
	}
}
