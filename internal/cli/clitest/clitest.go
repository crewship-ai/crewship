package clitest

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
)

// ─── StubServer ──────────────────────────────────────────────────────────

// Handler is the per-route response producer registered on a
// [StubServer]. It receives a copy of the request body (already
// drained from r.Body so the route handler can call ReadAll without
// thinking about it) and returns the canned status + body the CLI
// should observe.
//
// Implementations should NOT mutate r — the StubServer hands out
// pointers but reuses them across registered routes; mutation creates
// hard-to-find test cross-contamination.
type Handler func(r *http.Request, body []byte) (status int, respBody []byte, contentType string)

// routeKey is the registration shape: method + path. We key on the
// raw path (no query string normalisation) so a route registered for
// "/api/v1/agents" matches both "/api/v1/agents" and
// "/api/v1/agents?limit=10" — query handling is the route handler's
// responsibility, not the dispatcher's.
type routeKey struct {
	method string
	path   string
}

// RecordedCall captures one request the StubServer observed. Tests
// assert against [StubServer.Calls] to verify "the CLI called POST
// /api/v1/agents exactly once with this body".
type RecordedCall struct {
	Method  string
	Path    string
	Query   string // raw query string ("?" stripped)
	Headers http.Header
	Body    []byte
}

// StubServer wraps an httptest.Server with a route registry. Use
// [NewStubServer] to construct one; remember to call Close() in a
// defer.
type StubServer struct {
	srv *httptest.Server

	mu       sync.Mutex
	routes   map[routeKey]Handler
	calls    []RecordedCall
	fallback Handler // returns 404 by default — override with SetFallback
}

// NewStubServer returns a ready-to-use StubServer. Register routes
// with On / OnGet / OnPost / OnPatch / OnDelete / OnPut. Unmatched
// requests fall through to the fallback handler (default 404).
func NewStubServer() *StubServer {
	s := &StubServer{
		routes: make(map[routeKey]Handler),
		fallback: func(r *http.Request, _ []byte) (int, []byte, string) {
			body := fmt.Sprintf(`{"error":"clitest: no stub registered for %s %s"}`, r.Method, r.URL.Path)
			return http.StatusNotFound, []byte(body), "application/json"
		},
	}
	s.srv = httptest.NewServer(http.HandlerFunc(s.dispatch))
	return s
}

// URL returns the server's base URL, suitable for passing to
// [internal/cli.NewClient].
func (s *StubServer) URL() string { return s.srv.URL }

// Close shuts down the underlying httptest.Server. Always defer
// this; leaking httptest servers slowly exhausts the test process's
// listener budget.
func (s *StubServer) Close() { s.srv.Close() }

// On registers a handler for a method + path tuple. Multiple registrations
// for the same tuple replace each other — the most recent wins, which
// matches the "subtest reconfigures the same route" pattern.
func (s *StubServer) On(method, path string, h Handler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.routes[routeKey{method: strings.ToUpper(method), path: path}] = h
}

// OnGet is sugar for On("GET", ...).
func (s *StubServer) OnGet(path string, h Handler) { s.On(http.MethodGet, path, h) }

// OnPost is sugar for On("POST", ...).
func (s *StubServer) OnPost(path string, h Handler) { s.On(http.MethodPost, path, h) }

// OnPatch is sugar for On("PATCH", ...).
func (s *StubServer) OnPatch(path string, h Handler) { s.On(http.MethodPatch, path, h) }

// OnPut is sugar for On("PUT", ...).
func (s *StubServer) OnPut(path string, h Handler) { s.On(http.MethodPut, path, h) }

// OnDelete is sugar for On("DELETE", ...).
func (s *StubServer) OnDelete(path string, h Handler) { s.On(http.MethodDelete, path, h) }

// SetFallback replaces the default 404 handler. Use this when a test
// wants ANY unstubbed request to fail with a specific status (e.g.
// 500) so an accidental extra call surfaces loudly rather than as a
// confusing "missing stub" message.
func (s *StubServer) SetFallback(h Handler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fallback = h
}

