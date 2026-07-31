package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Spec conformance (#1489).
//
// GET /openapi.json is how an agent learns what this API accepts. Two ways
// that answer can be wrong, and this file guards both:
//
//  1. The spec lists an operation the router does not serve, or the router
//     serves one the spec omits (TestOpenAPISpecMatchesRegisteredRoutes).
//     The CI "openapi.gen.json is stale" step only proves the JSON matches
//     what cmd/gen-openapi's REGEX found in router_*.go — it cannot see a
//     registration shape the regex misses, a route registered from a file
//     the glob does not cover, or a commented-out registration the scan
//     picks up anyway (router_auth.go carries a warning about exactly that).
//     This test compares the spec against the routes the mux really has.
//
//  2. A method the spec does not list is silently accepted instead of
//     answering 405 (TestRegisteredPathsRejectUnlistedMethods). Go 1.22
//     routing matches method and path together, so an unlisted method on a
//     literal path can fall through to a sibling wildcard — see
//     router_mux.go.
// ---------------------------------------------------------------------------

// specWildcard matches a ServeMux path parameter, including the trailing-"..."
// multi-segment form. cmd/gen-openapi renders "{token...}" as "{token}" (OpenAPI
// has no multi-segment equivalent), so the comparison applies the same rewrite.
var specWildcard = regexp.MustCompile(`\{([A-Za-z0-9_]+)(\.\.\.)?\}`)

// conditionalSpecRoutes are operations that appear in openapi.gen.json — the
// generator scans source, so it sees every registration unconditionally — but
// that a Router built with no options does not register, because their
// registration is guarded by a dependency the bare constructor has no way to
// supply.
//
// Keep this list EXACT. The test fails both when an entry stops being
// conditional (stale allowance to delete) and when a new unregistered
// operation appears (real drift to fix), so the list cannot quietly grow into
// a blanket exemption.
var conditionalSpecRoutes = map[string][]string{
	// router_orchestration.go: registered only when the orchestrator, keeper
	// container provider, log writer AND internal token are all wired —
	// i.e. a real server boot, never a bare NewRouter.
	"/api/v1/webhooks/{crewId}/{agentId}/trigger": {"POST"},
}

// loadGeneratedSpec reads the embedded spec as path -> set of upper-case
// methods.
func loadGeneratedSpec(t *testing.T) map[string]map[string]bool {
	t.Helper()
	raw, err := os.ReadFile("openapi.gen.json")
	if err != nil {
		t.Fatalf("read openapi.gen.json: %v", err)
	}
	var doc struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse openapi.gen.json: %v", err)
	}
	out := make(map[string]map[string]bool, len(doc.Paths))
	for path, ops := range doc.Paths {
		methods := make(map[string]bool, len(ops))
		for method := range ops {
			methods[strings.ToUpper(method)] = true
		}
		out[path] = methods
	}
	return out
}

// registeredPublicRoutes reports the router's route table in spec shape,
// applying the same two exclusions cmd/gen-openapi applies: the
// /exposed/{token...} reverse-proxy mount (no fixed method or response shape)
// and the sidecar-only /api/v1/internal/* surface (deliberately absent from a
// publicly served spec).
func registeredPublicRoutes(r *Router) map[string]map[string]bool {
	out := map[string]map[string]bool{}
	for path, methods := range r.mux.Routes() {
		if strings.HasPrefix(path, "/exposed/") || strings.HasPrefix(path, "/api/v1/internal/") {
			continue
		}
		key := specWildcard.ReplaceAllString(path, "{$1}")
		if out[key] == nil {
			out[key] = map[string]bool{}
		}
		for _, method := range methods {
			out[key][method] = true
		}
	}
	return out
}

func sortedKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func TestOpenAPISpecMatchesRegisteredRoutes(t *testing.T) {
	r, err := NewRouter(setupTestDB(t), "this-is-a-32-char-test-secret-pad", newTestLogger())
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}

	spec := loadGeneratedSpec(t)
	registered := registeredPublicRoutes(r)

	paths := map[string]bool{}
	for p := range spec {
		paths[p] = true
	}
	for p := range registered {
		paths[p] = true
	}

	// Every conditional allowance must still be an allowance worth having.
	unusedConditional := map[string]bool{}
	for p := range conditionalSpecRoutes {
		unusedConditional[p] = true
	}

	for _, path := range sortedKeys(paths) {
		specMethods, registeredMethods := spec[path], registered[path]

		conditional := map[string]bool{}
		for _, m := range conditionalSpecRoutes[path] {
			conditional[m] = true
		}

		var inSpecOnly, inRouterOnly []string
		for method := range specMethods {
			if registeredMethods[method] {
				continue
			}
			if conditional[method] {
				delete(unusedConditional, path)
				continue
			}
			inSpecOnly = append(inSpecOnly, method)
		}
		for method := range registeredMethods {
			if !specMethods[method] {
				inRouterOnly = append(inRouterOnly, method)
			}
		}
		sort.Strings(inSpecOnly)
		sort.Strings(inRouterOnly)

		if len(inSpecOnly) > 0 {
			t.Errorf("%s: openapi.gen.json documents %v but the router registers no such route — "+
				"a client following the spec gets 404/405. Remove it from the spec or register the route.",
				path, inSpecOnly)
		}
		if len(inRouterOnly) > 0 {
			t.Errorf("%s: the router serves %v but openapi.gen.json does not document it — "+
				"run `go generate ./internal/api/` (and if that does not pick it up, cmd/gen-openapi's "+
				"registration-shape regexes need extending).",
				path, inRouterOnly)
		}
	}

	for path := range unusedConditional {
		t.Errorf("conditionalSpecRoutes[%q] is stale: the bare router now registers it. Delete the entry.", path)
	}
}

