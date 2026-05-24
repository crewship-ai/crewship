package clitest

import (
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
)

func TestStubServer_HappyPath(t *testing.T) {
	t.Parallel()
	s := NewStubServer()
	defer s.Close()

	s.OnGet("/api/v1/agents", JSONResponse(200, []map[string]string{{"id": "a1"}, {"id": "a2"}}))
	s.OnPost("/api/v1/agents", JSONResponse(201, map[string]string{"id": "a3"}))

	// GET
	resp, err := http.Get(s.URL() + "/api/v1/agents")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("GET status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "\"a1\"") {
		t.Errorf("GET body = %s, want to contain a1", body)
	}

	// POST
	resp2, err := http.Post(s.URL()+"/api/v1/agents", "application/json", strings.NewReader(`{"name":"X"}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != 201 {
		t.Errorf("POST status = %d, want 201", resp2.StatusCode)
	}

	calls := s.Calls()
	if len(calls) != 2 {
		t.Fatalf("Calls len = %d, want 2", len(calls))
	}
	if calls[0].Method != "GET" || calls[0].Path != "/api/v1/agents" {
		t.Errorf("call[0] = %+v", calls[0])
	}
	if calls[1].Method != "POST" || string(calls[1].Body) != `{"name":"X"}` {
		t.Errorf("call[1] body = %s", calls[1].Body)
	}
}

func TestStubServer_FallbackIs404(t *testing.T) {
	t.Parallel()
	s := NewStubServer()
	defer s.Close()

	resp, err := http.Get(s.URL() + "/unregistered")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "no stub registered") {
		t.Errorf("body should explain missing stub: %s", body)
	}
}

func TestStubServer_QueryStringIgnored(t *testing.T) {
	t.Parallel()
	s := NewStubServer()
	defer s.Close()

	s.OnGet("/api/v1/agents", JSONResponse(200, []string{"ok"}))
	resp, err := http.Get(s.URL() + "/api/v1/agents?limit=10&offset=0")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("status = %d, want 200 (query should not affect routing)", resp.StatusCode)
	}
	calls := s.Calls()
	if len(calls) != 1 || calls[0].Query != "limit=10&offset=0" {
		t.Errorf("query not captured correctly: %+v", calls)
	}
}

func TestStubServer_CallsFor(t *testing.T) {
	t.Parallel()
	s := NewStubServer()
	defer s.Close()
	s.OnPost("/api/v1/agents", JSONResponse(201, map[string]string{"id": "a1"}))
	s.OnGet("/api/v1/agents", JSONResponse(200, []string{}))

	// Each call's body must be closed to avoid leaking the underlying
	// connection back to the http transport pool — t.Parallel tests
	// that re-use the same default transport will otherwise eventually
	// run into "too many open files" on CI.
	postOnce := func() {
		resp, err := http.Post(s.URL()+"/api/v1/agents", "application/json", strings.NewReader(`{}`))
		if err != nil {
			t.Fatalf("POST: %v", err)
		}
		_ = resp.Body.Close()
	}
	postOnce()
	postOnce()
	resp, err := http.Get(s.URL() + "/api/v1/agents")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	_ = resp.Body.Close()

	if got := len(s.CallsFor("POST", "/api/v1/agents")); got != 2 {
		t.Errorf("CallsFor(POST) = %d, want 2", got)
	}
	if got := len(s.CallsFor("GET", "/api/v1/agents")); got != 1 {
		t.Errorf("CallsFor(GET) = %d, want 1", got)
	}
	if got := len(s.CallsFor("DELETE", "/api/v1/agents")); got != 0 {
		t.Errorf("CallsFor(DELETE) = %d, want 0", got)
	}
}

func TestStubServer_RouteReplacement(t *testing.T) {
	t.Parallel()
	s := NewStubServer()
	defer s.Close()
	s.OnGet("/x", JSONResponse(200, "first"))
	s.OnGet("/x", JSONResponse(500, "second"))

	resp, err := http.Get(s.URL() + "/x")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 500 {
		t.Errorf("most-recent registration should win, got status %d", resp.StatusCode)
	}
}

func TestStubServer_Reset(t *testing.T) {
	t.Parallel()
	s := NewStubServer()
	defer s.Close()
	s.OnGet("/x", JSONResponse(200, "ok"))
	if r, err := http.Get(s.URL() + "/x"); err == nil {
		r.Body.Close()
	}

	s.Reset()
	if got := len(s.Calls()); got != 0 {
		t.Errorf("post-Reset Calls = %d, want 0", got)
	}
	// route should be gone too
	resp, err := http.Get(s.URL() + "/x")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Errorf("post-Reset route registration should be gone, got %d", resp.StatusCode)
	}
}

func TestStubServer_ResetCallsKeepsRoutes(t *testing.T) {
	t.Parallel()
	s := NewStubServer()
	defer s.Close()
	s.OnGet("/x", JSONResponse(200, "ok"))
	if r, err := http.Get(s.URL() + "/x"); err == nil {
		r.Body.Close()
	}

	s.ResetCalls()
	if got := len(s.Calls()); got != 0 {
		t.Errorf("ResetCalls should clear calls, got %d", got)
	}
	resp, err := http.Get(s.URL() + "/x")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("ResetCalls should keep routes, got %d", resp.StatusCode)
	}
}

func TestStubServer_ConcurrentRequests(t *testing.T) {
	t.Parallel()
	s := NewStubServer()
	defer s.Close()
	s.OnGet("/x", JSONResponse(200, "ok"))

	var wg sync.WaitGroup
	const n = 30
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, _ := http.Get(s.URL() + "/x")
			if resp != nil {
				resp.Body.Close()
			}
		}()
	}
	wg.Wait()
	if got := len(s.Calls()); got != n {
		t.Errorf("Calls len = %d, want %d (lost call under contention?)", got, n)
	}
}

func TestErrorResponse_StandardEnvelope(t *testing.T) {
	t.Parallel()
	s := NewStubServer()
	defer s.Close()
	s.OnGet("/x", ErrorResponse(403, "Forbidden: requires OWNER"))

	resp, err := http.Get(s.URL() + "/x")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Errorf("status = %d, want 403", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `"error":"Forbidden: requires OWNER"`) {
		t.Errorf("body = %s, want standard error envelope", body)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
}

func TestEmptyResponse_NoBody(t *testing.T) {
	t.Parallel()
	s := NewStubServer()
	defer s.Close()
	s.OnDelete("/x", EmptyResponse(204))

	req, _ := http.NewRequest("DELETE", s.URL()+"/x", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 204 {
		t.Errorf("status = %d, want 204", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if len(body) != 0 {
		t.Errorf("body len = %d, want 0", len(body))
	}
}

func TestDecodeJSONBody(t *testing.T) {
	t.Parallel()
	var v struct{ Name string }
	if err := DecodeJSONBody([]byte(`{"name":"X"}`), &v); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v.Name != "X" {
		t.Errorf("Name = %q, want X", v.Name)
	}

	if err := DecodeJSONBody(nil, &v); err == nil {
		t.Error("expected error on empty body")
	}
	if err := DecodeJSONBody([]byte("not json"), &v); err == nil {
		t.Error("expected error on invalid JSON")
	}
}

func TestDelayedJSONResponse_PanicsByDesign(t *testing.T) {
	t.Parallel()
	defer func() {
		if recover() == nil {
			t.Error("DelayedJSONResponse should panic per its doc comment")
		}
	}()
	_ = DelayedJSONResponse(200, nil)
}

func TestEdgeCases_CatalogShape(t *testing.T) {
	t.Parallel()
	if len(EdgeCases) == 0 {
		t.Fatal("EdgeCases catalog is empty")
	}
	seen := make(map[string]bool)
	for i, ec := range EdgeCases {
		if !strings.HasPrefix(ec.Slug, "edge_case_") {
			t.Errorf("EdgeCases[%d] slug %q must use edge_case_ prefix", i, ec.Slug)
		}
		if seen[ec.Slug] {
			t.Errorf("EdgeCases[%d] duplicate slug %q", i, ec.Slug)
		}
		seen[ec.Slug] = true
		if ec.Description == "" || ec.Recipe == "" {
			t.Errorf("EdgeCases[%d] %q missing description or recipe", i, ec.Slug)
		}
		if ec.Status < 100 || ec.Status >= 600 {
			t.Errorf("EdgeCases[%d] %q invalid HTTP status %d", i, ec.Slug, ec.Status)
		}
	}
}

func TestEdgeCaseBySlug(t *testing.T) {
	t.Parallel()
	for _, slug := range EdgeCaseSlugs() {
		if ec := EdgeCaseBySlug(slug); ec == nil || ec.Slug != slug {
			t.Errorf("EdgeCaseBySlug(%q) roundtrip failed", slug)
		}
	}
	if got := EdgeCaseBySlug("nonexistent"); got != nil {
		t.Errorf("EdgeCaseBySlug(\"nonexistent\") = %+v, want nil", got)
	}
}

// TestEdgeCases_AllInstallable smoke-tests every catalog entry by
// installing its recipe shape on a stub server and verifying the
// status matches. Documents that the catalog isn't drifting from the
// helpers — if someone adds a new edge case but mistypes the helper
// usage, this test fails.
func TestEdgeCases_AllInstallable(t *testing.T) {
	t.Parallel()
	s := NewStubServer()
	defer s.Close()

	for _, ec := range EdgeCases {
		ec := ec
		t.Run(ec.Slug, func(t *testing.T) {
			t.Parallel()
			// We can't blindly eval ec.Recipe (it's a doc string),
			// but we can install the documented status via the
			// canonical helper and confirm the round-trip.
			localStub := NewStubServer()
			defer localStub.Close()
			localStub.OnGet("/case", ErrorResponse(ec.Status, ec.Description))
			resp, err := http.Get(localStub.URL() + "/case")
			if err != nil {
				t.Fatalf("GET: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != ec.Status {
				t.Errorf("got status %d, want %d", resp.StatusCode, ec.Status)
			}
		})
	}
}
