package pipeline

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTTPStep_GET_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	exec := NewExecutor(store, resolver, nil, nil)
	exec.SetAllowPrivateHTTPForTesting(true)

	step := Step{
		ID:   "fetch",
		Type: StepHTTP,
		HTTP: &HTTPStep{Method: "GET", URL: srv.URL},
	}
	out, _, _, err := exec.runHTTPStep(context.Background(), step, RenderContext{})
	if err != nil {
		t.Fatalf("http step: %v", err)
	}
	if !strings.Contains(out, `"ok":true`) {
		t.Errorf("output: got %q", out)
	}
}

func TestHTTPStep_BlockedByEgressAllowlist(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	exec := NewExecutor(store, resolver, nil, nil).WithEgressGate(func(host string) bool {
		return false // deny everything
	})
	exec.SetAllowPrivateHTTPForTesting(true)

	step := Step{ID: "fetch", Type: StepHTTP, HTTP: &HTTPStep{Method: "GET", URL: srv.URL}}
	_, _, _, err := exec.runHTTPStep(context.Background(), step, RenderContext{})
	if err == nil || !strings.Contains(err.Error(), "not in egress allowlist") {
		t.Errorf("expected egress denial, got %v", err)
	}
}

func TestHTTPStep_NonSuccessStatusFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"err":"boom"}`))
	}))
	defer srv.Close()

	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	exec := NewExecutor(store, resolver, nil, nil)
	exec.SetAllowPrivateHTTPForTesting(true)

	step := Step{ID: "fetch", Type: StepHTTP, HTTP: &HTTPStep{Method: "GET", URL: srv.URL}}
	out, _, _, err := exec.runHTTPStep(context.Background(), step, RenderContext{})
	if err == nil {
		t.Fatal("expected non-success error")
	}
	if !strings.Contains(out, "boom") {
		t.Errorf("output should still carry response body: %q", out)
	}
}

func TestHTTPStep_BearerCredentialInjection(t *testing.T) {
	var seenAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	exec := NewExecutor(store, resolver, nil, nil).WithCredentialResolver(
		func(_ context.Context, t string) (string, error) { return "secret-token", nil },
	)
	exec.SetAllowPrivateHTTPForTesting(true)

	step := Step{
		ID: "post", Type: StepHTTP,
		HTTP: &HTTPStep{
			Method: "POST", URL: srv.URL, Body: `{}`,
			CredentialRef: &CredentialRef{Type: "slack"},
		},
	}
	if _, _, _, err := exec.runHTTPStep(context.Background(), step, RenderContext{}); err != nil {
		t.Fatalf("step: %v", err)
	}
	if seenAuth != "Bearer secret-token" {
		t.Errorf("Authorization header: got %q", seenAuth)
	}
}

func TestHTTPStep_TemplateSubstitution(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("X-Echo-Path", r.URL.Path)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	exec := NewExecutor(store, resolver, nil, nil)
	exec.SetAllowPrivateHTTPForTesting(true)

	step := Step{
		ID: "post", Type: StepHTTP,
		HTTP: &HTTPStep{
			Method: "POST",
			URL:    srv.URL + "/u/{{ inputs.user }}",
			Body:   `{"msg":"hello {{ inputs.user }}"}`,
		},
	}
	rctx := RenderContext{Inputs: map[string]any{"user": "pavel"}}
	out, _, _, err := exec.runHTTPStep(context.Background(), step, rctx)
	if err != nil {
		t.Fatalf("step: %v", err)
	}
	if !strings.Contains(out, "hello pavel") {
		t.Errorf("output should contain templated body, got %q", out)
	}
}

func TestHTTPStep_MaxResponseBytesTruncates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(strings.Repeat("x", 5000)))
	}))
	defer srv.Close()

	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	exec := NewExecutor(store, resolver, nil, nil)
	exec.SetAllowPrivateHTTPForTesting(true)

	step := Step{
		ID: "fetch", Type: StepHTTP,
		HTTP: &HTTPStep{Method: "GET", URL: srv.URL, MaxResponseBytes: 100},
	}
	out, _, _, err := exec.runHTTPStep(context.Background(), step, RenderContext{})
	if err != nil {
		t.Fatalf("step: %v", err)
	}
	if !strings.Contains(out, "(response truncated)") {
		t.Errorf("expected truncation marker, got len=%d", len(out))
	}
}

// TestHTTPStep_SSRFViaRedirectBlocked targets the security fix from
// the routines stabilization commit. Without the CheckRedirect guard,
// an allowed host could 302-redirect into a blocked host (e.g. AWS
// IMDS at 169.254.169.254 or localhost) and leak metadata into the
// step output. The fix re-validates every redirect target against
// the egress allowlist.
//
// Setup: two servers — `allowed` (allowlisted) returns a 302 to
// `blocked` (NOT allowlisted). The egress gate accepts only
// `allowed.URL`'s host. With the CheckRedirect fix, the step fails
// at the redirect rather than succeeding with `blocked`'s body.
func TestHTTPStep_SSRFViaRedirectBlocked(t *testing.T) {
	blockedHits := 0
	blocked := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		blockedHits++
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("internal-metadata-secret"))
	}))
	defer blocked.Close()

	allowed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, blocked.URL, http.StatusFound)
	}))
	defer allowed.Close()

	allowedHost := mustParseHost(t, allowed.URL)
	blockedHost := mustParseHost(t, blocked.URL)

	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	exec := NewExecutor(store, resolver, nil, nil).WithEgressGate(func(host string) bool {
		return host == allowedHost // ONLY the entrypoint, NOT the redirect target
	})
	exec.SetAllowPrivateHTTPForTesting(true)

	step := Step{
		ID:   "fetch",
		Type: StepHTTP,
		HTTP: &HTTPStep{Method: "GET", URL: allowed.URL},
	}
	out, _, _, err := exec.runHTTPStep(context.Background(), step, RenderContext{})
	if err == nil {
		t.Fatalf("SSRF leak: expected redirect to be blocked; got out=%q", out)
	}
	if !strings.Contains(err.Error(), blockedHost) ||
		!strings.Contains(err.Error(), "blocked by egress allowlist") {
		t.Errorf("expected redirect-blocked error mentioning host, got %v", err)
	}
	if strings.Contains(out, "internal-metadata-secret") {
		t.Errorf("blocked server's body leaked into step output: %q", out)
	}
	if blockedHits > 0 {
		t.Errorf("blocked server should never have been hit; got %d hits", blockedHits)
	}
}

// mustParseHost extracts the host:port from a test-server URL. We
// don't pull in net/url import surface beyond what runner_http_test
// already needs; httptest URLs are always well-formed.
func mustParseHost(t *testing.T, raw string) string {
	t.Helper()
	// Strip the scheme — formats are http://host:port[/path]
	const prefix = "http://"
	if !strings.HasPrefix(raw, prefix) {
		t.Fatalf("unexpected test server URL format: %s", raw)
	}
	rest := strings.TrimPrefix(raw, prefix)
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		rest = rest[:i]
	}
	return rest
}
