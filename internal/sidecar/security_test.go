package sidecar

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/crewship-ai/crewship/internal/scrubber"
)

// --- Allowlist bypass attempts ---

func TestSecurityAllowlistTrailingDot(t *testing.T) {
	al := NewDomainAllowlist([]string{"api.anthropic.com"})
	// Trailing dot is valid DNS but must not bypass allowlist
	if al.IsAllowed("api.anthropic.com.") {
		t.Error("trailing dot should not bypass allowlist")
	}
}

func TestSecurityAllowlistSubdomain(t *testing.T) {
	al := NewDomainAllowlist([]string{"api.anthropic.com"})
	if al.IsAllowed("evil.api.anthropic.com") {
		t.Error("subdomain should not match exact domain allowlist")
	}
}

func TestSecurityAllowlistSuffixAttack(t *testing.T) {
	al := NewDomainAllowlist([]string{"api.anthropic.com"})
	// Attacker registers api.anthropic.com.evil.com
	if al.IsAllowed("api.anthropic.com.evil.com") {
		t.Error("suffix-based domain should not match")
	}
}

func TestSecurityAllowlistNonStandardPort(t *testing.T) {
	al := NewDomainAllowlist([]string{"api.anthropic.com"})
	// Port 8443 should still match (port is stripped)
	if !al.IsAllowed("api.anthropic.com:8443") {
		t.Error("non-standard port on allowed domain should be allowed")
	}
	// But evil domain with any port should not
	if al.IsAllowed("evil.com:443") {
		t.Error("evil domain with port 443 should not be allowed")
	}
}

func TestSecurityAllowlistEmptyHost(t *testing.T) {
	al := NewDomainAllowlist(DefaultAllowedDomains)
	if al.IsAllowed("") {
		t.Error("empty host should not be allowed")
	}
}

func TestSecurityAllowlistIPAddress(t *testing.T) {
	al := NewDomainAllowlist([]string{"api.anthropic.com"})
	// Attacker tries to use the IP of api.anthropic.com directly
	if al.IsAllowed("104.18.32.68") {
		t.Error("IP address should not bypass domain allowlist")
	}
}

// --- Proxy host header attacks ---

func TestSecurityDoubleHostHeader(t *testing.T) {
	proxy := newTestProxy(nil, []string{"api.anthropic.com"})

	req := httptest.NewRequest("GET", "http://api.anthropic.com/v1/messages", nil)
	req.Host = "evil.com" // Smuggle a different Host header
	w := httptest.NewRecorder()

	proxy.ServeHTTP(w, req)

	// The proxy should use r.Host (evil.com), which is NOT on the allowlist
	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403 for smuggled Host header, got %d", w.Code)
	}
}

func TestSecurityHostCaseSensitivity(t *testing.T) {
	proxy := newTestProxy(nil, []string{"api.anthropic.com"})

	for _, host := range []string{"API.ANTHROPIC.COM", "Api.Anthropic.Com", "api.ANTHROPIC.com"} {
		req := httptest.NewRequest("POST", "http://"+host+"/v1/messages", nil)
		req.Host = host
		w := httptest.NewRecorder()
		proxy.ServeHTTP(w, req)
		// Should match (case-insensitive) -- will get 503 (no creds) not 403 (blocked)
		if w.Code == http.StatusForbidden {
			t.Errorf("case variation %q should pass allowlist", host)
		}
	}
}

// --- Credential injection security ---

func TestSecurityCredentialNotInjectedForNonProvider(t *testing.T) {
	// api.factory.ai is allowed but not a known LLM provider
	var receivedAuthHeader string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuthHeader = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	upstreamHost := strings.TrimPrefix(upstream.URL, "http://")

	creds := []Credential{
		{ID: "c1", Provider: ProviderAnthropic, Token: "sk-ant-secret"},
	}
	cs := NewCredStore()
	cs.Load(creds)

	proxy := NewProxy(ProxyConfig{
		CredStore: cs,
		Allowlist: NewDomainAllowlist([]string{upstreamHost}),
		Scrubber:  scrubber.New(),
		Logger:    silentLogger,
	})

	req := httptest.NewRequest("GET", "http://"+upstreamHost+"/api/v1/data", nil)
	req.Host = upstreamHost
	w := httptest.NewRecorder()
	proxy.ServeHTTP(w, req)

	if receivedAuthHeader != "" {
		t.Error("credential should NOT be injected for non-provider domain")
	}
}

