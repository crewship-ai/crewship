package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// staticDoer lets us point a provider's *http.Client at an httptest server
// without exporting the unexported baseURL/apiKey fields. Each provider
// embeds a standard *http.Client, so swapping its Transport to redirect to
// the test server is the cleanest seam.
type rewriteTransport struct {
	target string
	rt     http.RoundTripper
}

func (rt *rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Redirect host/scheme to the test server, preserve the path+query.
	req.URL.Scheme = "http"
	req.URL.Host = rt.target
	return rt.rt.RoundTrip(req)
}

func TestOllamaListModels(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		body       string
		wantErr    bool
		wantIDs    []string
		wantSource string
	}{
		{
			name:    "live tags",
			status:  http.StatusOK,
			body:    `{"models":[{"name":"llama3.2:latest"},{"name":"qwen2.5-coder:7b"}]}`,
			wantIDs: []string{"llama3.2:latest", "qwen2.5-coder:7b"},
		},
		{
			name:    "empty list",
			status:  http.StatusOK,
			body:    `{"models":[]}`,
			wantIDs: []string{},
		},
		{
			name:    "skips blank names",
			status:  http.StatusOK,
			body:    `{"models":[{"name":""},{"name":"phi4"}]}`,
			wantIDs: []string{"phi4"},
		},
		{
			name:    "non-200 errors",
			status:  http.StatusInternalServerError,
			body:    "boom",
			wantErr: true,
		},
		{
			name:    "bad json errors",
			status:  http.StatusOK,
			body:    "{not json",
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/tags" {
					t.Errorf("path = %s, want /api/tags", r.URL.Path)
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			o := NewOllama(srv.URL, "llama3.2")
			got, err := o.ListModels(context.Background())
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got models %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ListModels: %v", err)
			}
			assertModelIDs(t, got, tc.wantIDs)
			for _, m := range got {
				if m.Provider != "ollama" {
					t.Errorf("provider = %s, want ollama", m.Provider)
				}
			}
		})
	}
}

func TestOllamaListModels_NetworkError(t *testing.T) {
	// Point at a server we immediately close so Do() returns a transport
	// error — covers the "ollama http:" branch.
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()

	o := NewOllama(url, "llama3.2")
	if _, err := o.ListModels(context.Background()); err == nil {
		t.Fatalf("expected transport error against closed server")
	}
}

func TestOpenAIListModels(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		body    string
		wantErr bool
		wantIDs []string
	}{
		{
			name:    "live models",
			status:  http.StatusOK,
			body:    `{"data":[{"id":"gpt-4o"},{"id":"gpt-4o-mini"}]}`,
			wantIDs: []string{"gpt-4o", "gpt-4o-mini"},
		},
		{
			name:    "skips blank ids",
			status:  http.StatusOK,
			body:    `{"data":[{"id":""},{"id":"o3"}]}`,
			wantIDs: []string{"o3"},
		},
		{
			name:    "unauthorized errors",
			status:  http.StatusUnauthorized,
			body:    `{"error":"bad key"}`,
			wantErr: true,
		},
		{
			name:    "bad json errors",
			status:  http.StatusOK,
			body:    "nope",
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/models" {
					t.Errorf("path = %s, want /v1/models", r.URL.Path)
				}
				if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
					t.Errorf("auth header = %q", got)
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			o := NewOpenAIWithBaseURL("test-key", srv.URL+"/v1/chat/completions")
			got, err := o.ListModels(context.Background())
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ListModels: %v", err)
			}
			assertModelIDs(t, got, tc.wantIDs)
			for _, m := range got {
				if m.Provider != "openai" {
					t.Errorf("provider = %s, want openai", m.Provider)
				}
			}
		})
	}
}

