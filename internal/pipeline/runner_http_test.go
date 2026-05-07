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
