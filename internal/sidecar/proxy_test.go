package sidecar

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/llmroute"
	"github.com/crewship-ai/crewship/internal/scrubber"
)

// roundTripFunc allows using a plain function as an http.RoundTripper in tests.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// redirectTransport returns an http.RoundTripper that rewrites requests
// destined for targetHost to the given upstream (HTTP) test server address.
// This avoids TLS handshake issues when testing reverse proxy logic.
func redirectTransport(targetHost, upstream string) http.RoundTripper {
	return roundTripFunc(func(r *http.Request) (*http.Response, error) {
		r2 := r.Clone(r.Context())
		if strings.HasPrefix(r2.URL.Host, targetHost) || r2.URL.Host == "" {
			r2.URL.Scheme = "http"
			r2.URL.Host = upstream
		}
		return http.DefaultTransport.RoundTrip(r2)
	})
}

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

// TestNewProxy_ConfigTransportOverride verifies ProxyConfig.Transport is
// honored from OUTSIDE the package (via NewProxy), not just via the direct
// `proxy.transport = …` field assignment every other test in this file uses.
// This is the seam a future replay/cassette-backed RoundTripper (quality/
// testability plan item A4) needs: something outside internal/sidecar (the
// sidecar's main(), gated on a replay-mode flag) constructing a Proxy that
// never touches the real network.
func TestNewProxy_ConfigTransportOverride(t *testing.T) {
	var sawRequest bool
	fake := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		sawRequest = true
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"type":"message"}`)),
			Request:    r,
		}, nil
	})

	cs := NewCredStore()
	proxy := NewProxy(ProxyConfig{
		CredStore: cs,
		Allowlist: NewDomainAllowlist(DefaultAllowedDomains),
		Scrubber:  scrubber.New(),
		Logger:    slog.Default(),
		Transport: fake,
	})

	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{}`))
	req.Host = "127.0.0.1:9119"
	req.RemoteAddr = "127.0.0.1:54321"
	req.Header.Set("Authorization", "Bearer sk-ant-oat01-my-oauth-token")
	w := httptest.NewRecorder()
	proxy.ServeHTTP(w, req)

	if !sawRequest {
		t.Fatal("expected the configured Transport override to receive the forwarded request")
	}
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
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
	req.RemoteAddr = "127.0.0.1:54321"
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

// specFor is the test-side lookup for a provider's descriptor. Every
// injectCredential(req, provider, token) call in this package became
// llmroute.ApplyAuth(req, specFor(t, provider), token, nil): the auth table
// moved out of proxy.go's switch and into the descriptor, so the assertions
// below now exercise the descriptor row AND the writer in one call — which is
// what the proxy itself does.
func specFor(t *testing.T, provider ProviderType) llmroute.Spec {
	t.Helper()
	s, ok := llmroute.Lookup(string(provider))
	if !ok {
		t.Fatalf("no llmroute spec registered for provider %q; the sidecar routes it, so the descriptor must describe it", provider)
	}
	return s
}

func TestInjectCredentialAnthropic(t *testing.T) {
	req := httptest.NewRequest("POST", "https://api.anthropic.com/v1/messages", nil)
	llmroute.ApplyAuth(req, specFor(t, ProviderAnthropic), "sk-ant-test-key", nil)

	if req.Header.Get("x-api-key") != "sk-ant-test-key" {
		t.Errorf("expected x-api-key header, got %q", req.Header.Get("x-api-key"))
	}
	if req.Header.Get("anthropic-version") != "2023-06-01" {
		t.Errorf("expected anthropic-version header")
	}
}

func TestInjectCredentialOpenAI(t *testing.T) {
	req := httptest.NewRequest("POST", "https://api.openai.com/v1/chat/completions", nil)
	llmroute.ApplyAuth(req, specFor(t, ProviderOpenAI), "sk-oai-test-key", nil)

	if req.Header.Get("Authorization") != "Bearer sk-oai-test-key" {
		t.Errorf("expected Authorization Bearer header, got %q", req.Header.Get("Authorization"))
	}
}

