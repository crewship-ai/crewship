package cli

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClientPatchAndPut(t *testing.T) {
	var gotMethod, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "", "")

	resp, err := c.Patch("/api/v1/things/1", map[string]string{"name": "new"})
	if err != nil {
		t.Fatalf("Patch: %v", err)
	}
	resp.Body.Close()
	if gotMethod != "PATCH" {
		t.Errorf("method = %q, want PATCH", gotMethod)
	}
	if !strings.Contains(gotBody, `"name":"new"`) {
		t.Errorf("PATCH body = %q", gotBody)
	}

	resp, err = c.Put("/api/v1/policy", map[string]string{"mode": "strict"})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	resp.Body.Close()
	if gotMethod != "PUT" {
		t.Errorf("method = %q, want PUT", gotMethod)
	}
	if !strings.Contains(gotBody, `"mode":"strict"`) {
		t.Errorf("PUT body = %q", gotBody)
	}
}

func TestLooksLikeCUID(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"c1234567890123456789", true},           // 20 chars, valid
		{"cabcdefghijklmnopqrstuvwxyz123", true}, // longer, valid
		{"c123456789012345678", false},           // 19 chars, too short
		{"d1234567890123456789", false},          // wrong first char
		{"cABCDEFGHIJKLMNOPQRS", false},          // uppercase rejected
		{"c12345678901234567-9", false},          // dash rejected
		{"", false},
		{"my-workspace-slug", false},
	}
	for _, tt := range tests {
		if got := looksLikeCUID(tt.in); got != tt.want {
			t.Errorf("looksLikeCUID(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestWithContextNilFallsBackToBackground(t *testing.T) {
	c := NewClient("http://example.com", "", "")
	clone := c.WithContext(nil) //nolint:staticcheck // nil ctx is the documented fallback path
	if clone.ctx == nil {
		t.Fatal("clone ctx should never be nil")
	}
	if clone == c {
		t.Error("WithContext must return a copy, not the receiver")
	}
}

func TestWithContextDoesNotMutateOriginal(t *testing.T) {
	c := NewClient("http://example.com", "", "")
	orig := c.ctx
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	clone := c.WithContext(ctx)
	if clone.ctx != ctx {
		t.Error("clone should carry the provided ctx")
	}
	if c.ctx != orig {
		t.Error("original client ctx must be unchanged")
	}
}

func TestDoMarshalBodyError(t *testing.T) {
	c := NewClient("http://example.com", "", "")
	_, err := c.Post("/api/v1/x", make(chan int)) // channels are not JSON-marshalable
	if err == nil || !strings.Contains(err.Error(), "marshal body") {
		t.Errorf("err = %v, want marshal body error", err)
	}
}

func TestDoBadBaseURL(t *testing.T) {
	c := NewClient("http://example.com/\x00", "", "")
	_, err := c.Get("/api/v1/x")
	if err == nil || !strings.Contains(err.Error(), "parse URL") {
		t.Errorf("err = %v, want parse URL error", err)
	}
}

func TestDoRequestFailed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close()

	c := NewClient(url, "", "")
	_, err := c.Get("/api/v1/x")
	if err == nil || !strings.Contains(err.Error(), "request failed") {
		t.Errorf("err = %v, want request failed error", err)
	}
}

func TestDoIOReaderBodyPassesThroughVerbatim(t *testing.T) {
	raw := `{"already":"serialized"}`
	var gotBody, gotCT string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		gotCT = r.Header.Get("Content-Type")
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "", "")
	resp, err := c.Post("/api/v1/x", strings.NewReader(raw))
	if err != nil {
		t.Fatalf("Post: %v", err)
	}
	resp.Body.Close()
	if gotBody != raw {
		t.Errorf("body = %q, want verbatim %q (io.Reader must not be re-marshaled)", gotBody, raw)
	}
	if gotCT != "application/json" {
		t.Errorf("Content-Type = %q", gotCT)
	}
}