func TestSecurityCredentialOverwritesAgentHeader(t *testing.T) {
	// Agent tries to set its own API key -- proxy must overwrite it
	var receivedKey string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedKey = r.Header.Get("x-api-key")
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	upstreamHost := strings.TrimPrefix(upstream.URL, "http://")

	creds := []Credential{
		{ID: "c1", Provider: ProviderAnthropic, Token: "sk-ant-real-key"},
	}
	cs := NewCredStore()
	cs.Load(creds)

	proxy := NewProxy(ProxyConfig{
		CredStore: cs,
		Allowlist: NewDomainAllowlist([]string{"api.anthropic.com"}),
		Scrubber:  scrubber.New(),
		Logger:    silentLogger,
	})
	proxy.transport = &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if strings.HasPrefix(addr, "api.anthropic.com") {
				addr = upstreamHost
			}
			return net.Dial(network, addr)
		},
	}

	req := httptest.NewRequest("POST", "http://api.anthropic.com/v1/messages", strings.NewReader("{}"))
	req.Host = "api.anthropic.com"
	req.Header.Set("x-api-key", "sk-ant-attacker-injected-key")
	w := httptest.NewRecorder()
	proxy.ServeHTTP(w, req)

	if receivedKey != "sk-ant-real-key" {
		t.Errorf("proxy must overwrite agent-supplied key; got %q", receivedKey)
	}
}

func TestSecurityEmptyCredentialToken(t *testing.T) {
	cs := NewCredStore()
	cs.Load([]Credential{
		{ID: "c1", Provider: ProviderAnthropic, Token: ""},
	})
	cred := cs.Select(ProviderAnthropic)
	if cred == nil {
		t.Fatal("expected credential")
	}

	req := httptest.NewRequest("POST", "https://api.anthropic.com/v1/messages", nil)
	injectCredential(req, ProviderAnthropic, cred.Token)

	// Empty token should still set the header as an empty value (it's the store's
	// responsibility to not have empty tokens). The assertion here is that the
	// call did NOT crash — reaching this line means we survived injectCredential.
	_ = req.Header.Get("x-api-key")
}

func TestSecurityCredentialNotLeakedIn503Response(t *testing.T) {
	// When no cred available, 503 response body must not contain any credential info
	creds := []Credential{
		{ID: "c1", Provider: ProviderOpenAI, Token: "sk-super-secret-key"},
	}
	proxy := NewProxy(ProxyConfig{
		CredStore: func() *CredStore {
			cs := NewCredStore()
			cs.Load(creds)
			return cs
		}(),
		Allowlist: NewDomainAllowlist(DefaultAllowedDomains),
		Scrubber:  scrubber.New(),
		Logger:    silentLogger,
	})

	// Request for Anthropic but only OpenAI creds available
	req := httptest.NewRequest("POST", "http://api.anthropic.com/v1/messages", nil)
	req.Host = "api.anthropic.com"
	w := httptest.NewRecorder()
	proxy.ServeHTTP(w, req)

	body := w.Body.String()
	if strings.Contains(body, "sk-super-secret-key") {
		t.Error("credential leaked in 503 error response")
	}
	if strings.Contains(body, "sk-ant") {
		t.Error("credential pattern found in error response")
	}
}

func TestSecurityCredentialNotLeakedIn502Response(t *testing.T) {
	creds := []Credential{
		{ID: "c1", Provider: ProviderAnthropic, Token: "sk-ant-very-secret"},
	}
	cs := NewCredStore()
	cs.Load(creds)

	proxy := NewProxy(ProxyConfig{
		CredStore: cs,
		Allowlist: NewDomainAllowlist(DefaultAllowedDomains),
		Scrubber:  scrubber.New(),
		Logger:    silentLogger,
	})
	// Force transport to fail with unreachable host
	proxy.transport = &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return nil, &net.OpError{Op: "dial", Err: &net.DNSError{Err: "no such host"}}
		},
	}

	req := httptest.NewRequest("POST", "http://api.anthropic.com/v1/messages", nil)
	req.Host = "api.anthropic.com"
	w := httptest.NewRecorder()
	proxy.ServeHTTP(w, req)

	body := w.Body.String()
	if strings.Contains(body, "sk-ant-very-secret") {
		t.Error("credential leaked in 502 error response")
	}
}

// --- Hop-by-hop header stripping ---