// Calls returns a defensive copy of every request the server has
// observed, in arrival order. Defensive-copy so a test that captures
// then asserts can't accidentally corrupt the server's internal log.
func (s *StubServer) Calls() []RecordedCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]RecordedCall, len(s.calls))
	copy(out, s.calls)
	return out
}

// CallsFor returns the subset of [Calls] matching method + path.
// Useful for "exactly one POST to /agents" assertions without
// iterating the full log manually.
func (s *StubServer) CallsFor(method, path string) []RecordedCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	method = strings.ToUpper(method)
	var out []RecordedCall
	for _, c := range s.calls {
		if c.Method == method && c.Path == path {
			out = append(out, c)
		}
	}
	return out
}

// Reset clears the call log AND every registered route. Use between
// subtests when the same server should look pristine.
func (s *StubServer) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.routes = make(map[routeKey]Handler)
	s.calls = nil
}

// ResetCalls clears only the call log but keeps registered routes.
// Useful when a single subtest does setup, runs the CLI command, then
// wants to verify ONLY the second-phase calls.
func (s *StubServer) ResetCalls() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = nil
}

// dispatch is the httptest.Server's HandlerFunc. It drains the body
// once, records the call, looks up the route, and writes the canned
// response.
func (s *StubServer) dispatch(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		// Read failure here is a test-infra bug, not a routing
		// outcome — surface it loud rather than translating to 4xx.
		http.Error(w, fmt.Sprintf("clitest: read body: %v", err), http.StatusInternalServerError)
		return
	}
	_ = r.Body.Close()

	s.mu.Lock()
	h, ok := s.routes[routeKey{method: r.Method, path: r.URL.Path}]
	if !ok {
		h = s.fallback
	}
	s.calls = append(s.calls, RecordedCall{
		Method:  r.Method,
		Path:    r.URL.Path,
		Query:   r.URL.RawQuery,
		Headers: r.Header.Clone(),
		Body:    append([]byte(nil), body...),
	})
	s.mu.Unlock()

	status, respBody, contentType := h(r, body)
	if contentType == "" {
		contentType = "application/json"
	}
	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(status)
	if len(respBody) > 0 {
		_, _ = w.Write(respBody)
	}
}

// ─── Response helpers ───────────────────────────────────────────────────

// JSONResponse returns a Handler that serves the given status + JSON
// payload. payload can be any JSON-marshallable Go value (map, struct,
// slice, primitive). Panics if marshalling fails — that's a test
// authoring bug, not a runtime outcome.
func JSONResponse(status int, payload any) Handler {
	b, err := json.Marshal(payload)
	if err != nil {
		panic(fmt.Sprintf("clitest.JSONResponse: marshal: %v", err))
	}
	return func(_ *http.Request, _ []byte) (int, []byte, string) {
		return status, b, "application/json"
	}
}

// ErrorResponse returns a Handler that serves the standard Crewship
// error envelope: `{"error": "message"}` at the given HTTP status.
// Matches the shape internal/api/helpers.go:replyError produces, so
// CLI code that decodes errors with cli.CheckError sees identical
// bytes whether the backend is real or stubbed.
func ErrorResponse(status int, message string) Handler {
	return JSONResponse(status, map[string]string{"error": message})
}

// EmptyResponse returns a Handler that serves status + zero body.
// Useful for 204 No Content paths or DELETE confirmations.
func EmptyResponse(status int) Handler {
	return func(_ *http.Request, _ []byte) (int, []byte, string) {
		return status, nil, ""
	}
}

// TextResponse returns a Handler that serves a text/plain body.
// Useful for endpoints that emit logs / streams as text rather than
// JSON (rare in this codebase but covered for completeness).
func TextResponse(status int, body string) Handler {
	return func(_ *http.Request, _ []byte) (int, []byte, string) {
		return status, []byte(body), "text/plain; charset=utf-8"
	}
}

