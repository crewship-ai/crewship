package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// These are the regression tests for the shape mismatch that made a correctly
// configured Keeper deny everything.
//
// One ENDPOINT_URL credential was consumed by three paths that each wanted a
// different shape of the same string: a bare root for llm.Ollama, ".../v1" for
// the OpenCode agent block, ".../v1/chat/completions" for llm.OpenAI. Our own
// docs tell operators to store the ".../v1" form. Feed that to the Keeper judge
// and it POSTed to ".../v1/api/chat" -> 404 -> fail-closed DENY on every
// credential request, while the credential's Test button reported green
// (the probe strips "/v1" before falling back to "/api/tags").
//
// Both tests fail against the pre-endpoint providers, which is the point: they
// pin the behaviour an operator actually gets, not the string we happen to store.

// recordingModelServer answers the canonical API paths and records which ones
// were hit, 404ing anything else the way a real server would.
type recordingModelServer struct {
	*httptest.Server
	mu   sync.Mutex
	hits []string
}

func newRecordingModelServer(t *testing.T) *recordingModelServer {
	t.Helper()
	s := &recordingModelServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.hits = append(s.hits, r.URL.Path)
		s.mu.Unlock()

		switch r.URL.Path {
		case "/api/chat":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"message":{"role":"assistant","content":"{\"decision\":\"ALLOW\"}"},"done":true,"done_reason":"stop"}`))
		case "/api/tags":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"models":[{"name":"qwen3:4b"}]}`))
		case "/v1/chat/completions":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
		case "/v1/models":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[{"id":"qwen3:4b"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *recordingModelServer) hit(path string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.hits {
		if p == path {
			return true
		}
	}
	return false
}

func (s *recordingModelServer) hitPaths() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.Join(s.hits, ", ")
}

func probeRequest() Request {
	return Request{
		Model:     "qwen3:4b",
		Messages:  []Message{{Role: "user", Content: "decide"}},
		MaxTokens: 16,
	}
}

// TestOllama_AcceptsEveryStoredShape is the core regression: every shape an
// operator may have stored must reach /api/chat, not /v1/api/chat.
func TestOllama_AcceptsEveryStoredShape(t *testing.T) {
	for _, suffix := range []string{"", "/", "/v1", "/v1/", "/api/chat"} {
		t.Run("base"+suffix, func(t *testing.T) {
			srv := newRecordingModelServer(t)
			p := NewOllama(srv.URL+suffix, "qwen3:4b")

			resp, err := p.Complete(context.Background(), probeRequest())
			if err != nil {
				t.Fatalf("Complete with base %q: %v (paths hit: %s)", srv.URL+suffix, err, srv.hitPaths())
			}
			if resp == nil || resp.Content == "" {
				t.Fatalf("Complete with base %q returned no content", srv.URL+suffix)
			}
			if !srv.hit("/api/chat") {
				t.Fatalf("base %q never reached /api/chat; hit: %s", srv.URL+suffix, srv.hitPaths())
			}
		})
	}
}

// TestOllama_ListModelsAcceptsEveryStoredShape covers discovery on the same
// shapes — the model picker lists against this, so a "/v1" value used to make
// the picker come back empty and the operator assume the endpoint was down.
func TestOllama_ListModelsAcceptsEveryStoredShape(t *testing.T) {
	for _, suffix := range []string{"", "/v1", "/api/tags"} {
		t.Run("base"+suffix, func(t *testing.T) {
			srv := newRecordingModelServer(t)
			p := NewOllama(srv.URL+suffix, "")

			models, err := p.ListModels(context.Background())
			if err != nil {
				t.Fatalf("ListModels with base %q: %v (paths hit: %s)", srv.URL+suffix, err, srv.hitPaths())
			}
			if len(models) != 1 || models[0].ID != "qwen3:4b" {
				t.Fatalf("ListModels with base %q = %+v, want one qwen3:4b", srv.URL+suffix, models)
			}
		})
	}
}

// TestOpenAI_AcceptsEveryStoredShape is the mirror image for the compat wire:
// llm.OpenAI used the stored value verbatim as the POST target, so a ".../v1"
// credential POSTed to "/v1" and 404'd. Both the bare root and the "/v1" form
// must land on /v1/chat/completions.
func TestOpenAI_AcceptsEveryStoredShape(t *testing.T) {
	for _, suffix := range []string{"", "/v1", "/v1/chat/completions"} {
		t.Run("base"+suffix, func(t *testing.T) {
			srv := newRecordingModelServer(t)
			p := NewOpenAIWithBaseURL("test-key", srv.URL+suffix)

			resp, err := p.Complete(context.Background(), probeRequest())
			if err != nil {
				t.Fatalf("Complete with base %q: %v (paths hit: %s)", srv.URL+suffix, err, srv.hitPaths())
			}
			if resp == nil || resp.Content == "" {
				t.Fatalf("Complete with base %q returned no content", srv.URL+suffix)
			}
			if !srv.hit("/v1/chat/completions") {
				t.Fatalf("base %q never reached /v1/chat/completions; hit: %s", srv.URL+suffix, srv.hitPaths())
			}
		})
	}
}

