package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestProbeLocalModelEndpoint_OpenAICompat(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"data":[{"id":"qwen2.5-coder:7b"},{"id":"llama3"}]}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	res := probeLocalModelEndpoint(context.Background(), srv.URL+"/v1")
	if !res.Valid {
		t.Fatalf("expected reachable, got error=%q", res.Error)
	}
	if want := "2 model"; !strings.Contains(res.Error, want) {
		t.Fatalf("expected model count note containing %q, got %q", want, res.Error)
	}
}

func TestProbeLocalModelEndpoint_OllamaNativeFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			http.Error(w, "not found", http.StatusNotFound)
		case "/api/tags":
			w.Write([]byte(`{"models":[{"name":"qwen2.5-coder:7b"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	res := probeLocalModelEndpoint(context.Background(), srv.URL+"/v1")
	if !res.Valid {
		t.Fatalf("expected reachable via /api/tags fallback, got error=%q", res.Error)
	}
}

func TestProbeLocalModelEndpoint_Unreachable(t *testing.T) {
	// A port that nothing listens on → connection refused, reported not-valid.
	res := probeLocalModelEndpoint(context.Background(), "http://127.0.0.1:1/v1")
	if res.Valid {
		t.Fatal("expected unreachable endpoint to report not valid")
	}
	if res.Error == "" {
		t.Fatal("expected an error message for an unreachable endpoint")
	}
}

func TestEndpointDialControl_BlocksMetadata(t *testing.T) {
	blocked := []string{"169.254.169.254:80", "169.254.0.1:443", "[fe80::1]:80", "224.0.0.1:80", "0.0.0.0:80"}
	for _, addr := range blocked {
		if err := endpointDialControl("tcp", addr, nil); err == nil {
			t.Errorf("expected %s to be refused by endpointDialControl", addr)
		}
	}
	allowed := []string{"192.168.1.10:11434", "10.0.0.5:11434", "127.0.0.1:11434", "8.8.8.8:443"}
	for _, addr := range allowed {
		if err := endpointDialControl("tcp", addr, nil); err != nil {
			t.Errorf("expected %s to be allowed (private/loopback/public are fine here), got %v", addr, err)
		}
	}
}

// SSRF regression (#980 review): the unauthenticated body-Test path
// (probeProvider dialEndpoint=false) must NEVER dial a caller-supplied
// ENDPOINT_URL host. It syntax-validates only; a live server at the URL must
// receive no request. The role-gated stored path (dialEndpoint=true) does dial.
func TestProbeProvider_BodyPathDoesNotDialEndpoint(t *testing.T) {
	var hit int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hit, 1)
		w.Write([]byte(`{"data":[{"id":"m"}]}`))
	}))
	defer srv.Close()

	// Body path: dialEndpoint=false -> no network call, valid-if-well-formed.
	res := probeProvider(context.Background(), "OLLAMA", string(CredTypeEndpointURL), srv.URL+"/v1", false)
	if !res.Valid {
		t.Fatalf("well-formed URL should validate on the body path, got error=%q", res.Error)
	}
	if got := atomic.LoadInt32(&hit); got != 0 {
		t.Fatalf("body-Test path dialed the endpoint %d time(s) — SSRF: it must NOT make a network call", got)
	}

	// A malformed URL is rejected by syntax validation (still no dial).
	if r := probeProvider(context.Background(), "OLLAMA", string(CredTypeEndpointURL), "not-a-url", false); r.Valid {
		t.Fatal("malformed endpoint URL should be rejected on the body path")
	}
	if got := atomic.LoadInt32(&hit); got != 0 {
		t.Fatalf("body path dialed on a malformed URL (%d) — must not", got)
	}

	// Role-gated stored path: dialEndpoint=true DOES probe.
	res = probeProvider(context.Background(), "OLLAMA", string(CredTypeEndpointURL), srv.URL+"/v1", true)
	if !res.Valid {
		t.Fatalf("stored path should reach the live endpoint, got error=%q", res.Error)
	}
	if got := atomic.LoadInt32(&hit); got == 0 {
		t.Fatal("stored path (dialEndpoint=true) should have dialed the endpoint at least once")
	}
}

// Bare base (no /v1) must still find models via the Ollama-native /api/tags
// fallback instead of being reported unreachable after only /models 404s.
func TestProbeLocalModelEndpoint_BareBaseUsesTags(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models":
			http.Error(w, "not found", http.StatusNotFound)
		case "/api/tags":
			w.Write([]byte(`{"models":[{"name":"qwen2.5-coder:7b"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	res := probeLocalModelEndpoint(context.Background(), srv.URL) // bare base, no /v1
	if !res.Valid {
		t.Fatalf("bare base should reach /api/tags, got error=%q", res.Error)
	}
}