// DelayedJSONResponse wraps [JSONResponse] with a sleep before
// returning. NOT recommended for unit tests (slows the suite); useful
// for the rare integration test that needs to verify CLI-side
// timeouts.
//
// Implementation note: kept here rather than as a wrapper at the
// caller site so the delay constant lives next to the other
// scaffold code — easier to grep for "delayed" when triaging a slow
// suite.
//
// Deliberately NOT implemented in this PR — adding the time.Sleep
// here would set the precedent that test scaffolds may sleep, which
// then leaks into people's tests. If a real test needs timeout
// coverage, wrap the inner Handler with a explicit `time.Sleep` at
// the call site so the slowness is locally obvious.
//
// This stub exists as documentation of the intentional non-feature.
func DelayedJSONResponse(_ int, _ any) Handler {
	panic("clitest: DelayedJSONResponse is intentionally not implemented — wrap a Handler with explicit time.Sleep at the call site, see doc comment for rationale")
}

// ─── Request decoding helpers ───────────────────────────────────────────

// DecodeJSONBody unmarshals body into v. Returns the error so the
// test author can decide whether to t.Fatalf — clitest does not
// reach into testing.T itself, on the principle that test helpers
// should compose with whatever assertion library the caller prefers.
func DecodeJSONBody(body []byte, v any) error {
	if len(body) == 0 {
		return fmt.Errorf("clitest: body is empty")
	}
	if err := json.Unmarshal(body, v); err != nil {
		return fmt.Errorf("clitest: decode body: %w", err)
	}
	return nil
}

// MustDecodeJSONBody is [DecodeJSONBody] that panics on failure.
// Use only when the test has already asserted the request was made
// (so an empty / invalid body genuinely is a test-infra bug, not a
// behaviour assertion).
func MustDecodeJSONBody(body []byte, v any) {
	if err := DecodeJSONBody(body, v); err != nil {
		panic(err)
	}
}

// ─── HTTP edge case catalog ─────────────────────────────────────────────

// EdgeCase documents one canonical backend response shape the CLI
// must handle. Slug is a stable identifier (kebab-case with
// edge_case_ prefix) used for searchability.
type EdgeCase struct {
	Slug        string
	Status      int
	Description string
	Body        string // raw JSON the real backend emits
	Recipe      string // one-line snippet showing how to install on a StubServer
}