// TestOpenAIListModels_PreservesQueryAndPath pins the net/url-based models
// endpoint derivation: an Azure/proxy base URL with a query string and a
// versioned path must rewrite only the trailing chat/completions segment to
// /models while keeping the path prefix and the query (e.g. api-version).
func TestOpenAIListModels_PreservesQueryAndPath(t *testing.T) {
	var gotPath, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.Query().Get("api-version")
		_, _ = w.Write([]byte(`{"data":[{"id":"gpt-4o"}]}`))
	}))
	defer srv.Close()

	o := NewOpenAIWithBaseURL("test-key", srv.URL+"/openai/deployments/x/chat/completions?api-version=2024-02-01")
	got, err := o.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if gotPath != "/openai/deployments/x/models" {
		t.Errorf("path = %q, want /openai/deployments/x/models", gotPath)
	}
	if gotQuery != "2024-02-01" {
		t.Errorf("api-version query not preserved: %q", gotQuery)
	}
	assertModelIDs(t, got, []string{"gpt-4o"})
}

func TestAnthropicListModels(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		body    string
		wantErr bool
		wantIDs []string
	}{
		{
			name:    "live models",
			status:  http.StatusOK,
			body:    `{"data":[{"id":"claude-opus-4-8","display_name":"Claude Opus 4.8"},{"id":"claude-sonnet-4-6"}]}`,
			wantIDs: []string{"claude-opus-4-8", "claude-sonnet-4-6"},
		},
		{
			name:    "skips blank ids",
			status:  http.StatusOK,
			body:    `{"data":[{"id":""},{"id":"claude-haiku-4-5"}]}`,
			wantIDs: []string{"claude-haiku-4-5"},
		},
		{
			name:    "unauthorized errors",
			status:  http.StatusUnauthorized,
			body:    `{"error":"bad key"}`,
			wantErr: true,
		},
		{
			name:    "bad json errors",
			status:  http.StatusOK,
			body:    "not-json",
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/models" {
					t.Errorf("path = %s, want /v1/models", r.URL.Path)
				}
				if got := r.Header.Get("x-api-key"); got != "sk-ant-test" {
					t.Errorf("x-api-key = %q", got)
				}
				if got := r.Header.Get("anthropic-version"); got == "" {
					t.Errorf("missing anthropic-version header")
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			a := NewAnthropic("sk-ant-test")
			// Redirect the provider's client at the test server.
			a.client.Transport = &rewriteTransport{target: hostOf(srv.URL), rt: http.DefaultTransport}
			got, err := a.ListModels(context.Background())
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ListModels: %v", err)
			}
			assertModelIDs(t, got, tc.wantIDs)
		})
	}
}

func TestCuratedModels(t *testing.T) {
	tests := []struct {
		provider string
		wantSome string
		wantLen  bool
	}{
		{provider: "ANTHROPIC", wantSome: "claude-opus-4-8", wantLen: true},
		{provider: "anthropic", wantSome: "claude-opus-4-8", wantLen: true},
		{provider: "OPENAI", wantSome: "gpt-4o", wantLen: true},
		{provider: "GOOGLE", wantSome: "gemini-2.0-flash", wantLen: true},
		{provider: "OLLAMA", wantSome: "", wantLen: false},
		{provider: "UNKNOWN", wantSome: "", wantLen: false},
	}
	for _, tc := range tests {
		t.Run(tc.provider, func(t *testing.T) {
			got := CuratedModels(tc.provider)
			if tc.wantLen && len(got) == 0 {
				t.Fatalf("expected curated models for %s, got none", tc.provider)
			}
			if !tc.wantLen && len(got) != 0 {
				t.Fatalf("expected no curated models for %s, got %+v", tc.provider, got)
			}
			if tc.wantSome != "" {
				found := false
				for _, m := range got {
					if m.ID == tc.wantSome {
						found = true
					}
					if m.Provider == "" {
						t.Errorf("curated model %q missing provider", m.ID)
					}
				}
				if !found {
					t.Errorf("curated %s missing %q; got %+v", tc.provider, tc.wantSome, got)
				}
			}
		})
	}
}

func assertModelIDs(t *testing.T, got []ModelInfo, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("len = %d (%+v), want %d (%v)", len(got), got, len(want), want)
	}
	for i, id := range want {
		if got[i].ID != id {
			t.Errorf("model[%d].ID = %q, want %q", i, got[i].ID, id)
		}
	}
}

func hostOf(rawURL string) string {
	// rawURL is like "http://127.0.0.1:PORT"
	return rawURL[len("http://"):]
}
