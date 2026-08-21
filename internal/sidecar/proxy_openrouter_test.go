package sidecar

// The fourth provider — OpenRouter, reached at /llm/openrouter. It is the first
// provider that exists ONLY because of the descriptor table: nothing in
// proxy.go names it.
//
// Two properties here are not shared with the three grandfathered rows and are
// the whole reason the table has the fields it has:
//
//   - RequireCredential. Anthropic/OpenAI/Google forward a credential-less
//     request untouched, because that is the live OAuth path and changing it
//     would break working crews. OpenRouter has no env-carried token to fall
//     back on, so a missing credential is a 503 and the request never leaves.
//   - UpstreamBasePath. openrouter.ai serves under /api/v1, so the stripped
//     path is re-rooted rather than sent to the origin root.

import (
	"github.com/crewship-ai/crewship/internal/llmroute"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOpenRouterProxy_StripsPrefixJoinsBasePathInjectsBearer(t *testing.T) {
	var upstream *http.Request
	proxy := newCapturingProxy(t,
		[]Credential{{ID: "or1", Provider: ProviderOpenRouter, Token: "sk-or-v1-REALOPENROUTERKEY"}}, &upstream)

	req := httptest.NewRequest("POST", "http://127.0.0.1:9119/llm/openrouter/chat/completions",
		strings.NewReader(`{"model":"anthropic/claude-sonnet-4-6"}`))
	req.Host = "127.0.0.1:9119"
	req.RemoteAddr = "127.0.0.1:54321"
	w := httptest.NewRecorder()
	proxy.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	if upstream == nil {
		t.Fatal("upstream transport never called")
	}
	if upstream.URL.Scheme != "https" || upstream.URL.Host != "openrouter.ai" {
		t.Errorf("upstream = %s://%s, want https://openrouter.ai", upstream.URL.Scheme, upstream.URL.Host)
	}
	// /llm/openrouter is stripped and /api/v1 is joined ahead of the remainder.
	if upstream.URL.Path != "/api/v1/chat/completions" {
		t.Errorf("upstream path = %q, want /api/v1/chat/completions", upstream.URL.Path)
	}
	if got := upstream.Header.Get("Authorization"); got != "Bearer sk-or-v1-REALOPENROUTERKEY" {
		t.Errorf("Authorization = %q, want the CredStore token as a Bearer", got)
	}
	if upstream.Host != "openrouter.ai" {
		t.Errorf("outbound Host header = %q, want openrouter.ai", upstream.Host)
	}
}

// A credential-less OpenRouter request must 503 and NEVER be forwarded.
// Falling through the way Anthropic does would send an unauthenticated request
// to a real upstream, and the operator would see a 401 from OpenRouter rather
// than "you have not attached a credential" — which is the actual fault.
func TestOpenRouterProxy_NilCredIs503NotPassThrough(t *testing.T) {
	var upstream *http.Request
	proxy := newCapturingProxy(t, nil, &upstream)

	req := httptest.NewRequest("POST", "http://127.0.0.1:9119/llm/openrouter/chat/completions",
		strings.NewReader(`{}`))
	req.Host = "127.0.0.1:9119"
	req.RemoteAddr = "127.0.0.1:54321"
	req.Header.Set("Authorization", "Bearer agent-supplied-key")
	w := httptest.NewRecorder()
	proxy.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", w.Code)
	}
	if upstream != nil {
		t.Errorf("request was forwarded to %s%s without a credential", upstream.URL.Host, upstream.URL.Path)
	}
	if strings.Contains(w.Body.String(), "agent-supplied-key") {
		t.Errorf("503 body echoes a token: %q", w.Body.String())
	}
}

