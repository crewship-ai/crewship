package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestRouteMux_Handler_EmptyPatternMeansTheMuxAnswersItself pins the contract
// both consumers of routeMux.Handler rely on, and that neither of them can
// assert on its own.
//
// net/http reports an EMPTY pattern for the two answers ServeMux produces by
// itself — 404 not-found and 405 method-not-allowed — and a non-empty one only
// when a registered handler would really receive the request. sealMethodGuards
// reads it as "is there a hole here". The internal-surface fence (#1501,
// router_internal_fence.go) reads the same call as "would this dispatch", and
// answers everything else with one indistinguishable 404 so the sidecar
// surface cannot be mapped by probing.
//
// The wrapper must therefore keep forwarding Handler. Dropping it does not
// merely lose a convenience — it takes the method away from the fence, and the
// build breaks rather than the fence silently opening, which is the outcome
// worth pinning.
func TestRouteMux_Handler_EmptyPatternMeansTheMuxAnswersItself(t *testing.T) {
	m := newRouteMux()
	m.HandleFunc("POST /thing", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	m.HandleFunc("GET /other/{id}", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	for _, tc := range []struct {
		name    string
		method  string
		target  string
		wantPat string
	}{
		{"registered method on a registered path", http.MethodPost, "/thing", "POST /thing"},
		{"HEAD rides the GET registration", http.MethodHead, "/other/7", "GET /other/{id}"},
		{"method mismatch reports no pattern", http.MethodGet, "/thing", ""},
		{"unknown path reports no pattern", http.MethodGet, "/nope", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.target, nil)
			_, pattern := m.Handler(req)
			if pattern != tc.wantPat {
				t.Errorf("Handler pattern = %q, want %q", pattern, tc.wantPat)
			}
			// The forwarded answer must agree with the embedded mux, or the
			// two consumers are reasoning about different route tables.
			if _, direct := m.mux.Handler(req); direct != pattern {
				t.Errorf("forwarded pattern %q != embedded mux pattern %q", pattern, direct)
			}
		})
	}
}