// TestRegisteredPathsRejectUnlistedMethods is the durable form of the
// schemathesis "Unsupported methods" finding: for every registered path, a
// method the path does not declare must not reach a handler. Before the method
// guards, 29 literal paths leaked into a sibling wildcard route — e.g.
// DELETE /api/v1/notifications/count ran the delete-notification handler with
// id="count" instead of answering 405.
//
// Resolution is checked with ServeMux.Handler, which reports the pattern a
// request WOULD reach without running it — no handler side effects, and the
// failure message names the exact pattern that captured the request.
func TestRegisteredPathsRejectUnlistedMethods(t *testing.T) {
	r, err := NewRouter(setupTestDB(t), "this-is-a-32-char-test-secret-pad", newTestLogger())
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}

	for _, shape := range r.mux.shapeOrder {
		declared := r.mux.shapeMethods[shape]
		if declared[""] {
			// Registered without a method — owns every method by design.
			continue
		}
		spelling := r.mux.shapeSpelling[shape]
		probePath := pathParamSub.ReplaceAllString(spelling, guardProbeSegment)

		for _, method := range guardedMethods {
			if declared[method] {
				continue
			}
			if method == http.MethodHead && declared[http.MethodGet] {
				continue // ServeMux answers HEAD from the GET registration.
			}
			req, err := http.NewRequest(method, probePath, nil)
			if err != nil {
				t.Fatalf("build probe request %s %s: %v", method, probePath, err)
			}
			_, pattern := r.mux.mux.Handler(req)
			if pattern == "" {
				continue // ServeMux's own 405 — correct.
			}
			if pattern == method+" "+spelling {
				continue // Our method guard — correct.
			}
			if capturedMethod, _ := splitPattern(pattern); capturedMethod == "" {
				continue // A method-less mount that owns every method by design.
			}
			t.Errorf("%s %s resolves to %q; it must answer 405, not fall through to another route",
				method, spelling, pattern)
		}
	}
}

// TestMethodGuard_Answers405WithAllow pins the wire behaviour of a guarded
// route end to end: status, the RFC 9110 Allow header (a 405 without it is
// itself a spec-conformance failure), and the shared JSON error envelope.
func TestMethodGuard_Answers405WithAllow(t *testing.T) {
	r, err := NewRouter(setupTestDB(t), "this-is-a-32-char-test-secret-pad", newTestLogger())
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}

	cases := []struct {
		method, path, wantAllow string
	}{
		// Shadowed by DELETE /api/v1/notifications/{id}.
		{http.MethodDelete, "/api/v1/notifications/count", "GET, HEAD"},
		// Shadowed by GET /api/v1/agents/{id}.
		{http.MethodGet, "/api/v1/agents/hire", "POST"},
		// Shadowed by PATCH /api/v1/inbox/{id}.
		{http.MethodPatch, "/api/v1/inbox/count", "GET, HEAD"},
		// Shadowed by PUT /api/v1/credentials/{id}.
		{http.MethodPut, "/api/v1/credentials/test", "POST"},
	}

	for _, c := range cases {
		t.Run(c.method+" "+c.path, func(t *testing.T) {
			req := httptest.NewRequest(c.method, c.path, nil)
			rr := httptest.NewRecorder()
			r.mux.ServeHTTP(rr, req)

			if rr.Code != http.StatusMethodNotAllowed {
				t.Fatalf("status = %d, want 405 (body %q) — the request reached a handler instead of the method guard",
					rr.Code, rr.Body.String())
			}
			if got := rr.Header().Get("Allow"); got != c.wantAllow {
				t.Errorf("Allow = %q, want %q", got, c.wantAllow)
			}
			var body map[string]string
			if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
				t.Fatalf("body is not the shared JSON error envelope: %v (%q)", err, rr.Body.String())
			}
			if body["error"] != "method_not_allowed" {
				t.Errorf("error = %q, want method_not_allowed", body["error"])
			}
		})
	}
}

// TestMethodGuard_DeclaredMethodsStillReachHandlers is the other half of the
// guarantee: adding the guards must not have shadowed a real route. Every
// declared method on every registered path must still resolve to its own
// pattern.
func TestMethodGuard_DeclaredMethodsStillReachHandlers(t *testing.T) {
	r, err := NewRouter(setupTestDB(t), "this-is-a-32-char-test-secret-pad", newTestLogger())
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}

	for _, shape := range r.mux.shapeOrder {
		spelling := r.mux.shapeSpelling[shape]
		probePath := pathParamSub.ReplaceAllString(spelling, guardProbeSegment)
		for method := range r.mux.shapeMethods[shape] {
			if method == "" {
				continue
			}
			req, err := http.NewRequest(method, probePath, nil)
			if err != nil {
				t.Fatalf("build probe request %s %s: %v", method, probePath, err)
			}
			_, pattern := r.mux.mux.Handler(req)
			if pattern != method+" "+spelling {
				t.Errorf("%s %s resolves to %q, want its own registration — a method guard is shadowing a real route",
					method, spelling, pattern)
			}
		}
	}
}
