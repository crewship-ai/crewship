package api

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

// ---------------------------------------------------------------------------
// router_mux.go — the recording mux and the method guards it synthesizes.
//
// These are white-box unit tests on a mux built from scratch, so the shadowing
// scenario is visible in three lines instead of buried in 500 real routes.
// The whole-router assertions live in openapi_conformance_test.go.
// ---------------------------------------------------------------------------

func TestSplitPattern(t *testing.T) {
	cases := []struct{ in, method, path string }{
		{"POST /api/v1/agents", "POST", "/api/v1/agents"},
		{"GET /api/v1/agents/{id}", "GET", "/api/v1/agents/{id}"},
		{"/exposed/{token...}", "", "/exposed/{token...}"},
		{"DELETE  /spaced", "DELETE", "/spaced"},
	}
	for _, c := range cases {
		method, path := splitPattern(c.in)
		if method != c.method || path != c.path {
			t.Errorf("splitPattern(%q) = (%q, %q), want (%q, %q)", c.in, method, path, c.method, c.path)
		}
	}
}

func TestNormalizeShape(t *testing.T) {
	// Different wildcard names are the SAME pattern to ServeMux; the shape key
	// has to collapse them or the guard pass registers a duplicate and panics.
	if a, b := normalizeShape("/a/{id}/b"), normalizeShape("/a/{agentId}/b"); a != b {
		t.Errorf("normalizeShape disagrees on equivalent patterns: %q vs %q", a, b)
	}
	// A multi-segment wildcard is NOT the same pattern as a single-segment one.
	if a, b := normalizeShape("/a/{id}"), normalizeShape("/a/{id...}"); a == b {
		t.Errorf("normalizeShape collapsed {id} and {id...} to %q", a)
	}
	if got := normalizeShape("/a/b/c"); got != "/a/b/c" {
		t.Errorf("normalizeShape on a literal path = %q, want it unchanged", got)
	}
}