func TestInjectCredentialGoogle(t *testing.T) {
	req := httptest.NewRequest("POST", "https://generativelanguage.googleapis.com/v1/models/gemini-pro:generateContent", nil)
	llmroute.ApplyAuth(req, specFor(t, ProviderGoogle), "AIza-test-key", nil)

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
	// Fake Anthropic API that captures the injected key
	var receivedKey string
	fakeAnthropic := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedKey = r.Header.Get("x-api-key")
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

	al := NewDomainAllowlist([]string{"api.anthropic.com"})
	proxy := NewProxy(ProxyConfig{
		CredStore: cs,
		Allowlist: al,
		Scrubber:  scrubber.New(),
		Logger:    slog.Default(),
	})
	// Redirect api.anthropic.com to our fake upstream
	proxy.transport = &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if strings.HasPrefix(addr, "api.anthropic.com") {
				addr = fakeHost
			}
			return net.Dial(network, addr)
		},
	}

	// Start proxy server
	proxyServer := httptest.NewServer(proxy)
	defer proxyServer.Close()

	// Route request through the proxy using HTTP_PROXY
	proxyURL, _ := url.Parse(proxyServer.URL)
	client := &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyURL(proxyURL),
		},
	}

	req, _ := http.NewRequest("POST", "http://api.anthropic.com/v1/messages",
		strings.NewReader(`{"model":"claude-3-5-sonnet-20241022","max_tokens":1024,"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", "dummy-key-from-agent")

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "Hello") {
		t.Errorf("expected Hello in response, got %s", body)
	}
	// Verify the proxy injected the real credential, overwriting the dummy
	if receivedKey != "sk-ant-secret-injected" {
		t.Errorf("expected injected key, got %q", receivedKey)
	}
}

func TestInjectCredentialOverwritesExistingOpenAI(t *testing.T) {
	req := httptest.NewRequest("POST", "https://api.openai.com/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer sk-agent-fake-key")
	llmroute.ApplyAuth(req, specFor(t, ProviderOpenAI), "sk-real-openai-key", nil)
	if req.Header.Get("Authorization") != "Bearer sk-real-openai-key" {
		t.Errorf("expected real key to overwrite agent key, got %q", req.Header.Get("Authorization"))
	}
}

func TestInjectCredentialOverwritesExistingGoogle(t *testing.T) {
	req := httptest.NewRequest("POST", "https://generativelanguage.googleapis.com/v1/models/gemini-pro:generateContent?key=agent-fake-key", nil)
	llmroute.ApplyAuth(req, specFor(t, ProviderGoogle), "AIzaSy-real-key", nil)
	if req.URL.Query().Get("key") != "AIzaSy-real-key" {
		t.Errorf("expected real key to overwrite agent key, got %q", req.URL.Query().Get("key"))
	}
}

// TestHealth_CountPerDescriptor: /health carries one provider_creds entry per
// registered descriptor, including the ones with no credential (a missing key
// and a zero are the same fact, and an absent key reads as "this sidecar has
// never heard of that provider"). Providers WITHOUT a descriptor — CURSOR,
// FACTORY, which are env-injected and never proxied — must not appear: the map
// describes what the router can route, not what the store happens to hold.
func TestHealth_CountPerDescriptor(t *testing.T) {
	cs := NewCredStore()
	cs.Load([]Credential{
		{ID: "a1", Provider: ProviderAnthropic, Token: "sk-ant-1"},
		{ID: "r1", Provider: ProviderOpenRouter, Token: "sk-or-v1-1"},
		{ID: "r2", Provider: ProviderOpenRouter, Token: "sk-or-v1-2"},
		{ID: "x1", Provider: ProviderCursor, Token: "cur_not_proxied"},
	})
	proxy := NewProxy(ProxyConfig{
		CredStore: cs,
		Allowlist: NewDomainAllowlist(nil),
		Logger:    slog.Default(),
	})

	req := httptest.NewRequest("GET", "http://127.0.0.1:9119/health", nil)
	req.Host = "127.0.0.1:9119"
	req.RemoteAddr = "127.0.0.1:54321"
	w := httptest.NewRecorder()
	proxy.ServeHTTP(w, req)

	var got struct {
		ProviderCreds map[string]int `json:"provider_creds"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal health: %v; body=%s", err, w.Body.String())
	}

	specs := llmroute.Specs()
	if len(specs) == 0 {
		t.Fatal("llmroute.Specs() is empty; this assertion would pass vacuously")
	}
	if len(got.ProviderCreds) != len(specs) {
		t.Errorf("provider_creds has %d entries, want one per descriptor (%d): %v", len(got.ProviderCreds), len(specs), got.ProviderCreds)
	}
	for _, s := range specs {
		if _, ok := got.ProviderCreds[s.ID]; !ok {
			t.Errorf("provider_creds is missing %s; every descriptor must report, zero included", s.ID)
		}
	}
	if got.ProviderCreds["ANTHROPIC"] != 1 || got.ProviderCreds["OPENROUTER"] != 2 {
		t.Errorf("counts = %v, want ANTHROPIC:1 OPENROUTER:2", got.ProviderCreds)
	}
	if _, ok := got.ProviderCreds[string(ProviderCursor)]; ok {
		t.Errorf("provider_creds reports CURSOR, which has no descriptor and is never proxied: %v", got.ProviderCreds)
	}
}

