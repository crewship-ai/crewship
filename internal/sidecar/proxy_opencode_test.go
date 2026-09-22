package sidecar

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/llmroute"
)

func TestOpenCodeGatewayProxy(t *testing.T) {
	for _, tc := range []struct{ provider, route, base string }{
		{"OPENCODE", "opencode", "/zen/v1"},
		{"OPENCODE_GO", "opencode-go", "/zen/go/v1"},
	} {
		t.Run(tc.provider, func(t *testing.T) {
			for _, endpoint := range []struct{ path, codec string }{
				{"/chat/completions", "openai"}, {"/responses", "openai"}, {"/messages", "anthropic"}, {"/models/gemini-3.1-pro:streamGenerateContent", "google"},
			} {
				t.Run(endpoint.path, func(t *testing.T) {
					var upstream *http.Request
					proxy := newCapturingProxy(t, []Credential{{ID: "gateway-key", Provider: ProviderType(tc.provider), Token: "private-opencode-key"}}, &upstream)
					req := httptest.NewRequest("POST", "http://127.0.0.1:9119/llm/"+tc.route+endpoint.path, strings.NewReader(`{}`))
					req.RemoteAddr = "127.0.0.1:12345"
					req.Header.Set("Authorization", "Bearer dummy")
					req.Header.Set("x-api-key", "dummy")
					req.Header.Set("x-goog-api-key", "dummy")
					req.Header.Set("x-opencode-session", "session-123")
					req.Header.Set("User-Agent", "opencode/test")
					w := httptest.NewRecorder()
					proxy.ServeHTTP(w, req)
					if w.Code != http.StatusOK || upstream == nil {
						t.Fatalf("status %d: %s", w.Code, w.Body.String())
					}
					if upstream.URL.Host != "opencode.ai" || upstream.URL.Path != tc.base+endpoint.path {
						t.Fatalf("wrong upstream: %s", upstream.URL)
					}
					for _, header := range []string{"x-api-key", "x-goog-api-key"} {
						if upstream.Header.Get(header) != "private-opencode-key" {
							t.Errorf("%s was not replaced", header)
						}
					}
					if upstream.Header.Get("Authorization") != "Bearer private-opencode-key" {
						t.Fatal("bearer was not injected")
					}
					if upstream.Header.Get("x-opencode-session") != "session-123" || upstream.Header.Get("User-Agent") != "opencode/test" {
						t.Fatal("client/session identity lost")
					}
					spec, _ := llmroute.Lookup(tc.provider)
					if got := spec.ResponseCodec(req.URL.Path); got != endpoint.codec {
						t.Fatalf("codec %s", got)
					}
				})
			}
			var upstream *http.Request
			other := "OPENCODE"
			if tc.provider == other {
				other = "OPENCODE_GO"
			}
			proxy := newCapturingProxy(t, []Credential{{ID: "wrong-plan", Provider: ProviderType(other), Token: "other-plan-key"}}, &upstream)
			req := httptest.NewRequest("POST", "http://127.0.0.1:9119/llm/"+tc.route+"/chat/completions", strings.NewReader(`{}`))
			req.RemoteAddr = "127.0.0.1:12345"
			w := httptest.NewRecorder()
			proxy.ServeHTTP(w, req)
			if w.Code != http.StatusServiceUnavailable || upstream != nil {
				t.Fatal("wrong-plan key was accepted or forwarded")
			}
		})
	}
}