func TestGetWorkspaceIDEmpty(t *testing.T) {
	c := NewClient("http://example.com", "", "")
	if got := c.GetWorkspaceID(); got != "" {
		t.Errorf("GetWorkspaceID() = %q, want empty", got)
	}
}

func TestGetWorkspaceIDUsesCachedResolution(t *testing.T) {
	c := NewClient("http://example.com", "", "my-slug")
	c.resolvedWorkspaceID = "c1234567890123456789"
	if got := c.GetWorkspaceID(); got != "c1234567890123456789" {
		t.Errorf("GetWorkspaceID() = %q, want cached CUID without HTTP", got)
	}
}

func TestGetWorkspaceIDSlugFallbackOnResolveError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "", "my-slug")
	if got := c.GetWorkspaceID(); got != "my-slug" {
		t.Errorf("GetWorkspaceID() = %q, want slug fallback on resolve failure", got)
	}
	if c.resolvedWorkspaceID != "" {
		t.Errorf("failed resolution must not be cached, got %q", c.resolvedWorkspaceID)
	}
}

func TestResolveWorkspaceSlugNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "", "")
	_, err := c.resolveWorkspaceSlug(context.Background(), "x")
	if err == nil || !strings.Contains(err.Error(), "workspace list failed (HTTP 403)") {
		t.Errorf("err = %v, want HTTP 403 error", err)
	}
}

func TestResolveWorkspaceSlugBadJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not json"))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "", "")
	if _, err := c.resolveWorkspaceSlug(context.Background(), "x"); err == nil {
		t.Error("want unmarshal error for invalid JSON body")
	}
}

func TestResolveWorkspaceSlugNotFound(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`[{"id":"c1","slug":"other"}]`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "secret-token", "")
	// nil ctx exercises the fallback-to-Background branch.
	_, err := c.resolveWorkspaceSlug(nil, "missing") //nolint:staticcheck
	if err == nil || !strings.Contains(err.Error(), "workspace not found: missing") {
		t.Errorf("err = %v, want workspace not found", err)
	}
	if gotAuth != "Bearer secret-token" {
		t.Errorf("Authorization = %q, want bearer token on resolve call", gotAuth)
	}
}

func TestResolveWorkspaceSlugBadURL(t *testing.T) {
	c := NewClient("http://example.com/\x00", "", "")
	_, err := c.resolveWorkspaceSlug(context.Background(), "x")
	if err == nil || !strings.Contains(err.Error(), "parse workspace URL") {
		t.Errorf("err = %v, want parse workspace URL error", err)
	}
}

func TestResolveWorkspaceSlugConnectError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close()

	c := NewClient(url, "", "")
	_, err := c.resolveWorkspaceSlug(context.Background(), "x")
	if err == nil || !strings.Contains(err.Error(), "fetch workspaces") {
		t.Errorf("err = %v, want fetch workspaces error", err)
	}
}

type errReadCloser struct{ err error }

func (e errReadCloser) Read([]byte) (int, error) { return 0, e.err }
func (e errReadCloser) Close() error             { return nil }

func TestReadJSONBodyReadError(t *testing.T) {
	boom := errors.New("disk on fire")
	resp := &http.Response{Body: errReadCloser{err: boom}}
	var v map[string]any
	err := ReadJSON(resp, &v)
	if err == nil || !strings.Contains(err.Error(), "read response") {
		t.Errorf("err = %v, want read response error", err)
	}
	if !errors.Is(err, boom) {
		t.Errorf("err = %v, want wrapped %v", err, boom)
	}
}

func TestDoNilCtxFallsBackToBackground(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	// Zero-value construction (not via NewClient) leaves ctx nil; Do must
	// tolerate it instead of panicking in http.NewRequestWithContext.
	c := &Client{BaseURL: srv.URL, HTTPClient: srv.Client()}
	resp, err := c.Get("/api/v1/x")
	if err != nil {
		t.Fatalf("Get with nil ctx: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("status = %d, want 204", resp.StatusCode)
	}
}