// The §1.4 asymmetry, named and pinned rather than left as an accident.
//
// openrouter.ai IS in DefaultAllowedDomains, so an OpenCode BYOK crew can dial
// it directly today with its own key in the agent env. If the descriptor
// claimed the host, handleHTTP would look for an OPENROUTER credential, find
// none, and 503 — turning a working crew off. So OpenRouter is reachable by
// path prefix only, and providerForHost must keep returning "" for it.
func TestProviderForHost_OpenRouterStillUnmapped(t *testing.T) {
	for _, host := range []string{"openrouter.ai", "openrouter.ai:443", "OpenRouter.ai"} {
		if got := providerForHost(host); got != "" {
			t.Errorf("providerForHost(%q) = %q, want \"\": mapping this host would 503 every existing BYOK crew that dials OpenRouter with its own key", host, got)
		}
		// Asserted against llmroute.MatchHost as well, because that is what
		// handleHTTP actually calls (proxy.go). providerForHost is a delegation
		// today, but a guard on the wrapper alone would stop covering the proxy
		// the moment the wrapper grew a rule of its own.
		if s, ok := llmroute.MatchHost(strings.ToLower(stripPort(host))); ok {
			t.Errorf("llmroute.MatchHost(%q) = %q, want no match: the proxy resolves the host through this, not through the wrapper", host, s.ID)
		}
	}
	// And the forward-proxy path must therefore leave such a request alone.
	var upstream *http.Request
	proxy := newCapturingProxy(t,
		[]Credential{{ID: "or1", Provider: ProviderOpenRouter, Token: "sk-or-v1-SIDECAR-HELD"}}, &upstream)
	req := httptest.NewRequest("POST", "http://openrouter.ai/api/v1/chat/completions", strings.NewReader(`{}`))
	req.Host = "openrouter.ai"
	req.Header.Set("Authorization", "Bearer sk-or-agent-owned")
	w := httptest.NewRecorder()
	proxy.ServeHTTP(w, req)

	if upstream == nil {
		t.Fatal("forward-proxied OpenRouter request was not forwarded")
	}
	if got := upstream.Header.Get("Authorization"); got != "Bearer sk-or-agent-owned" {
		t.Errorf("Authorization = %q: the sidecar rewrote a host it does not claim", got)
	}
}

// The E1 fix, at the level that was broken. copyAndObserveLLM used to hand
// parseLLMUsage an uppercase ProviderType while the parser switched on
// lowercase, so EVERY proxied call reported zero tokens: Anthropic posted $0
// ledger rows and OpenAI/Gemini posted none at all. This drives a real
// response body through ServeHTTP and asserts the counts reach the observer.
//
// FAILS ON PRE-PHASE-2 MAIN. That is the point — a fourth provider written on
// top of the old path would have inherited a billing path that never worked.
func TestReverseProxy_RecordsRealTokenCounts(t *testing.T) {
	cases := []struct {
		name       string
		path       string
		cred       Credential
		body       string
		wantLedger string
		wantModel  string
		wantIn     int64
		wantOut    int64
	}{
		{
			name: "anthropic", path: "/v1/messages",
			cred:       Credential{ID: "a1", Provider: ProviderAnthropic, Token: "sk-ant-api03-REAL"},
			body:       `{"model":"claude-sonnet-4-6","usage":{"input_tokens":700,"output_tokens":25,"cache_read_input_tokens":100}}`,
			wantLedger: "anthropic", wantModel: "claude-sonnet-4-6", wantIn: 700, wantOut: 25,
		},
		{
			name: "openai", path: "/openai/v1/chat/completions",
			cred:       Credential{ID: "o1", Provider: ProviderOpenAI, Token: "sk-proj-REAL"},
			body:       `{"model":"gpt-5.5","usage":{"prompt_tokens":300,"completion_tokens":40}}`,
			wantLedger: "openai", wantModel: "gpt-5.5", wantIn: 300, wantOut: 40,
		},
		{
			name: "google", path: "/gemini/v1beta/models/g:generateContent",
			cred:       Credential{ID: "g1", Provider: ProviderGoogle, Token: "AIzaSyREAL"},
			body:       `{"modelVersion":"gemini-2.5-pro","usageMetadata":{"promptTokenCount":210,"candidatesTokenCount":8}}`,
			wantLedger: "google", wantModel: "gemini-2.5-pro", wantIn: 210, wantOut: 8,
		},
		{
			// OpenRouter is why BodyCodec and LedgerProvider are two fields:
			// an OpenAI-shaped body billed under a different rate card.
			name: "openrouter_openai_shaped_body_own_rate_card", path: "/llm/openrouter/chat/completions",
			cred:       Credential{ID: "or1", Provider: ProviderOpenRouter, Token: "sk-or-v1-REAL"},
			body:       `{"model":"anthropic/claude-sonnet-4-6","usage":{"prompt_tokens":55,"completion_tokens":9}}`,
			wantLedger: "openrouter", wantModel: "anthropic/claude-sonnet-4-6", wantIn: 55, wantOut: 9,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cs := NewCredStore()
			cs.Load([]Credential{tc.cred})

			var got LLMUsage
			fired := false
			proxy := NewProxy(ProxyConfig{
				CredStore: cs,
				Allowlist: NewDomainAllowlist(nil),
				Logger:    covLogger(),
				FreeMode:  true,
				OnLLMCall: func(u LLMUsage, q QuotaInfo, mode, plan string) { got, fired = u, true },
			})
			proxy.transport = roundTripperFunc(func(r *http.Request) (*http.Response, error) {
				return jsonUpstreamResponse(http.StatusOK, "application/json", tc.body, nil), nil
			})

			req := httptest.NewRequest("POST", "http://127.0.0.1:9119"+tc.path, strings.NewReader(`{}`))
			req.Host = "127.0.0.1:9119"
			req.RemoteAddr = "127.0.0.1:54321"
			proxy.ServeHTTP(httptest.NewRecorder(), req)

			if !fired {
				t.Fatal("OnLLMCall never fired")
			}
			if got.Provider != tc.wantLedger {
				t.Errorf("usage.Provider = %q, want the lowercase paymaster rate-card key %q", got.Provider, tc.wantLedger)
			}
			if got.Model != tc.wantModel {
				t.Errorf("usage.Model = %q, want %q", got.Model, tc.wantModel)
			}
			if got.InputTokens != tc.wantIn || got.OutputTokens != tc.wantOut {
				t.Errorf("tokens = in:%d out:%d, want in:%d out:%d — zero means the body was never parsed",
					got.InputTokens, got.OutputTokens, tc.wantIn, tc.wantOut)
			}
		})
	}
}

