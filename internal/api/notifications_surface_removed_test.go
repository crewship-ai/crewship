package api

import (
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
)

// The entity-scoped in-app notifications surface (#1751) was five ways to read
// and clear a table nothing ever wrote to: no create route existed, and the
// only insert helper in the tree had zero non-test callers in any released
// version. It is gone — handler, routes, table, CLI group.
//
// This is the guard against it returning half-wired. The failure the first
// version shipped was not "the code is wrong", it was "the readers exist and
// the writer does not", which every unit test passed happily. So the test is
// stated at the router: no path under /api/v1/notifications may be registered
// unless somebody deliberately deletes this test and says why.
//
// Companions: TestDropDeadNotifications_* (internal/database — the table stays
// dropped) and TestNotificationCommandStaysRemoved (cmd/crewship — no CLI
// group). One layer coming back alone now fails the test for the layer that
// did not.
func TestNotificationsSurfaceStaysRemoved(t *testing.T) {
	r, err := NewRouter(setupTestDB(t), "this-is-a-32-char-test-secret-pad", newTestLogger())
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}

	var offenders []string
	for path, methods := range r.mux.Routes() {
		// Exact-segment match: /api/v1/notification-channels and friends are
		// the LIVE outbound surface and must not be caught here.
		if path == "/api/v1/notifications" || strings.HasPrefix(path, "/api/v1/notifications/") {
			offenders = append(offenders, strings.Join(methods, ",")+" "+path)
		}
	}
	if len(offenders) > 0 {
		sort.Strings(offenders)
		t.Errorf("the router registers %v — the entity-scoped notifications surface was removed "+
			"in #1751 because it read a table with no writer. A per-user feed should be built on "+
			"notifyroute + inbox with a per-user filter, not by re-registering these routes.",
			offenders)
	}
}

// The live outbound-notification routes share the prefix and are NOT part of
// this removal. Pinning one of them keeps the guard above from being satisfied
// by an over-broad deletion later.
func TestOutboundNotificationRoutesSurvive(t *testing.T) {
	r, err := NewRouter(setupTestDB(t), "this-is-a-32-char-test-secret-pad", newTestLogger())
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	routes := r.mux.Routes()
	for _, want := range []string{
		"/api/v1/notification-channels",
		"/api/v1/notification-providers",
		"/api/v1/me/notification-prefs",
	} {
		if _, ok := routes[want]; !ok {
			t.Errorf("route %s is gone — #1751 removed the entity-scoped feed only; the "+
				"outbound channels are live", want)
		}
	}
}

// End of the same story on the wire: the removed paths must answer like paths
// the server does not serve, not like paths it serves and refuses. A 401 here
// would mean a handler is still mounted behind auth.
func TestRemovedNotificationPathsAre404(t *testing.T) {
	r, err := NewRouter(setupTestDB(t), "this-is-a-32-char-test-secret-pad", newTestLogger())
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}

	for _, probe := range []struct{ method, path string }{
		{"GET", "/api/v1/notifications"},
		{"GET", "/api/v1/notifications/count"},
		{"POST", "/api/v1/notifications/n1/read"},
		{"POST", "/api/v1/notifications/read-all"},
		{"DELETE", "/api/v1/notifications/n1"},
	} {
		req := httptest.NewRequest(probe.method, probe.path, nil)
		rr := httptest.NewRecorder()
		r.ServeHTTP(rr, req)
		if rr.Code != http.StatusNotFound {
			t.Errorf("%s %s = %d, want 404 — something is still mounted there",
				probe.method, probe.path, rr.Code)
		}
	}
}
