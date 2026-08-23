package sidecar

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestServerStartAndShutdown(t *testing.T) {
	srv := NewServer(ServerConfig{
		Addr:   "127.0.0.1:0", // random port
		Logger: slog.Default(),
		Credentials: []Credential{
			{ID: "c1", Provider: ProviderAnthropic, Token: "sk-ant-1"},
		},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.Start(ctx)
	}()

	// Give it a moment to start
	time.Sleep(100 * time.Millisecond)
	cancel()

	if err := <-errCh; err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestServerCredStoreAccess(t *testing.T) {
	srv := NewServer(ServerConfig{
		Credentials: []Credential{
			{ID: "c1", Provider: ProviderAnthropic, Token: "sk-ant-1"},
			{ID: "c2", Provider: ProviderOpenAI, Token: "sk-oai-1"},
		},
	})

	cs := srv.CredStore()
	if cs.Count(ProviderAnthropic) != 1 {
		t.Errorf("expected 1 anthropic cred, got %d", cs.Count(ProviderAnthropic))
	}
	if cs.Count(ProviderOpenAI) != 1 {
		t.Errorf("expected 1 openai cred, got %d", cs.Count(ProviderOpenAI))
	}
}

func TestServerAllowlistAccess(t *testing.T) {
	srv := NewServer(ServerConfig{
		AllowedDomains: []string{"custom.api.com"},
	})

	al := srv.Allowlist()
	if !al.IsAllowed("api.anthropic.com") {
		t.Error("default domain should be allowed")
	}
	if !al.IsAllowed("custom.api.com") {
		t.Error("custom domain should be allowed")
	}
	if al.IsAllowed("evil.com") {
		t.Error("unknown domain should not be allowed")
	}
}

func TestServerE2EHealthCheck(t *testing.T) {
	srv := NewServer(ServerConfig{
		Addr: "127.0.0.1:0",
		Credentials: []Credential{
			{ID: "c1", Provider: ProviderAnthropic, Token: "sk-ant-1"},
		},
	})

	// Test via httptest.NewRecorder instead of real server
	req := httptest.NewRequest("GET", "http://localhost:9119/health", nil)
	req.Host = "localhost:9119"
	req.RemoteAddr = "127.0.0.1:54321"
	w := httptest.NewRecorder()

	srv.proxy.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), `"anthropic_creds":1`) {
		t.Errorf("expected anthropic_creds:1, got %q", w.Body.String())
	}
}

// TestNewServer_RegistersCredentialLiteralsWithTheScrubber covers §5.3: the
// memory-write scrubber's built-in patterns are SHAPE-based (sk-ant-…,
// AIzaSy…, sk-or-…), which is exactly what a bring-your-own endpoint's token
// does not have — a self-hosted LiteLLM or vLLM proxy issues whatever string
// its operator configured. The sidecar knows the literal, so it registers it.
//
// scrubber.AddPattern had zero production callers before this; a shapeless key
// written into crew memory was previously stored verbatim.
func TestNewServer_RegistersCredentialLiteralsWithTheScrubber(t *testing.T) {
	const shapeless = "my-litellm-gateway-token-9f2c1"      // no recognisable prefix
	const shaped = "sk-ant-api03-" + "AAAABBBBCCCCDDDDEEEE" // already caught by anthropic_key

	srv := NewServer(ServerConfig{
		Addr:   "127.0.0.1:0",
		Logger: slog.Default(),
		Credentials: []Credential{
			{ID: "compat-1", Provider: ProviderOpenAICompat, Token: shapeless, BaseURL: "https://llm.internal.example/v1"},
			{ID: "anth-1", Provider: ProviderAnthropic, Token: shaped},
			// Below the length guard. Registering a short (or empty) literal
			// compiles to a pattern that matches everywhere and would redact
			// the entire memory surface — the guard is why this one is skipped.
			{ID: "tiny-1", Provider: ProviderOpenAI, Token: "ab"},
		},
	})

	s := srv.memoryScrubber()
	if s == nil {
		t.Fatal("server has no scrubber")
	}

	if out := s.Scrub("endpoint token is " + shapeless); strings.Contains(out, shapeless) {
		t.Errorf("a shapeless credential survived scrubbing: %q", out)
	}
	if out := s.Scrub("key " + shaped); strings.Contains(out, shaped) {
		t.Errorf("a shaped credential survived scrubbing: %q", out)
	}
	// The short token must NOT have become a pattern — if it had, ordinary
	// prose containing "ab" would come back redacted.
	if out := s.Scrub("about the abstract label"); out != "about the abstract label" {
		t.Errorf("a sub-minimum token was registered as a pattern and is redacting prose: %q", out)
	}
}
