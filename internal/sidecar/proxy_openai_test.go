package sidecar

// #1030: the sidecar reverse-proxy, previously wired only for
// api.anthropic.com, now also injects keys for OpenAI. Codex points
// OPENAI_BASE_URL at http://127.0.0.1:9119/openai/v1; the sidecar strips
// the /openai routing prefix, swaps the dummy key for the real one from
// the CredStore, and forwards to api.openai.com — so the OpenAI key lives
// only in the sidecar (UID 1002) heap, never in the agent env.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// captures the upstream request a fake transport saw.
func newCapturingProxy(t *testing.T, creds []Credential, capture **http.Request) *Proxy {
	t.Helper()
	cs := NewCredStore()
	cs.Load(creds)
	proxy := NewProxy(ProxyConfig{
		CredStore:   cs,
		Allowlist:   NewDomainAllowlist(nil),
		Logger:      covLogger(),
		FreeMode:    true,
		BillingMode: "metered",
	})
	proxy.transport = roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		*capture = r
		return jsonUpstreamResponse(http.StatusOK, "application/json",
			`{"model":"gpt-test","usage":{"prompt_tokens":10,"completion_tokens":5}}`, nil), nil
	})
	return proxy
}

func TestOpenAIReverseProxy_InjectsKeyStripsPrefixRewritesHost(t *testing.T) {
	var upstream *http.Request
	proxy := newCapturingProxy(t,
		[]Credential{{ID: "o1", Provider: ProviderOpenAI, Token: "sk-openai-real"}}, &upstream)

	req := httptest.NewRequest("POST", "http://127.0.0.1:9119/openai/v1/responses",
		strings.NewReader(`{"model":"gpt-test"}`))
	req.Host = "127.0.0.1:9119"
	req.Header.Set("Authorization", "Bearer sk-dummy-crewship-sidecar")
	w := httptest.NewRecorder()
	proxy.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	if upstream == nil {
		t.Fatal("upstream transport never called")
	}
	if upstream.URL.Host != "api.openai.com" || upstream.URL.Scheme != "https" {
		t.Errorf("upstream URL = %s, want https://api.openai.com/...", upstream.URL.String())
	}
	// The /openai routing prefix must be stripped so OpenAI sees its real path.
	if upstream.URL.Path != "/v1/responses" {
		t.Errorf("upstream path = %q, want /v1/responses (prefix stripped)", upstream.URL.Path)
	}
	// The dummy key must be replaced with the real one from the CredStore.
	if got := upstream.Header.Get("Authorization"); got != "Bearer sk-openai-real" {
		t.Errorf("Authorization = %q, want Bearer sk-openai-real", got)
	}
}

// A chat/completions path is forwarded just the same (path-agnostic).
func TestOpenAIReverseProxy_ChatCompletionsPath(t *testing.T) {
	var upstream *http.Request
	proxy := newCapturingProxy(t,
		[]Credential{{ID: "o1", Provider: ProviderOpenAI, Token: "sk-real"}}, &upstream)
	req := httptest.NewRequest("POST", "http://127.0.0.1:9119/openai/v1/chat/completions",
		strings.NewReader(`{}`))
	req.Host = "127.0.0.1:9119"
	w := httptest.NewRecorder()
	proxy.ServeHTTP(w, req)
	if upstream == nil || upstream.URL.Path != "/v1/chat/completions" {
		t.Fatalf("upstream path = %v, want /v1/chat/completions", upstream)
	}
}

// No OpenAI credential in the store: the request is still forwarded (the
// caller-supplied Authorization header is left intact) — mirrors the
// Anthropic OAuth passthrough and must not 500 or panic.
func TestOpenAIReverseProxy_NoCredPassesThrough(t *testing.T) {
	var upstream *http.Request
	proxy := newCapturingProxy(t, nil, &upstream)
	req := httptest.NewRequest("POST", "http://127.0.0.1:9119/openai/v1/responses",
		strings.NewReader(`{}`))
	req.Host = "127.0.0.1:9119"
	req.Header.Set("Authorization", "Bearer caller-supplied")
	w := httptest.NewRecorder()
	proxy.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if upstream == nil || upstream.Header.Get("Authorization") != "Bearer caller-supplied" {
		t.Errorf("passthrough Authorization not preserved: %v", upstream)
	}
}

// Routing isolation: an Anthropic /v1/ path must still reach api.anthropic.com
// and an OpenAI /openai/ path must reach api.openai.com — no cross-routing.
func TestHandleLocal_RoutesOpenAIAndAnthropicDistinctly(t *testing.T) {
	cases := []struct {
		path        string
		wantHost    string
		wantPath    string
		provider    ProviderType
		token       string
		wantAuthHdr string
		wantXAPIKey string
	}{
		{"/v1/messages", "api.anthropic.com", "/v1/messages", ProviderAnthropic, "sk-ant-real", "", "sk-ant-real"},
		{"/openai/v1/responses", "api.openai.com", "/v1/responses", ProviderOpenAI, "sk-oai-real", "Bearer sk-oai-real", ""},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			var upstream *http.Request
			proxy := newCapturingProxy(t,
				[]Credential{{ID: "c", Provider: tc.provider, Token: tc.token}}, &upstream)
			req := httptest.NewRequest("POST", "http://127.0.0.1:9119"+tc.path, strings.NewReader(`{}`))
			req.Host = "127.0.0.1:9119"
			w := httptest.NewRecorder()
			proxy.ServeHTTP(w, req)
			if upstream == nil {
				t.Fatalf("no upstream call for %s", tc.path)
			}
			if upstream.URL.Host != tc.wantHost || upstream.URL.Path != tc.wantPath {
				t.Errorf("routed to %s%s, want %s%s", upstream.URL.Host, upstream.URL.Path, tc.wantHost, tc.wantPath)
			}
			if tc.wantXAPIKey != "" && upstream.Header.Get("x-api-key") != tc.wantXAPIKey {
				t.Errorf("x-api-key = %q, want %q", upstream.Header.Get("x-api-key"), tc.wantXAPIKey)
			}
			if tc.wantAuthHdr != "" && upstream.Header.Get("Authorization") != tc.wantAuthHdr {
				t.Errorf("Authorization = %q, want %q", upstream.Header.Get("Authorization"), tc.wantAuthHdr)
			}
		})
	}
}