// TestOpenAI_ListModelsAcceptsEveryStoredShape pins discovery on the compat wire.
func TestOpenAI_ListModelsAcceptsEveryStoredShape(t *testing.T) {
	for _, suffix := range []string{"", "/v1", "/v1/chat/completions"} {
		t.Run("base"+suffix, func(t *testing.T) {
			srv := newRecordingModelServer(t)
			p := NewOpenAIWithBaseURL("test-key", srv.URL+suffix)

			models, err := p.ListModels(context.Background())
			if err != nil {
				t.Fatalf("ListModels with base %q: %v (paths hit: %s)", srv.URL+suffix, err, srv.hitPaths())
			}
			if len(models) != 1 || models[0].ID != "qwen3:4b" {
				t.Fatalf("ListModels with base %q = %+v, want one qwen3:4b", srv.URL+suffix, models)
			}
		})
	}
}

// TestOpenAI_DefaultTargetUnchanged guards the normalization against regressing
// the hosted path: NewOpenAI must still POST to the real chat-completions URL.
func TestOpenAI_DefaultTargetUnchanged(t *testing.T) {
	p := NewOpenAI("k")
	if got := p.chatURL(); got != openaiAPIURL {
		t.Fatalf("default chat URL = %q, want %q", got, openaiAPIURL)
	}
}

// TestOpenAI_AzureQueryPreserved keeps Azure-style deployments working: the
// api-version query is part of addressing there, so normalization must not drop
// it from either the chat or the models URL.
func TestOpenAI_AzureQueryPreserved(t *testing.T) {
	const base = "https://acme.openai.azure.com/openai/v1/chat/completions?api-version=2026-02-01"
	p := NewOpenAIWithBaseURL("k", base)

	if got := p.chatURL(); got != base {
		t.Fatalf("chat URL = %q, want %q", got, base)
	}
	want := "https://acme.openai.azure.com/openai/v1/models?api-version=2026-02-01"
	if got := p.modelsURL(); got != want {
		t.Fatalf("models URL = %q, want %q", got, want)
	}
}

// TestOllama_PolicyRejectedBaseFallsBack keeps a base that normalization refuses
// on POLICY behaving exactly as it did before this package existed. An endpoint
// with embedded credentials is rejected as an endpoint value — but Go sends it
// as basic auth, so a deployment configured that way worked. Normalization is a
// repair, not a new gate: it must not turn a working oddball into a hard failure.
func TestOllama_PolicyRejectedBaseFallsBack(t *testing.T) {
	p := NewOllama("http://user:pass@ollama.internal:11434", "m")
	got := p.chatURL()
	if !strings.Contains(got, "user:pass@ollama.internal:11434") || !strings.HasSuffix(got, "/api/chat") {
		t.Fatalf("chat URL = %q, want the raw value preserved with /api/chat appended", got)
	}
}

// TestOpenAI_UnparseableBaseIsAParseError is the other half of that rule: a base
// that is not a URL at all never worked, so it surfaces as a parse error rather
// than as a confusing failure deeper in the request path.
func TestOpenAI_UnparseableBaseIsAParseError(t *testing.T) {
	p := NewOpenAIWithBaseURL("k", "http://bad\x7f.example/v1/chat/completions")
	if _, err := p.ListModels(context.Background()); err == nil ||
		!strings.Contains(err.Error(), "parse base url") {
		t.Fatalf("ListModels error = %v, want it to mention parse base url", err)
	}
	if _, err := p.Complete(context.Background(), probeRequest()); err == nil ||
		!strings.Contains(err.Error(), "parse base url") {
		t.Fatalf("Complete error = %v, want it to mention parse base url", err)
	}
}

// TestOpenAI_UnversionedBaseKeepsItsLayout pins the Azure deployments shape:
// ".../deployments/{name}/chat/completions" mounts the API with no "/v1"
// segment, so appending one would 404. Normalization has to carry that fact,
// not assume every server versions its API.
func TestOpenAI_UnversionedBaseKeepsItsLayout(t *testing.T) {
	p := NewOpenAIWithBaseURL("k", "https://acme.openai.azure.com/openai/deployments/x/chat/completions?api-version=2024-02-01")

	wantChat := "https://acme.openai.azure.com/openai/deployments/x/chat/completions?api-version=2024-02-01"
	if got := p.chatURL(); got != wantChat {
		t.Fatalf("chat URL = %q, want %q", got, wantChat)
	}
	// The model list is a property of the RESOURCE, not of one deployment:
	// Azure serves it at ".../openai/models". Deriving it from the deployment
	// root the chat path uses asks for a route Azure does not have, so an
	// endpoint whose completions work would fail discovery with a 404.
	wantModels := "https://acme.openai.azure.com/openai/models?api-version=2024-02-01"
	if got := p.modelsURL(); got != wantModels {
		t.Fatalf("models URL = %q, want %q", got, wantModels)
	}
}

// TestOllama_ChatBodyIsWellFormed is a guard on the request the judge actually
// sends — a decoding server must see the model and the messages, so a future
// refactor of the URL layer cannot quietly break the body.
func TestOllama_ChatBodyIsWellFormed(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"message":{"role":"assistant","content":"ok"},"done":true}`))
	}))
	defer srv.Close()

	p := NewOllama(srv.URL+"/v1", "qwen3:4b")
	if _, err := p.Complete(context.Background(), probeRequest()); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if got["model"] != "qwen3:4b" {
		t.Fatalf("model in body = %v, want qwen3:4b", got["model"])
	}
	if _, ok := got["messages"]; !ok {
		t.Fatalf("no messages in body: %+v", got)
	}
}
