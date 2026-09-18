package sidecar

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/llmroute"
)

// The Coding Plan is an openai-compatible product: every model speaks the
// chat/responses wire format, and the only auth header to replace is the
// bearer. A credential from any other product must not serve its route.
func TestZAICodingPlanProxy(t *testing.T) {
	for _, endpoint := range []string{"/chat/completions", "/responses"} {
		t.Run(endpoint, func(t *testing.T) {
			var upstream *http.Request
			proxy := newCapturingProxy(t, []Credential{{ID: "coding-plan-key", Provider: ProviderType("ZAI_CODING_PLAN"), Token: "private-coding-key"}}, &upstream)
			req := httptest.NewRequest("POST", "http://127.0.0.1:9119/llm/zai-coding-plan"+endpoint, strings.NewReader(`{}`))
			req.RemoteAddr = "127.0.0.1:12345"
			req.Header.Set("Authorization", "Bearer dummy")
			req.Header.Set("x-opencode-session", "session-123")
			req.Header.Set("User-Agent", "opencode/test")
			w := httptest.NewRecorder()
			proxy.ServeHTTP(w, req)
			if w.Code != http.StatusOK || upstream == nil {
				t.Fatalf("status %d: %s", w.Code, w.Body.String())
			}
			if upstream.URL.Host != "api.z.ai" || upstream.URL.Path != "/api/coding/paas/v4"+endpoint {
				t.Fatalf("wrong upstream: %s", upstream.URL)
			}
			if upstream.Header.Get("Authorization") != "Bearer private-coding-key" {
				t.Fatal("bearer was not replaced")
			}
			if upstream.Header.Get("x-opencode-session") != "session-123" || upstream.Header.Get("User-Agent") != "opencode/test" {
				t.Fatal("client/session identity lost")
			}
			spec, _ := llmroute.Lookup("ZAI_CODING_PLAN")
			if got := spec.ResponseCodec(req.URL.Path); got != "openai" {
				t.Fatalf("codec %s", got)
			}
		})
	}
	var upstream *http.Request
	proxy := newCapturingProxy(t, []Credential{{ID: "metered", Provider: ProviderType("OPENCODE"), Token: "other-product-key"}}, &upstream)
	req := httptest.NewRequest("POST", "http://127.0.0.1:9119/llm/zai-coding-plan/chat/completions", strings.NewReader(`{}`))
	req.RemoteAddr = "127.0.0.1:12345"
	w := httptest.NewRecorder()
	proxy.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable || upstream != nil {
		t.Fatal("wrong-product key was accepted or forwarded")
	}
}
