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
	w := httptest.NewRecorder()

	srv.proxy.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), `"anthropic_creds":1`) {
		t.Errorf("expected anthropic_creds:1, got %q", w.Body.String())
	}
}
