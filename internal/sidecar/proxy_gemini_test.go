package sidecar

// #1030 (Gemini leg): the sidecar reverse-proxy, wired for api.anthropic.com
// and api.openai.com, now also injects keys for Google's Gemini API. The
// Gemini CLI points GOOGLE_GEMINI_BASE_URL at http://127.0.0.1:9119/gemini
// (the same path-suffixed base-URL shape the @google/genai SDK already
// supports for gateways); the sidecar strips the /gemini routing prefix,
// swaps the dummy key for the real one from the CredStore, and forwards to
// generativelanguage.googleapis.com — so the Google key lives only in the
// sidecar (UID 1002) heap, never in the agent env.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newCapturingProxy is defined in proxy_openai_test.go.

func TestGeminiReverseProxy_InjectsKeyStripsPrefixRewritesHost(t *testing.T) {
	var upstream *http.Request
	proxy := newCapturingProxy(t,
		[]Credential{{ID: "g1", Provider: ProviderGoogle, Token: "AIzaSy-real-google-key"}}, &upstream)

	req := httptest.NewRequest("POST",
		"http://127.0.0.1:9119/gemini/v1beta/models/gemini-2.5-pro:generateContent",
		strings.NewReader(`{"contents":[]}`))
	req.Host = "127.0.0.1:9119"
	// The genai SDK sends the (dummy) key in x-goog-api-key.
	req.Header.Set("x-goog-api-key", "dummy-crewship-sidecar")
	w := httptest.NewRecorder()
	proxy.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	if upstream == nil {
		t.Fatal("upstream transport never called")
	}
	if upstream.URL.Host != "generativelanguage.googleapis.com" || upstream.URL.Scheme != "https" {
		t.Errorf("upstream URL = %s, want https://generativelanguage.googleapis.com/...", upstream.URL.String())
	}
	// The /gemini routing prefix must be stripped so Google sees its real path.
	if upstream.URL.Path != "/v1beta/models/gemini-2.5-pro:generateContent" {
		t.Errorf("upstream path = %q, want /v1beta/models/gemini-2.5-pro:generateContent (prefix stripped)", upstream.URL.Path)
	}
	// The dummy header key must be replaced with the real one from the CredStore.
	if got := upstream.Header.Get("x-goog-api-key"); got != "AIzaSy-real-google-key" {
		t.Errorf("x-goog-api-key = %q, want AIzaSy-real-google-key", got)
	}
}

// A streaming path with a query string is forwarded with the query preserved
// (alt=sse must survive the prefix strip and the key injection).
func TestGeminiReverseProxy_StreamPathPreservesQuery(t *testing.T) {
	var upstream *http.Request
	proxy := newCapturingProxy(t,
		[]Credential{{ID: "g1", Provider: ProviderGoogle, Token: "AIzaSy-real"}}, &upstream)
	req := httptest.NewRequest("POST",
		"http://127.0.0.1:9119/gemini/v1beta/models/gemini-2.5-flash:streamGenerateContent?alt=sse",
		strings.NewReader(`{}`))
	req.Host = "127.0.0.1:9119"
	w := httptest.NewRecorder()
	proxy.ServeHTTP(w, req)
	if upstream == nil {
		t.Fatal("upstream transport never called")
	}
	if upstream.URL.Path != "/v1beta/models/gemini-2.5-flash:streamGenerateContent" {
		t.Errorf("upstream path = %q, want prefix-stripped streamGenerateContent path", upstream.URL.Path)
	}
	if got := upstream.URL.Query().Get("alt"); got != "sse" {
		t.Errorf("alt query param = %q, want sse (query must be preserved)", got)
	}
	if got := upstream.Header.Get("x-goog-api-key"); got != "AIzaSy-real" {
		t.Errorf("x-goog-api-key = %q, want AIzaSy-real", got)
	}
}

// No Google credential in the store: the request is still forwarded (the
// caller-supplied key header is left intact) — mirrors the Anthropic OAuth
// passthrough and must not 500 or panic.
func TestGeminiReverseProxy_NoCredPassesThrough(t *testing.T) {
	var upstream *http.Request
	proxy := newCapturingProxy(t, nil, &upstream)
	req := httptest.NewRequest("POST",
		"http://127.0.0.1:9119/gemini/v1beta/models/gemini-2.5-pro:generateContent",
		strings.NewReader(`{}`))
	req.Host = "127.0.0.1:9119"
	req.Header.Set("x-goog-api-key", "caller-supplied")
	w := httptest.NewRecorder()
	proxy.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if upstream == nil || upstream.Header.Get("x-goog-api-key") != "caller-supplied" {
		t.Errorf("passthrough x-goog-api-key not preserved: %v", upstream)
	}
}

// Routing isolation across all three reverse-proxied providers on the shared
// port: /v1/ → Anthropic, /openai/ → OpenAI, /gemini/ → Google. No cross-
// routing, and each provider gets its own auth slot injected.
func TestHandleLocal_RoutesGeminiDistinctFromAnthropicAndOpenAI(t *testing.T) {
	cases := []struct {
		path        string
		wantHost    string
		wantPath    string
		provider    ProviderType
		token       string
		wantAuthHdr string // Authorization value, "" = don't check
		wantXAPIKey string // x-api-key value, "" = don't check
		wantGoogHdr string // x-goog-api-key value, "" = don't check
	}{
		{"/v1/messages", "api.anthropic.com", "/v1/messages", ProviderAnthropic, "sk-ant-real", "", "sk-ant-real", ""},
		{"/openai/v1/responses", "api.openai.com", "/v1/responses", ProviderOpenAI, "sk-oai-real", "Bearer sk-oai-real", "", ""},
		{"/gemini/v1beta/models/g:generateContent", "generativelanguage.googleapis.com", "/v1beta/models/g:generateContent", ProviderGoogle, "AIzaSy-goo-real", "", "", "AIzaSy-goo-real"},
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
			if tc.wantGoogHdr != "" && upstream.Header.Get("x-goog-api-key") != tc.wantGoogHdr {
				t.Errorf("x-goog-api-key = %q, want %q", upstream.Header.Get("x-goog-api-key"), tc.wantGoogHdr)
			}
		})
	}
}

// injectCredential for Google must set BOTH auth slots the Gemini API accepts:
// the x-goog-api-key header (what the genai SDK sends, so the dummy the agent
// carries gets overwritten) and the ?key= query param (the pre-existing
// forward-proxy behavior, kept for clients that authenticate that way).
func TestInjectCredentialGoogle_SetsHeaderAndQueryParam(t *testing.T) {
	req := httptest.NewRequest("POST",
		"https://generativelanguage.googleapis.com/v1beta/models/g:generateContent", nil)
	req.Header.Set("x-goog-api-key", "dummy-crewship-sidecar")
	injectCredential(req, ProviderGoogle, "AIzaSy-real-key")
	if got := req.Header.Get("x-goog-api-key"); got != "AIzaSy-real-key" {
		t.Errorf("x-goog-api-key = %q, want the real key (dummy overwritten)", got)
	}
	if got := req.URL.Query().Get("key"); got != "AIzaSy-real-key" {
		t.Errorf("key query param = %q, want the real key", got)
	}
}