// EdgeCases enumerates the recurring backend response patterns the
// CLI must handle. Sourced from the actual response shapes the
// real handlers in internal/api/ produce — keep in sync when the
// server's error envelope evolves.
var EdgeCases = []EdgeCase{
	{
		Slug:        "edge_case_unauthorized_no_token",
		Status:      http.StatusUnauthorized,
		Description: "Caller missing or invalid bearer — CLI must prompt for re-login, not retry",
		Body:        `{"error":"Unauthorized"}`,
		Recipe:      `stub.OnGet("/api/v1/workspaces", clitest.ErrorResponse(401, "Unauthorized"))`,
	},
	{
		Slug:        "edge_case_forbidden_role",
		Status:      http.StatusForbidden,
		Description: "Caller authenticated but role lacks permission — CLI must surface required role",
		Body:        `{"error":"Forbidden: requires OWNER or ADMIN role"}`,
		Recipe:      `stub.OnDelete("/api/v1/workspaces/ws_1", clitest.ErrorResponse(403, "Forbidden: requires OWNER role"))`,
	},
	{
		Slug:        "edge_case_not_found",
		Status:      http.StatusNotFound,
		Description: "Resource missing — CLI must distinguish from auth failure (404 not 401)",
		Body:        `{"error":"Crew not found"}`,
		Recipe:      `stub.OnGet("/api/v1/crews/ghost", clitest.ErrorResponse(404, "Crew not found"))`,
	},
	{
		Slug:        "edge_case_conflict_slug_taken",
		Status:      http.StatusConflict,
		Description: "Idempotency collision (e.g. slug already exists) — CLI must NOT retry, surface the collision",
		Body:        `{"error":"crew slug already exists"}`,
		Recipe:      `stub.OnPost("/api/v1/crews", clitest.ErrorResponse(409, "crew slug already exists"))`,
	},
	{
		Slug:        "edge_case_unprocessable_validation",
		Status:      http.StatusUnprocessableEntity,
		Description: "Server-side validation failed (multi-field) — CLI must list all errors not just first",
		Body:        `{"error":"validation failed","fields":{"email":"invalid format","password":"too short"}}`,
		Recipe:      `stub.OnPost("/api/v1/bootstrap", clitest.JSONResponse(422, map[string]any{"error":"validation failed", "fields": map[string]string{"email":"invalid"}}))`,
	},
	{
		Slug:        "edge_case_rate_limited",
		Status:      http.StatusTooManyRequests,
		Description: "Rate limiter triggered — CLI must NOT brute-retry; surface Retry-After header if present",
		Body:        `{"error":"Too Many Requests","retry_after":60}`,
		Recipe:      `stub.OnPost("/api/v1/auth/login", clitest.ErrorResponse(429, "Too Many Requests"))`,
	},
	{
		Slug:        "edge_case_server_error_5xx",
		Status:      http.StatusInternalServerError,
		Description: "Backend wedged — CLI may retry once with backoff but must not loop infinitely",
		Body:        `{"error":"Internal server error"}`,
		Recipe:      `stub.OnGet("/api/v1/agents", clitest.ErrorResponse(500, "Internal server error"))`,
	},
	{
		Slug:        "edge_case_service_unavailable",
		Status:      http.StatusServiceUnavailable,
		Description: "Provisioner busy / sidecar not yet ready — CLI must distinguish from 5xx (retry vs surface)",
		Body:        `{"error":"Service unavailable","reason":"provisioner queue full"}`,
		Recipe:      `stub.OnPost("/api/v1/crews/c1/provision", clitest.ErrorResponse(503, "Service unavailable"))`,
	},
	{
		Slug:        "edge_case_malformed_json_response",
		Status:      http.StatusOK,
		Description: "200 with body that isn't valid JSON — CLI must error cleanly, not panic on decode",
		Body:        `not json at all`,
		Recipe:      `stub.OnGet("/api/v1/crews", clitest.TextResponse(200, "not json at all"))`,
	},
	{
		Slug:        "edge_case_empty_body_on_success",
		Status:      http.StatusNoContent,
		Description: "204 No Content — CLI must NOT attempt to decode body",
		Body:        ``,
		Recipe:      `stub.OnDelete("/api/v1/crews/c1", clitest.EmptyResponse(204))`,
	},
	{
		Slug:        "edge_case_partial_list_response",
		Status:      http.StatusOK,
		Description: "List endpoint returns subset (server-side cap) — CLI must surface the truncation, not silently use partial data",
		Body:        `{"items":[...], "truncated": true, "total": 1000}`,
		Recipe:      `stub.OnGet("/api/v1/agents", clitest.JSONResponse(200, map[string]any{"items": [...], "truncated": true}))`,
	},
}

// EdgeCaseBySlug returns the catalog entry or nil. Use in
// table-driven tests so an unknown slug fails the test rather than
// silently being a no-op.
func EdgeCaseBySlug(slug string) *EdgeCase {
	for i := range EdgeCases {
		if EdgeCases[i].Slug == slug {
			return &EdgeCases[i]
		}
	}
	return nil
}

// EdgeCaseSlugs returns every slug in catalog order. Useful for
// table-driven tests that want to iterate every known edge case.
func EdgeCaseSlugs() []string {
	out := make([]string, len(EdgeCases))
	for i, ec := range EdgeCases {
		out[i] = ec.Slug
	}
	return out
}
