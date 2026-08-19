package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestCrewshipRoutePath_EscapedSeparatorDoesNotReroute closes the one gap in
// the existing TestCrewshipRoutePath, which asserts the escaping at the string
// level ("a/b" -> "a%2Fb") and the empty-value refusal.
//
// Neither is the load-bearing property. url.PathEscape only helps if the
// ROUTER treats %2F as data rather than as a separator — decode it before
// matching and the escaping buys nothing. So this routes the built path
// through a real http.ServeMux carrying both the intended route and one an
// attacker would want.
//
// It matters because CodeQL flags this path reaching
// http.NewRequestWithContext as go/request-forgery, critical (alert 823), and
// the request carries X-Internal-Token — the most privileged credential the
// process holds. Dismissing that alert is only honest with this test behind
// it; a dismissal with no test is a promise, not a control.
func TestCrewshipRoutePath_EscapedSeparatorDoesNotReroute(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/crews/{crew_id}/missions", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("intended:" + r.PathValue("crew_id")))
	})
	mux.HandleFunc("GET /api/v1/admin/danger", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("REROUTED"))
	})

	for _, arg := range []string{
		"../../admin/danger",
		"..%2F..%2Fadmin%2Fdanger",
		"/api/v1/admin/danger",
		"a/b/c",
	} {
		path, err := crewshipRoutePath("/api/v1/crews/{crew_id}/missions", map[string]any{"crew_id": arg})
		if err != nil {
			t.Fatalf("crewshipRoutePath(%q): %v", arg, err)
		}
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, httptest.NewRequest("GET", path, nil))

		if body := rr.Body.String(); body == "REROUTED" {
			t.Fatalf("arg %q reached the admin route via %q — the internal token would have gone with it", arg, path)
		} else if !strings.HasPrefix(body, "intended:") {
			t.Errorf("arg %q produced %q -> status %d body %q; expected the intended route", arg, path, rr.Code, body)
		}
	}
}
