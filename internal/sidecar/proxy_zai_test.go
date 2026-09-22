package sidecar

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/llmroute"
)

// The Coding Plan is an openai-compatible product: the SDK OpenCode drives it
// with (@ai-sdk/openai-compatible) speaks chat completions on the wire, and
// the only auth header to replace is the bearer. The Responses API is NOT
// exercised here — nothing claims the coding endpoint implements it — and a
// test that forwarded /responses would only prove the proxy copies paths.
func TestZAICodingPlanProxyChatCompletions(t *testing.T) {
	sse := `data: {"id":"chatcmpl-glm","object":"chat.completion.chunk","model":"glm-5.3","choices":[{"index":0,"delta":{"content":"ahoj"}}]}` + "\n\n" +
		`data: {"id":"chatcmpl-glm","object":"chat.completion.chunk","model":"glm-5.3","choices":[],"usage":{"prompt_tokens":9,"completion_tokens":2}}` + "\n\n" +
		"data: [DONE]\n\n"
	for _, tc := range []struct {
		name, contentType, body string
	}{
		{"json", "application/json", `{"id":"chatcmpl-glm","model":"glm-5.3","usage":{"prompt_tokens":9,"completion_tokens":2}}`},
		{"stream", "text/event-stream", sse},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var upstream *http.Request
			var usage LLMUsage
			cs := NewCredStore()
			cs.Load([]Credential{{ID: "coding-plan-key", Provider: ProviderType("ZAI_CODING_PLAN"), Token: "private-coding-key"}})
			proxy := NewProxy(ProxyConfig{
				CredStore: cs, Allowlist: NewDomainAllowlist(nil), Logger: covLogger(), FreeMode: true, BillingMode: "metered",
				OnLLMCall: func(u LLMUsage, _ QuotaInfo, _, _ string) { usage = u },
			})
			proxy.transport = roundTripperFunc(func(r *http.Request) (*http.Response, error) {
				upstream = r
				return jsonUpstreamResponse(http.StatusOK, tc.contentType, tc.body, nil), nil
			})
			req := httptest.NewRequest("POST", "http://127.0.0.1:9119/llm/zai-coding-plan/chat/completions", strings.NewReader(`{"model":"glm-5.3","stream":`+boolJSON(tc.name == "stream")+`}`))
			req.RemoteAddr = "127.0.0.1:12345"
			req.Header.Set("Authorization", "Bearer dummy")
			req.Header.Set("x-opencode-session", "session-123")
			req.Header.Set("User-Agent", "opencode/test")
			w := httptest.NewRecorder()
			proxy.ServeHTTP(w, req)
			if w.Code != http.StatusOK || upstream == nil {
				t.Fatalf("status %d: %s", w.Code, w.Body.String())
			}
			if upstream.URL.Host != "api.z.ai" || upstream.URL.Path != "/api/coding/paas/v4/chat/completions" {
				t.Fatalf("wrong upstream: %s", upstream.URL)
			}
			if upstream.Header.Get("Authorization") != "Bearer private-coding-key" {
				t.Fatal("bearer was not replaced")
			}
			if upstream.Header.Get("x-opencode-session") != "session-123" || upstream.Header.Get("User-Agent") != "opencode/test" {
				t.Fatal("client/session identity lost")
			}
			if w.Body.String() != tc.body {
				t.Fatalf("body not passed through verbatim: %q", w.Body.String())
			}
			spec, _ := llmroute.Lookup("ZAI_CODING_PLAN")
			if got := spec.ResponseCodec(req.URL.Path); got != "openai" {
				t.Fatalf("codec %s", got)
			}
			if usage.Provider != "zai-coding-plan" || usage.InputTokens != 9 || usage.OutputTokens != 2 {
				t.Fatalf("usage not observed for the coding-plan ledger: %+v", usage)
			}
		})
	}
}

// A credential from any other product must not serve the Coding Plan route.
func TestZAICodingPlanProxyRejectsOtherProductKey(t *testing.T) {
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

func boolJSON(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
