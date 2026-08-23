package sidecar

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The sidecar's local control plane is selected by Host header, and a Host
// header is attacker-controllable. On a shared docker bridge a container in a
// PEER crew can dial this port and present `Host: localhost:9119`
// (`curl --resolve localhost:9119:172.18.0.5 …`), which is why the branch at
// proxy.go requires the TCP source to be loopback as well — the same pair
// buildHandler has enforced since Patch-E (#2039).
//
// remoteIsLoopback has its own unit test and buildHandler has
// TestBuildHandler_CrossCrewBypassRejected, but nothing pinned THIS wiring:
// delete `&& remoteIsLoopback(r)` from proxy.go and every existing test still
// passes, because the tests that reach these routes all set a loopback
// RemoteAddr and a guard that never runs cannot fail them. A security gate with
// no test is one revert away from being gone, so this is that test.
func TestProxyLocalRoutes_RefusePeerCrewWithSpoofedHost(t *testing.T) {
	t.Parallel()

	// Only the two health routes, and the omission is deliberate. handleLocal
	// also claims the llmroute paths, but a request to one of those fails for
	// its own reasons with or without the guard — it proved nothing, and a
	// subtest that cannot fail is worse than no subtest because it reads as
	// coverage. These two discriminate: with the guard removed they answer 200
	// with the control-plane body (credential counts per provider, the sidecar
	// build hash, the domains hash) to a caller on the shared bridge.
	for _, path := range []string{"/health", "/healthz"} {
		t.Run(path, func(t *testing.T) {
			t.Parallel()

			proxy := newTestProxy(nil, []string{"localhost"})
			proxy.buildHash = "deadbeef1234"

			req := httptest.NewRequest("GET", "http://localhost:9119"+path, nil)
			req.Host = "localhost:9119"
			// The whole point: the Host header says localhost, the packet did
			// not come from loopback. 172.18.0.5 is a docker bridge peer.
			req.RemoteAddr = "172.18.0.5:54321"

			w := httptest.NewRecorder()
			proxy.ServeHTTP(w, req)

			// The request must fall through to the forward-proxy path, which
			// refuses it (non-allowed domain / unreachable upstream). What it
			// must NOT do is answer as the control plane.
			if w.Code == http.StatusOK {
				t.Errorf("peer-crew request to %s was answered 200 — the Host header alone selected the "+
					"local control plane; body=%q", path, w.Body.String())
			}
			if body := w.Body.String(); strings.Contains(body, "sidecar_hash") || strings.Contains(body, `"status"`) {
				t.Errorf("peer-crew request to %s received a control-plane response body: %q", path, body)
			}
		})
	}
}

// The other direction, so the test above cannot pass by the routes being broken
// for everyone: the identical request from loopback is still served.
func TestProxyLocalRoutes_StillServeLoopback(t *testing.T) {
	t.Parallel()

	proxy := newTestProxy(nil, []string{"localhost"})
	proxy.buildHash = "deadbeef1234"

	req := httptest.NewRequest("GET", "http://localhost:9119/health", nil)
	req.Host = "localhost:9119"
	req.RemoteAddr = "127.0.0.1:54321"

	w := httptest.NewRecorder()
	proxy.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("loopback health = %d, want 200 — the guard must not close the door on the real caller", w.Code)
	}
	if body := w.Body.String(); !strings.Contains(body, "sidecar_hash") {
		t.Errorf("loopback health body is not the control-plane response: %q", body)
	}
}
