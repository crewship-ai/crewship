package sidecar

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/bedrock"
)

func TestBedrockAccountRouteNeverSubstitutesPayerOrDestination(t *testing.T) {
	a := bedrock.Account{AuthMethod: "bedrock_api_key", APIKey: "secret-account-one", Region: "eu-central-1", Models: []string{"eu.anthropic.claude-opus-5-5"}, ExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339), EUOnly: true}
	raw, _ := json.Marshal(a)
	cs := NewCredStore()
	cs.Load([]Credential{{ID: "one", Provider: "AWS_BEDROCK", Token: string(raw)}, {ID: "peer", Provider: "AWS_BEDROCK", Token: string(raw), AgentIDs: []string{"other-agent"}}})
	calls := 0
	var usage LLMUsage
	p := NewProxy(ProxyConfig{CredStore: cs, Logger: covLogger(), FreeMode: true, BillingMode: "flat_rate", OnLLMCall: func(u LLMUsage, _ QuotaInfo, mode, plan string) {
		if mode != "metered" || plan != "" {
			t.Fatal("Bedrock inherited another account billing mode")
		}
		usage = u
	}})
	p.transport = roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Host != "bedrock-runtime.eu-central-1.amazonaws.com" || r.Header.Get("Authorization") != "Bearer secret-account-one" || r.Header.Get(bedrock.AccountHeader) != "" {
			t.Fatal("wrong destination, authentication or leaked routing header")
		}
		return jsonUpstreamResponse(200, "application/json", `{"output":{"message":{"role":"assistant"}},"usage":{"inputTokens":10,"outputTokens":1}}`, nil), nil
	})
	for _, tc := range []struct {
		id, model, action string
		status            int
	}{{"one", a.Models[0], "converse", 200}, {"", a.Models[0], "converse", 503}, {"missing", a.Models[0], "converse", 503}, {"peer", a.Models[0], "converse", 503}, {"one", "global.anthropic.haiku", "converse", 403}, {"one", a.Models[0], "invoke", 403}} {
		r := httptest.NewRequest("POST", "http://localhost/llm/amazon-bedrock/model/"+tc.model+"/"+tc.action, strings.NewReader(`{}`))
		r.RemoteAddr = "127.0.0.1:1234"
		r.Header.Set(bedrock.AccountHeader, tc.id)
		w := httptest.NewRecorder()
		p.ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatalf("account=%s model=%s status=%d: %s", tc.id, tc.model, w.Code, w.Body.String())
		}
	}
	if calls != 1 || usage.CredentialID != "one" || usage.Provider != "amazon-bedrock" || usage.Model != a.Models[0] || usage.InputTokens != 10 {
		t.Fatalf("invalid billing attribution: calls=%d usage=%+v", calls, usage)
	}
	cs.Remove("one")
	r := httptest.NewRequest("POST", "http://localhost/llm/amazon-bedrock/model/"+a.Models[0]+"/converse", strings.NewReader(`{}`))
	r.Header.Set(bedrock.AccountHeader, "one")
	r.RemoteAddr = "127.0.0.1:1234"
	w := httptest.NewRecorder()
	p.ServeHTTP(w, r)
	if w.Code != 503 || calls != 1 {
		t.Fatal("revoked payer fell back to another account")
	}
}