func TestAllowHeaderValue(t *testing.T) {
	cases := []struct {
		name string
		set  map[string]bool
		want string
	}{
		{"GET implies HEAD", map[string]bool{"GET": true}, "GET, HEAD"},
		{"sorted", map[string]bool{"POST": true, "DELETE": true}, "DELETE, POST"},
		{"explicit HEAD not duplicated", map[string]bool{"GET": true, "HEAD": true}, "GET, HEAD"},
		{"method-less entry ignored", map[string]bool{"": true, "PUT": true}, "PUT"},
	}
	for _, c := range cases {
		if got := allowHeaderValue(c.set); got != c.want {
			t.Errorf("%s: allowHeaderValue = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestRouteMux_Routes(t *testing.T) {
	m := newRouteMux()
	ok := func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }
	m.HandleFunc("GET /things", ok)
	m.HandleFunc("POST /things", ok)
	m.HandleFunc("GET /things/{id}", ok)
	m.HandleFunc("/anything", ok)

	got := m.Routes()
	want := map[string][]string{
		"/things":      {"GET", "POST"},
		"/things/{id}": {"GET"},
		"/anything":    {"*"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Routes() = %v, want %v", got, want)
	}
}

// TestRouteMux_SealMethodGuards_ClosesWildcardShadow is the unit-level
// reproduction of #1489: without the guard, DELETE /things/count runs the
// delete-by-id handler with id="count".
func TestRouteMux_SealMethodGuards_ClosesWildcardShadow(t *testing.T) {
	m := newRouteMux()
	reached := ""
	m.HandleFunc("GET /things/count", func(w http.ResponseWriter, _ *http.Request) {
		reached = "count"
		w.WriteHeader(http.StatusOK)
	})
	m.HandleFunc("DELETE /things/{id}", func(w http.ResponseWriter, r *http.Request) {
		reached = "delete:" + r.PathValue("id")
		w.WriteHeader(http.StatusOK)
	})

	// Before sealing: the shadow is real.
	rr := httptest.NewRecorder()
	m.ServeHTTP(rr, httptest.NewRequest(http.MethodDelete, "/things/count", nil))
	if reached != "delete:count" {
		t.Fatalf("precondition failed: DELETE /things/count reached %q, expected the wildcard handler "+
			"(if Go's routing changed, this whole guard mechanism may be unnecessary)", reached)
	}

	m.sealMethodGuards()

	reached = ""
	rr = httptest.NewRecorder()
	m.ServeHTTP(rr, httptest.NewRequest(http.MethodDelete, "/things/count", nil))
	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("DELETE /things/count = %d, want 405", rr.Code)
	}
	if got := rr.Header().Get("Allow"); got != "GET, HEAD" {
		t.Errorf("Allow = %q, want %q", got, "GET, HEAD")
	}
	if reached != "" {
		t.Errorf("a handler ran (%q); the guard must short-circuit", reached)
	}

	// The declared methods still work.
	reached = ""
	rr = httptest.NewRecorder()
	m.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/things/count", nil))
	if rr.Code != http.StatusOK || reached != "count" {
		t.Errorf("GET /things/count = %d (reached %q), want 200 via the count handler", rr.Code, reached)
	}
	reached = ""
	rr = httptest.NewRecorder()
	m.ServeHTTP(rr, httptest.NewRequest(http.MethodDelete, "/things/abc", nil))
	if rr.Code != http.StatusOK || reached != "delete:abc" {
		t.Errorf("DELETE /things/abc = %d (reached %q), want 200 via the wildcard handler", rr.Code, reached)
	}
}

// TestRouteMux_SealMethodGuards_LeavesUnshadowedPathsToServeMux keeps the guard
// pass minimal: where ServeMux already answers 405 on its own, no pattern is
// added.
func TestRouteMux_SealMethodGuards_LeavesUnshadowedPathsToServeMux(t *testing.T) {
	m := newRouteMux()
	m.HandleFunc("GET /solo", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	m.sealMethodGuards()

	req := httptest.NewRequest(http.MethodPost, "/solo", nil)
	if _, pattern := m.mux.Handler(req); pattern != "" {
		t.Errorf("POST /solo resolved to %q; an unshadowed path needs no guard", pattern)
	}
	rr := httptest.NewRecorder()
	m.ServeHTTP(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /solo = %d, want ServeMux's own 405", rr.Code)
	}
}

// TestRouteMux_SealMethodGuards_SkipsMethodLessRegistrations — a pattern
// registered without a method (the /exposed/{token...} reverse-proxy mount)
// already owns every method; guarding it would 405 the proxy.
func TestRouteMux_SealMethodGuards_SkipsMethodLessRegistrations(t *testing.T) {
	m := newRouteMux()
	m.HandleFunc("/proxy/{token...}", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })
	m.HandleFunc("DELETE /proxy/{id}", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	m.sealMethodGuards()

	rr := httptest.NewRecorder()
	m.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/proxy/anything", nil))
	if rr.Code != http.StatusTeapot {
		t.Errorf("POST /proxy/anything = %d, want 418 — the method-less mount must keep every method", rr.Code)
	}
}

// TestRouteMux_SealMethodGuards_HeadFollowsGet — ServeMux answers HEAD from a
// GET registration, so a HEAD guard on a GET route would break it.
func TestRouteMux_SealMethodGuards_HeadFollowsGet(t *testing.T) {
	m := newRouteMux()
	m.HandleFunc("GET /things/count", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	m.HandleFunc("GET /things/{id}", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	m.sealMethodGuards()

	rr := httptest.NewRecorder()
	m.ServeHTTP(rr, httptest.NewRequest(http.MethodHead, "/things/count", nil))
	if rr.Code != http.StatusOK {
		t.Errorf("HEAD /things/count = %d, want 200 (served by the GET registration)", rr.Code)
	}
}

// TestRouteMux_SealMethodGuards_Idempotent — a second call must be a no-op,
// not a duplicate-pattern panic inside ServeMux.
func TestRouteMux_SealMethodGuards_Idempotent(t *testing.T) {
	m := newRouteMux()
	m.HandleFunc("GET /things/count", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	m.HandleFunc("DELETE /things/{id}", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	m.sealMethodGuards()
	m.sealMethodGuards()

	rr := httptest.NewRecorder()
	m.ServeHTTP(rr, httptest.NewRequest(http.MethodDelete, "/things/count", nil))
	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("DELETE /things/count = %d, want 405 after a double seal", rr.Code)
	}
}

// TestRouteMux_SealMethodGuards_SharedShapeDifferentSpelling — two registrars
// spelling one pattern differently ({id} vs {agentId}) must produce ONE guard.
// Registering both spellings would panic ServeMux with a pattern conflict.
func TestRouteMux_SealMethodGuards_SharedShapeDifferentSpelling(t *testing.T) {
	m := newRouteMux()
	m.HandleFunc("GET /a/{id}/x", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	m.HandleFunc("POST /a/{agentId}/x", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	m.HandleFunc("DELETE /a/{id}/{rest}", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	defer func() {
		if rec := recover(); rec != nil {
			t.Fatalf("sealMethodGuards panicked on two spellings of one shape: %v", rec)
		}
	}()
	m.sealMethodGuards()

	rr := httptest.NewRecorder()
	m.ServeHTTP(rr, httptest.NewRequest(http.MethodDelete, "/a/1/x", nil))
	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("DELETE /a/1/x = %d, want 405", rr.Code)
	}
	if got := rr.Header().Get("Allow"); got != "GET, HEAD, POST" {
		t.Errorf("Allow = %q, want %q — both spellings' methods must be listed", got, "GET, HEAD, POST")
	}
}