func TestSecurityHopByHopHeadersStripped(t *testing.T) {
	var receivedHeaders http.Header
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHeaders = r.Header.Clone()
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()
	upstreamHost := strings.TrimPrefix(upstream.URL, "http://")

	creds := []Credential{{ID: "c1", Provider: ProviderAnthropic, Token: "sk-ant-test"}}
	cs := NewCredStore()
	cs.Load(creds)

	proxy := NewProxy(ProxyConfig{
		CredStore: cs,
		Allowlist: NewDomainAllowlist([]string{"api.anthropic.com"}),
		Scrubber:  scrubber.New(),
		Logger:    silentLogger,
	})
	proxy.transport = &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if strings.HasPrefix(addr, "api.anthropic.com") {
				addr = upstreamHost
			}
			return net.Dial(network, addr)
		},
	}

	req := httptest.NewRequest("POST", "http://api.anthropic.com/v1/messages", strings.NewReader("{}"))
	req.Host = "api.anthropic.com"
	req.Header.Set("Proxy-Authorization", "Basic attacker-creds")
	req.Header.Set("Connection", "keep-alive")
	req.Header.Set("Keep-Alive", "timeout=5")
	req.Header.Set("Transfer-Encoding", "chunked")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	proxy.ServeHTTP(w, req)

	// Verify hop-by-hop headers were stripped
	for _, h := range hopByHopHeaders {
		if receivedHeaders.Get(h) != "" {
			t.Errorf("hop-by-hop header %q was not stripped: %q", h, receivedHeaders.Get(h))
		}
	}
	// Non-hop-by-hop headers should survive
	if receivedHeaders.Get("Content-Type") != "application/json" {
		t.Error("Content-Type should not be stripped")
	}
}

// --- isLocalhost bypass attempts ---

func TestSecurityIsLocalhostFullRange(t *testing.T) {
	tests := []struct {
		host     string
		expected bool
	}{
		{"127.0.0.1", true},
		{"127.0.0.2", true},
		{"127.255.255.255", true},
		{"127.0.0.1:9119", true},
		{"localhost", true},
		{"localhost:9119", true},
		{"localhost.localdomain", true},
		{"::1", true},
		{"[::1]:9119", true},
		{"0.0.0.0", false}, // 0.0.0.0 is not loopback per RFC
		{"10.0.0.1", false},
		{"192.168.1.1", false},
		{"api.anthropic.com", false},
		{"evil.com", false},
		{"", false},
	}
	for _, tt := range tests {
		got := isLocalhost(tt.host)
		if got != tt.expected {
			t.Errorf("isLocalhost(%q) = %v, want %v", tt.host, got, tt.expected)
		}
	}
}

// --- CONNECT tunnel security ---

func TestSecurityConnectBlockedDomain(t *testing.T) {
	proxy := newTestProxy(nil, []string{"api.anthropic.com"})

	req := httptest.NewRequest("CONNECT", "evil.com:443", nil)
	req.Host = "evil.com:443"
	w := httptest.NewRecorder()
	proxy.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("CONNECT to blocked domain should return 403, got %d", w.Code)
	}
}

func TestSecurityConnectNonStandardPort(t *testing.T) {
	proxy := newTestProxy(nil, []string{"api.anthropic.com"})

	// Port 80 on allowed domain -- should be allowed (port is stripped by allowlist)
	req := httptest.NewRequest("CONNECT", "api.anthropic.com:80", nil)
	req.Host = "api.anthropic.com:80"
	w := httptest.NewRecorder()
	proxy.ServeHTTP(w, req)

	// It won't actually connect (no server), but should not be 403
	if w.Code == http.StatusForbidden {
		t.Error("CONNECT to allowed domain on non-standard port should not be blocked")
	}
}

// --- Concurrent access safety ---

func TestSecurityConcurrentCredentialRotation(t *testing.T) {
	cs := NewCredStore()
	cs.Load([]Credential{
		{ID: "c1", Provider: ProviderAnthropic, Token: "sk-ant-1"},
		{ID: "c2", Provider: ProviderAnthropic, Token: "sk-ant-2"},
	})

	var wg sync.WaitGroup
	// Readers: select credentials
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				cred := cs.Select(ProviderAnthropic)
				if cred == nil {
					// May happen during Load, that's OK
					continue
				}
			}
		}()
	}
	// Writer: replace credentials mid-flight
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			cs.Load([]Credential{
				{ID: "new-1", Provider: ProviderAnthropic, Token: "sk-ant-new-1"},
				{ID: "new-2", Provider: ProviderAnthropic, Token: "sk-ant-new-2"},
			})
		}(i)
	}
	wg.Wait()
	// No panic or race = pass (run with -race flag)
}

func TestSecurityConcurrentAllowlistModification(t *testing.T) {
	al := NewDomainAllowlist(DefaultAllowedDomains)

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			al.IsAllowed("api.anthropic.com")
			al.IsAllowed("evil.com")
		}()
	}
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			al.Add("custom.api.com")
		}(i)
	}
	wg.Wait()
}

// --- Google API key in URL ---

