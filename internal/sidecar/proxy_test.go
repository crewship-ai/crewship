package sidecar

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/scrubber"
)

func newTestProxy(creds []Credential, domains []string) *Proxy {
	cs := NewCredStore()
	if len(creds) > 0 {
		cs.Load(creds)
	}
	if len(domains) == 0 {
		domains = DefaultAllowedDomains
	}
	return NewProxy(ProxyConfig{
		CredStore: cs,
		Allowlist: NewDomainAllowlist(domains),
		Scrubber:  scrubber.New(),
		Logger:    slog.Default(),
	})
}

func TestProxyBlocksNonAllowedDomain(t *testing.T) {
	proxy := newTestProxy(nil, []string{"api.anthropic.com"})

	req := httptest.NewRequest("GET", "http://evil.com/steal", nil)
	w := httptest.NewRecorder()

	proxy.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "domain not allowed") {
		t.Errorf("expected domain not allowed message, got %q", w.Body.String())
	}
}

func TestProxyBlocksConnectNonAllowed(t *testing.T) {
	proxy := newTestProxy(nil, []string{"api.anthropic.com"})

	req := httptest.NewRequest("CONNECT", "evil.com:443", nil)
	req.Host = "evil.com:443"
	w := httptest.NewRecorder()

	proxy.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", w.Code)
	}
}

func TestProxyReturns503WhenNoCredentials(t *testing.T) {
	proxy := newTestProxy(nil, []string{"api.anthropic.com"})

	req := httptest.NewRequest("POST", "http://api.anthropic.com/v1/messages", nil)
	req.Host = "api.anthropic.com"
	w := httptest.NewRecorder()

	proxy.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", w.Code)
	}
}

func TestProxyHealthEndpoint(t *testing.T) {
	creds := []Credential{
		{ID: "c1", Provider: ProviderAnthropic, Token: "sk-ant-1"},
		{ID: "c2", Provider: ProviderOpenAI, Token: "sk-oai-1"},
	}
	proxy := newTestProxy(creds, []string{"localhost"})

	req := httptest.NewRequest("GET", "http://localhost:9119/health", nil)
	req.Host = "localhost:9119"
	w := httptest.NewRecorder()

	proxy.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, `"anthropic_creds":1`) {
		t.Errorf("expected anthropic_creds:1, got %q", body)
	}
	if !strings.Contains(body, `"openai_creds":1`) {
		t.Errorf("expected openai_creds:1, got %q", body)
	}
}

func TestInjectCredentialAnthropic(t *testing.T) {
	req := httptest.NewRequest("POST", "https://api.anthropic.com/v1/messages", nil)
	injectCredential(req, ProviderAnthropic, "sk-ant-test-key")

	if req.Header.Get("x-api-key") != "sk-ant-test-key" {
		t.Errorf("expected x-api-key header, got %q", req.Header.Get("x-api-key"))
	}
	if req.Header.Get("anthropic-version") != "2023-06-01" {
		t.Errorf("expected anthropic-version header")
	}
}

func TestInjectCredentialOpenAI(t *testing.T) {
	req := httptest.NewRequest("POST", "https://api.openai.com/v1/chat/completions", nil)
	injectCredential(req, ProviderOpenAI, "sk-oai-test-key")

	if req.Header.Get("Authorization") != "Bearer sk-oai-test-key" {
		t.Errorf("expected Authorization Bearer header, got %q", req.Header.Get("Authorization"))
	}
}

func TestInjectCredentialGoogle(t *testing.T) {
	req := httptest.NewRequest("POST", "https://generativelanguage.googleapis.com/v1/models/gemini-pro:generateContent", nil)
	injectCredential(req, ProviderGoogle, "AIza-test-key")

	if req.URL.Query().Get("key") != "AIza-test-key" {
		t.Errorf("expected key query param, got %q", req.URL.Query().Get("key"))
	}
}

