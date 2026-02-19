package sidecar

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/scrubber"
)

// silentLogger discards all log output for benchmarks.
var silentLogger = slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))

func BenchmarkCredStoreSelect(b *testing.B) {
	cs := NewCredStore()
	cs.Load([]Credential{
		{ID: "c1", Provider: ProviderAnthropic, Token: "sk-ant-1"},
		{ID: "c2", Provider: ProviderAnthropic, Token: "sk-ant-2"},
		{ID: "c3", Provider: ProviderAnthropic, Token: "sk-ant-3"},
	})
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cs.Select(ProviderAnthropic)
	}
}

func BenchmarkDomainAllowlistLookup(b *testing.B) {
	al := NewDomainAllowlist(DefaultAllowedDomains)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		al.IsAllowed("api.anthropic.com:443")
	}
}

func BenchmarkDomainAllowlistLookupBlocked(b *testing.B) {
	al := NewDomainAllowlist(DefaultAllowedDomains)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		al.IsAllowed("evil.com")
	}
}

func BenchmarkInjectCredentialAnthropic(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest("POST", "https://api.anthropic.com/v1/messages", nil)
		injectCredential(req, ProviderAnthropic, "sk-ant-benchmark-key-1234567890")
	}
}

func BenchmarkInjectCredentialOpenAI(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest("POST", "https://api.openai.com/v1/chat/completions", nil)
		injectCredential(req, ProviderOpenAI, "sk-benchmark-key-1234567890")
	}
}

func BenchmarkProxyBlockedDomain(b *testing.B) {
	cs := NewCredStore()
	proxy := NewProxy(ProxyConfig{
		CredStore: cs,
		Allowlist: NewDomainAllowlist([]string{"api.anthropic.com"}),
		Scrubber:  scrubber.New(),
		Logger:    silentLogger,
	})
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest("GET", "http://evil.com/steal", nil)
		w := httptest.NewRecorder()
		proxy.ServeHTTP(w, req)
	}
}

func BenchmarkProxyHealthEndpoint(b *testing.B) {
	creds := []Credential{
		{ID: "c1", Provider: ProviderAnthropic, Token: "sk-ant-1"},
	}
	proxy := newTestProxy(creds, append(DefaultAllowedDomains, "localhost"))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest("GET", "http://localhost:9119/health", nil)
		req.Host = "localhost:9119"
		w := httptest.NewRecorder()
		proxy.ServeHTTP(w, req)
	}
}

func BenchmarkProxyFullPipeline(b *testing.B) {
	// Simulate the full proxy pipeline: allowlist check + cred select + inject
	// (without actual HTTP round-trip to upstream)
	cs := NewCredStore()
	cs.Load([]Credential{
		{ID: "c1", Provider: ProviderAnthropic, Token: "sk-ant-bench-key"},
		{ID: "c2", Provider: ProviderAnthropic, Token: "sk-ant-bench-key2"},
	})
	al := NewDomainAllowlist(DefaultAllowedDomains)
	_ = scrubber.New()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		host := "api.anthropic.com"
		al.IsAllowed(host)
		provider := providerForHost(host)
		cred := cs.Select(provider)
		req := httptest.NewRequest("POST", "https://api.anthropic.com/v1/messages",
			strings.NewReader(`{"model":"claude-3","max_tokens":1024}`))
		injectCredential(req, provider, cred.Token)
	}
}

func BenchmarkScrubberInPipeline(b *testing.B) {
	// Benchmark scrubber processing typical agent output lines
	s := scrubber.New()
	lines := []string{
		"Analyzing the code in main.go...",
		"Found 3 issues with the implementation",
		"The API key sk-ant-api03-secretkey1234 was exposed in the config file",
		`{"type":"text","content":"Here is the fix for your code"}`,
		"Normal output line with no secrets at all, just regular text from the agent",
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, line := range lines {
			s.Scrub(line)
		}
	}
}

// BenchmarkProxyServeHTTPNoCredential measures the 503 path (no cred available)
func BenchmarkProxyServeHTTPNoCredential(b *testing.B) {
	proxy := NewProxy(ProxyConfig{
		CredStore: NewCredStore(), // empty
		Allowlist: NewDomainAllowlist(DefaultAllowedDomains),
		Scrubber:  scrubber.New(),
		Logger:    silentLogger,
	})
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest("POST", "http://api.anthropic.com/v1/messages", nil)
		req.Host = "api.anthropic.com"
		w := httptest.NewRecorder()
		proxy.ServeHTTP(w, req)
		if w.Code != http.StatusServiceUnavailable {
			b.Fatalf("expected 503, got %d", w.Code)
		}
	}
}