func TestSecurityGoogleAPIKeyNotInErrorResponse(t *testing.T) {
	creds := []Credential{
		{ID: "c1", Provider: ProviderGoogle, Token: "AIzaSyTestSecretKey123456789012345"},
	}
	cs := NewCredStore()
	cs.Load(creds)

	proxy := NewProxy(ProxyConfig{
		CredStore: cs,
		Allowlist: NewDomainAllowlist(DefaultAllowedDomains),
		Scrubber:  scrubber.New(),
		Logger:    silentLogger,
	})
	proxy.transport = &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return nil, &net.OpError{Op: "dial", Err: &net.DNSError{Err: "no such host"}}
		},
	}

	req := httptest.NewRequest("POST", "http://generativelanguage.googleapis.com/v1/models/gemini-pro:generateContent", nil)
	req.Host = "generativelanguage.googleapis.com"
	w := httptest.NewRecorder()
	proxy.ServeHTTP(w, req)

	body := w.Body.String()
	if strings.Contains(body, "AIzaSy") {
		t.Error("Google API key leaked in error response")
	}
}

// --- Path traversal ---

func TestSecurityPathTraversalInURL(t *testing.T) {
	// A forward proxy passes the URL as-is to the upstream. Path traversal
	// is the upstream's responsibility. The proxy's job is domain allowlist
	// enforcement. Verify that path traversal doesn't change the HOST used
	// for allowlist checking (the real security concern for a proxy).
	proxy := newTestProxy(nil, []string{"api.anthropic.com"})

	// Traversal in the path must NOT trick the proxy into hitting a different host
	req := httptest.NewRequest("GET", "http://api.anthropic.com/v1/../../../etc/passwd", nil)
	req.Host = "api.anthropic.com"
	w := httptest.NewRecorder()
	proxy.ServeHTTP(w, req)

	// Should pass allowlist (host is correct) but fail due to no credentials (503)
	// The key assertion: it's NOT 403 (blocked domain), meaning path traversal
	// did not change the host used for allowlist checking
	if w.Code == http.StatusForbidden {
		t.Error("path traversal should not change allowlist host check")
	}
}

// --- Health endpoint info disclosure ---

func TestSecurityHealthEndpointMinimalInfo(t *testing.T) {
	creds := []Credential{
		{ID: "c1", Provider: ProviderAnthropic, Token: "sk-ant-secret-key-12345"},
	}
	proxy := newTestProxy(creds, append(DefaultAllowedDomains, "localhost"))

	req := httptest.NewRequest("GET", "http://localhost:9119/health", nil)
	req.Host = "localhost:9119"
	w := httptest.NewRecorder()
	proxy.ServeHTTP(w, req)

	body := w.Body.String()
	// Must not contain credential values
	if strings.Contains(body, "sk-ant") {
		t.Error("health endpoint leaks credential values")
	}
	// Must not contain credential IDs
	if strings.Contains(body, "c1") {
		t.Error("health endpoint leaks credential IDs")
	}
}

// --- Localhost not found endpoint ---

func TestSecurityLocalhostUnknownPath(t *testing.T) {
	proxy := newTestProxy(nil, append(DefaultAllowedDomains, "localhost"))

	req := httptest.NewRequest("GET", "http://localhost:9119/admin/secrets", nil)
	req.Host = "localhost:9119"
	w := httptest.NewRecorder()
	proxy.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("unknown localhost path should return 404, got %d", w.Code)
	}
}

// --- Body size limit ---

func TestSecurityRequestBodySizeLimit(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if len(body) > maxRequestBodyBytes {
			t.Errorf("upstream received %d bytes, expected <= %d", len(body), maxRequestBodyBytes)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()
	upstreamHost := strings.TrimPrefix(upstream.URL, "http://")

	creds := []Credential{{ID: "c1", Provider: ProviderAnthropic, Token: "sk-ant-test"}}
	cs := NewCredStore()
	cs.Load(creds)

	proxy := NewProxy(ProxyConfig{
		CredStore: cs,
		Allowlist: NewDomainAllowlist([]string{"api.anthropic.com"}),
		Scrubber:  scrubber.New(),
		Logger:    silentLogger,
	})
	proxy.transport = &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if strings.HasPrefix(addr, "api.anthropic.com") {
				addr = upstreamHost
			}
			return net.Dial(network, addr)
		},
	}

	// Send a body larger than the limit
	bigBody := strings.NewReader(strings.Repeat("X", maxRequestBodyBytes+1000))
	req := httptest.NewRequest("POST", "http://api.anthropic.com/v1/messages", bigBody)
	req.Host = "api.anthropic.com"
	w := httptest.NewRecorder()
	proxy.ServeHTTP(w, req)

	// Should either truncate or return an error -- must not crash or OOM
	// The MaxBytesReader will return an error when the body exceeds the limit
}