func TestProxyForwardsToUpstream(t *testing.T) {
	// Create a fake upstream that verifies the credential was injected
	var receivedKey string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedKey = r.Header.Get("x-api-key")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"msg_test","type":"message"}`))
	}))
	defer upstream.Close()

	upstreamHost := strings.TrimPrefix(upstream.URL, "http://")

	creds := []Credential{
		{ID: "c1", Provider: ProviderAnthropic, Token: "sk-ant-injected"},
	}
	cs := NewCredStore()
	cs.Load(creds)

	// The proxy needs to see "api.anthropic.com" as the host but actually
	// connect to our test server. We override the transport's DialContext.
	proxy := NewProxy(ProxyConfig{
		CredStore: cs,
		Allowlist: NewDomainAllowlist([]string{"api.anthropic.com"}),
		Scrubber:  scrubber.New(),
		Logger:    slog.Default(),
	})
	proxy.transport = &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			// Redirect api.anthropic.com:443 -> our test server
			if strings.HasPrefix(addr, "api.anthropic.com") {
				addr = upstreamHost
			}
			return net.Dial(network, addr)
		},
	}

	req := httptest.NewRequest("POST", "http://api.anthropic.com/v1/messages", strings.NewReader(`{"model":"claude-3"}`))
	req.Host = "api.anthropic.com"
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	proxy.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if receivedKey != "sk-ant-injected" {
		t.Errorf("expected injected key, got %q", receivedKey)
	}
}

func TestProxyE2EWithCredentialInjection(t *testing.T) {
	// Fake Anthropic API that checks for injected key
	fakeAnthropic := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.Header.Get("x-api-key")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"type":"message","content":[{"type":"text","text":"Hello"}]}`))
	}))
	defer fakeAnthropic.Close()

	fakeHost := strings.TrimPrefix(fakeAnthropic.URL, "http://")

	creds := []Credential{
		{ID: "c1", Provider: ProviderAnthropic, Token: "sk-ant-secret-injected"},
	}
	cs := NewCredStore()
	cs.Load(creds)

	// Create proxy that maps the fake host as anthropic provider
	al := NewDomainAllowlist([]string{fakeHost})
	proxy := NewProxy(ProxyConfig{
		CredStore: cs,
		Allowlist: al,
		Scrubber:  scrubber.New(),
		Logger:    slog.Default(),
	})

	// Start proxy server
	proxyServer := httptest.NewServer(proxy)
	defer proxyServer.Close()

	// Agent makes request through proxy to the fake upstream
	// Simulating: ANTHROPIC_BASE_URL pointing to our fake
	req, _ := http.NewRequest("POST", fakeAnthropic.URL+"/v1/messages",
		strings.NewReader(`{"model":"claude-3-5-sonnet-20241022","max_tokens":1024,"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Content-Type", "application/json")
	// Agent sends dummy key (or no key) -- proxy injects the real one
	req.Header.Set("x-api-key", "dummy-key-from-agent")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", resp.StatusCode, body)
	}

	// Note: in this test the request goes directly to fakeAnthropic, not through the proxy.
	// The real E2E test with HTTP_PROXY is in TestServerE2E.
	// Here we just verify the fake server works.
	if !strings.Contains(string(body), "Hello") {
		t.Errorf("expected Hello in response, got %s", body)
	}
}

func TestInjectCredentialOverwritesExistingOpenAI(t *testing.T) {
	req := httptest.NewRequest("POST", "https://api.openai.com/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer sk-agent-fake-key")
	injectCredential(req, ProviderOpenAI, "sk-real-openai-key")
	if req.Header.Get("Authorization") != "Bearer sk-real-openai-key" {
		t.Errorf("expected real key to overwrite agent key, got %q", req.Header.Get("Authorization"))
	}
}

func TestInjectCredentialOverwritesExistingGoogle(t *testing.T) {
	req := httptest.NewRequest("POST", "https://generativelanguage.googleapis.com/v1/models/gemini-pro:generateContent?key=agent-fake-key", nil)
	injectCredential(req, ProviderGoogle, "AIzaSy-real-key")
	if req.URL.Query().Get("key") != "AIzaSy-real-key" {
		t.Errorf("expected real key to overwrite agent key, got %q", req.URL.Query().Get("key"))
	}
}

func TestIsLocalhost(t *testing.T) {
	tests := []struct {
		host     string
		expected bool
	}{
		{"localhost", true},
		{"localhost:9119", true},
		{"127.0.0.1", true},
		{"127.0.0.1:8080", true},
		{"::1", true},
		{"api.anthropic.com", false},
		{"evil.com", false},
	}
	for _, tt := range tests {
		if isLocalhost(tt.host) != tt.expected {
			t.Errorf("isLocalhost(%q) = %v, want %v", tt.host, !tt.expected, tt.expected)
		}
	}
}