// The other half of the header-only isolation: a credential whose ONLY auth
// material is a custom header must actually authenticate the outbound request.
//
// Delivering such a credential is pointless if the proxy drops its headers, and
// that is exactly what used to happen — llmroute.ApplyAuth returned early on an
// empty token. The credential would have reached the CredStore, satisfied the
// RequireCredential gate (it is non-nil), and then been forwarded upstream with
// no authentication at all: a 401 that blames the endpoint rather than the
// delivery. This drives the whole path.
func TestOpenAICompatProxy_HeaderOnlyCredentialAuthenticates(t *testing.T) {
	var upstream *http.Request
	proxy := newCapturingProxy(t, []Credential{{
		ID:       "byo-headers-only",
		Provider: ProviderOpenAICompat,
		Token:    "",
		BaseURL:  "https://gateway.acme.example/v1",
		Headers:  map[string]string{"X-Api-Key": "header-only-secret", "X-Org": "acme"},
	}}, &upstream)

	req := httptest.NewRequest("POST", "http://127.0.0.1:9119/llm/openai-compat/chat/completions",
		strings.NewReader(`{"model":"llama-3.1-70b"}`))
	req.Host = "127.0.0.1:9119"
	req.RemoteAddr = "127.0.0.1:54321"
	w := httptest.NewRecorder()
	proxy.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	if upstream == nil {
		t.Fatal("upstream transport never called")
	}
	if got := upstream.Header.Get("X-Api-Key"); got != "header-only-secret" {
		t.Errorf("X-Api-Key = %q — the credential's only auth material was dropped, so the request left unauthenticated", got)
	}
	if got := upstream.Header.Get("X-Org"); got != "acme" {
		t.Errorf("X-Org = %q, want acme", got)
	}
	// No token, so no Authorization header invented on its behalf: an empty
	// bearer would swap one broken auth for another and destroy the upstream's
	// own diagnostic.
	if got := upstream.Header.Get("Authorization"); got != "" {
		t.Errorf("Authorization = %q, want empty for a credential carrying no token", got)
	}
	if upstream.URL.Host != "gateway.acme.example" {
		t.Errorf("upstream host = %q, want the credential's own", upstream.URL.Host)
	}
}