// TestProxyDirectRequestReverseProxiesV1Path verifies that direct requests to the sidecar
// (via ANTHROPIC_BASE_URL=http://127.0.0.1:9119) on /v1/* paths are reverse-proxied to
// api.anthropic.com with credential injection.
func TestProxyDirectRequestReverseProxiesV1Path(t *testing.T) {
	var receivedKey, receivedPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedKey = r.Header.Get("x-api-key")
		receivedPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"type":"message"}`))
	}))
	defer upstream.Close()
	upstreamHost := strings.TrimPrefix(upstream.URL, "http://")

	creds := []Credential{
		{ID: "c1", Provider: ProviderAnthropic, Token: "sk-ant-real-key"},
	}
	proxy := newTestProxy(creds, DefaultAllowedDomains)
	// Use redirectTransport so HTTPS scheme is rewritten to HTTP for the test server
	proxy.transport = redirectTransport("api.anthropic.com", upstreamHost)

	// Simulate ANTHROPIC_BASE_URL=http://127.0.0.1:9119 — direct HTTP request, not proxy
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{}`))
	req.Host = "127.0.0.1:9119"
	req.RemoteAddr = "127.0.0.1:54321"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", "sk-ant-dummy-crewship-sidecar")
	w := httptest.NewRecorder()

	proxy.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if receivedKey != "sk-ant-real-key" {
		t.Errorf("expected injected real API key, got %q", receivedKey)
	}
	if receivedPath != "/v1/messages" {
		t.Errorf("expected /v1/messages path forwarded, got %q", receivedPath)
	}
}

// TestProxyDirectRequestOAuthPassthrough verifies that direct requests with OAuth Bearer
// auth are forwarded as-is (no sidecar key injection) when no API key is in CredStore.
func TestProxyDirectRequestOAuthPassthrough(t *testing.T) {
	var receivedAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"type":"message"}`))
	}))
	defer upstream.Close()
	upstreamHost := strings.TrimPrefix(upstream.URL, "http://")

	// Empty CredStore — OAuth token not stored here, injected as env var instead
	proxy := newTestProxy(nil, DefaultAllowedDomains)
	proxy.transport = redirectTransport("api.anthropic.com", upstreamHost)

	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{}`))
	req.Host = "127.0.0.1:9119"
	req.RemoteAddr = "127.0.0.1:54321"
	req.Header.Set("Authorization", "Bearer sk-ant-oat01-my-oauth-token")
	w := httptest.NewRecorder()

	proxy.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if receivedAuth != "Bearer sk-ant-oat01-my-oauth-token" {
		t.Errorf("expected OAuth Bearer token forwarded unchanged, got %q", receivedAuth)
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
