package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/logging"
)

// docsServer builds a server whose SPA handler announces itself, so any path
// that falls through to it is unmistakable in the assertion below.
func docsServer(t *testing.T) *Server {
	t.Helper()
	cfg := silentCfg()
	logger := logging.New("error", "json", nil)
	s := New(cfg, logger, &Deps{DB: openTestDB(t)})
	t.Cleanup(s.StopBackground)
	s.startedAt = time.Now()
	s.spaHandler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("<!doctype html><html><body>SPA-INDEX</body></html>"))
	})
	return s
}

// TestOpenAPIDocs_NotSwallowedBySPA is the trap /openapi.json fell into before
// #1325: the embedded Next.js catch-all answers every unmatched path with its
// index.html, so a docs route that is not explicitly mux-routed returns 200
// text/html — indistinguishable from a working page at a glance, and carrying
// none of the spec. The docs surface must be routed to the mux, at every one
// of its paths, including its assets.
func TestOpenAPIDocs_NotSwallowedBySPA(t *testing.T) {
	t.Parallel()
	s := docsServer(t)
	combined := s.combinedHandler()

	for _, tc := range []struct {
		path  string
		want  string // a fragment only the real rendering produces
		ctype string
	}{
		{"/openapi", "openapi.json", "text/html"},
		{"/openapi/", "openapi.json", "text/html"},
		{"/openapi/ui.css", "--line", "text/css"},
		{"/openapi/ui.js", "op-filter", "javascript"},
		{"/openapi/schemas", "component schemas", "text/html"},
		{"/openapi/op/post_api_v1_crews", "/api/v1/crews", "text/html"},
	} {
		rec := httptest.NewRecorder()
		combined.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
		body := rec.Body.String()

		if strings.Contains(body, "SPA-INDEX") {
			t.Errorf("GET %s fell through to the SPA catch-all", tc.path)
			continue
		}
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", tc.path, rec.Code)
			continue
		}
		if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, tc.ctype) {
			t.Errorf("GET %s Content-Type = %q, want to contain %q", tc.path, ct, tc.ctype)
		}
		if !strings.Contains(body, tc.want) {
			t.Errorf("GET %s does not look like the rendering: missing %q", tc.path, tc.want)
		}
	}

	// The reverse direction of the same trap: adding a docs surface at
	// /openapi must not shadow the JSON the tooling reads.
	rec := httptest.NewRecorder()
	combined.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/openapi.json", nil))
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("/openapi.json Content-Type = %q, want application/json", ct)
	}
	if !strings.Contains(rec.Body.String(), `"openapi"`) {
		t.Error("/openapi.json no longer serves the spec document")
	}

	// A page under the docs prefix that does not exist must 404, not render
	// something. A 200 for every name is how a docs surface starts lying.
	rec404 := httptest.NewRecorder()
	combined.ServeHTTP(rec404, httptest.NewRequest(http.MethodGet, "/openapi/op/nope", nil))
	if rec404.Code != http.StatusNotFound {
		t.Errorf("GET /openapi/op/nope = %d, want 404", rec404.Code)
	}
}

// TestOpenAPIDocs_CSPAllowsOnlyThisInstance is the offline guarantee expressed
// as a header: with default-src 'none' and only 'self' for script and style, a
// browser refuses to load a CDN bundle, a remote font or a tracking pixel even
// if one is ever added to these pages by accident. It is also the reason the
// rendering carries no inline script or style — those need 'unsafe-inline',
// which this policy does not grant.
func TestOpenAPIDocs_CSPAllowsOnlyThisInstance(t *testing.T) {
	t.Parallel()
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	wrapped := securityHeadersMiddleware(inner)

	for _, path := range []string{"/openapi", "/openapi/ui.css", "/openapi/op/get_api_health"} {
		rec := httptest.NewRecorder()
		wrapped.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		csp := rec.Header().Get("Content-Security-Policy")

		for _, want := range []string{"default-src 'none'", "script-src 'self'", "style-src 'self'"} {
			if !strings.Contains(csp, want) {
				t.Errorf("%s CSP = %q, want it to contain %q", path, csp, want)
			}
		}
		for _, unwanted := range []string{"unsafe-inline", "unsafe-eval", "http:", "https:"} {
			if strings.Contains(csp, unwanted) {
				t.Errorf("%s CSP = %q, must not contain %q", path, csp, unwanted)
			}
		}
	}

	// The SPA keeps its own, looser policy — the docs case must not have
	// been implemented by widening the strict branch for everyone.
	rec := httptest.NewRecorder()
	wrapped.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/crews", nil))
	if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "unsafe-inline") {
		t.Errorf("SPA CSP changed: %q", csp)
	}
}
