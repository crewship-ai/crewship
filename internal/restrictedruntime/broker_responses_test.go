//go:build linux

package restrictedruntime

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const textResponseRequest = `{"model":"fixture-model","input":"Reply OK","store":false,"stream":true}`

func responsesPlan() Plan {
	p := streamingPlan()
	g := &p.Network.Grants[0]
	g.URL = "https://api.openai.com/v1/responses"
	g.CredentialID = "model-key"
	g.Responses = &ResponsesPolicy{Model: "fixture-model", MaxOutputTokens: 128}
	p.Network.Credentials = []BrokerCredential{{ID: "model-key", Revision: "r1", Provider: "openai", Account: "account-a", Delivery: "broker-bearer-v1"}}
	return p
}

func TestResponsesPolicyAndDelegation(t *testing.T) {
	parent := responsesPlan()
	if err := parent.validate(time.Now()); err != nil {
		t.Fatal(err)
	}
	for name, edit := range map[string]func(*Plan){
		"different model": func(p *Plan) { p.Network.Grants[0].Responses.Model = "other" },
		"larger budget":   func(p *Plan) { p.Network.Grants[0].Responses.MaxOutputTokens++ },
		"remove policy":   func(p *Plan) { p.Network.Grants[0].Responses = nil },
	} {
		t.Run(name, func(t *testing.T) {
			child := responsesPlan()
			child.Expires = parent.Expires
			edit(&child)
			if Narrow(parent, child) == nil || parent.fingerprint() == child.fingerprint() {
				t.Fatal("policy change neither denied nor fenced")
			}
		})
	}
	child := responsesPlan()
	child.Expires = parent.Expires
	child.Network.Grants[0].Responses.MaxOutputTokens = 64
	if err := Narrow(parent, child); err != nil {
		t.Fatal("narrower output ceiling rejected", err)
	}
	for name, edit := range map[string]func(*Plan){
		"unbounded tokens": func(p *Plan) { p.Network.Grants[0].Responses.MaxOutputTokens = 0 },
		"excessive tokens": func(p *Plan) { p.Network.Grants[0].Responses.MaxOutputTokens = 32769 },
		"empty model":      func(p *Plan) { p.Network.Grants[0].Responses.Model = "" },
		"other endpoint":   func(p *Plan) { p.Network.Grants[0].URL = "https://api.openai.com/v1/conversations" },
		"other provider":   func(p *Plan) { p.Network.Credentials[0].Provider = "other" },
		"no credential":    func(p *Plan) { p.Network.Grants[0].CredentialID = "" },
		"not streaming":    func(p *Plan) { p.Network.Grants[0].ResponseMode = "" },
		"ambiguous alias": func(p *Plan) {
			g := p.Network.Grants[0]
			g.ID = "other"
			p.Network.Grants = append(p.Network.Grants, g)
		},
	} {
		t.Run(name, func(t *testing.T) {
			p := responsesPlan()
			edit(&p)
			if p.validate(time.Now()) == nil {
				t.Fatal("invalid provider plan accepted")
			}
		})
	}
}

func TestResponsesClosedTextSchema(t *testing.T) {
	policy := ResponsesPolicy{Model: "fixture-model", MaxOutputTokens: 128}
	for _, raw := range []string{
		textResponseRequest,
		`{"model":"fixture-model","input":[{"role":"user","content":[{"type":"input_text","text":"hello"}]},{"type":"message","role":"assistant","content":"OK"}],"instructions":"Be brief","store":false,"stream":true,"max_output_tokens":32}`,
	} {
		body, err := policy.responsesBody([]byte(raw))
		var got map[string]any
		if err != nil || json.Unmarshal(body, &got) != nil || got["store"] != false || got["stream"] != true || got["max_output_tokens"].(float64) > 128 {
			t.Fatalf("valid self-contained text rejected: %s %v", body, err)
		}
	}
	for name, field := range map[string]string{
		"previous response": `"previous_response_id":"resp_foreign"`,
		"conversation":      `"conversation":"conv_foreign"`,
		"prompt template":   `"prompt":{"id":"pmpt_foreign"}`,
		"remote tool":       `"tools":[{"type":"file_search","vector_store_ids":["vs_foreign"]}]`,
		"include":           `"include":["reasoning.encrypted_content"]`,
		"background":        `"background":true`,
		"metadata":          `"metadata":{"arbitrary":"value"}`,
		"cache key":         `"prompt_cache_key":"foreign"`,
		"duplicate store":   `"store":true`,
		"escaped duplicate": `"\u0073tore":false`,
		"case variant":      `"Store":false`,
		"output limit":      `"max_output_tokens":129`,
		"fractional limit":  `"max_output_tokens":1.5`,
		"null limit":        `"max_output_tokens":null`,
		"exponent limit":    `"max_output_tokens":1e2`,
	} {
		t.Run(name, func(t *testing.T) {
			raw := strings.TrimSuffix(textResponseRequest, "}") + "," + field + "}"
			if _, err := policy.responsesBody([]byte(raw)); err == nil {
				t.Fatal("forbidden field accepted")
			}
		})
	}
	for name, input := range map[string]string{
		"item reference":      `[{"type":"item_reference","id":"msg_foreign"}]`,
		"encrypted reasoning": `[{"type":"reasoning","encrypted_content":"foreign"}]`,
		"file":                `[{"role":"user","content":[{"type":"input_file","file_id":"file_foreign"}]}]`,
		"image":               `[{"role":"user","content":[{"type":"input_image","image_url":"https://foreign.example"}]}]`,
		"nested reference":    `[{"role":"user","content":[{"type":"input_text","text":"x","file_id":"foreign"}]}]`,
		"message id":          `[{"role":"assistant","id":"msg_foreign","content":"x"}]`,
		"function result":     `[{"type":"function_call_output","call_id":"foreign","output":"x"}]`,
		"unknown role":        `[{"role":"tool","content":"x"}]`,
		"null":                `null`,
		"deep":                strings.Repeat("[", 18) + `"x"` + strings.Repeat("]", 18),
	} {
		t.Run(name, func(t *testing.T) {
			raw := strings.Replace(textResponseRequest, `"Reply OK"`, input, 1)
			if _, err := policy.responsesBody([]byte(raw)); err == nil {
				t.Fatal("foreign resource shape accepted")
			}
		})
	}
	for _, raw := range []string{textResponseRequest + `{}`, `{`, `null`, strings.Replace(textResponseRequest, `"store":false,`, "", 1), strings.Replace(textResponseRequest, "fixture-model", "other", 1), strings.Replace(textResponseRequest, "Reply OK", "\xff", 1)} {
		if _, err := policy.responsesBody([]byte(raw)); err == nil {
			t.Fatal("malformed/missing policy accepted")
		}
	}
}

func TestResponsesBrokerRejectsBeforeDNSAndInjectsCeiling(t *testing.T) {
	p := responsesPlan()
	s, a := brokerTestSession(t, p)
	a.material = BoundSecret{ID: "model-key", Revision: "r1", Provider: "openai", Account: "account-a", Value: "synthetic-model-secret", Expires: time.Now().Add(time.Minute)}
	var calls, lookups atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var got map[string]any
		if json.NewDecoder(r.Body).Decode(&got) != nil || got["model"] != "fixture-model" || got["max_output_tokens"] != float64(128) || got["store"] != false || r.URL.Path != "/v1/responses" || r.Header.Get("Authorization") != "Bearer synthetic-model-secret" {
			t.Error("host model/account/limit binding lost")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: model-text\n\n")
	}))
	defer server.Close()
	tr := syntheticBrokerTransport(t, server)
	// httptest's certificate covers example.com; only this private fixture
	// seam replaces the production DNS destination and TLS server name.
	tr.tls.ServerName = "example.com"
	lookup := tr.lookup
	tr.lookup = func(ctx context.Context, host string) ([]net.IPAddr, error) {
		lookups.Add(1)
		return lookup(ctx, host)
	}
	for _, raw := range []string{strings.Replace(textResponseRequest, "fixture-model", "other", 1), strings.TrimSuffix(textResponseRequest, "}") + `,"previous_response_id":"foreign"}`} {
		out := s.brokerExchange(t.Context(), "token", brokerFrame{Token: "token", Operation: "echo", Body: []byte(raw)}, *tr, func(brokerFrame) error { t.Error("denial emitted stream"); return nil })
		if out.Status != 403 || calls.Load() != 0 || lookups.Load() != 0 {
			t.Fatal("forbidden provider request left host")
		}
	}
	var output strings.Builder
	out := s.brokerExchange(t.Context(), "token", brokerFrame{Token: "token", Operation: "echo", Body: []byte(textResponseRequest)}, *tr, func(f brokerFrame) error { output.Write(f.Body); return nil })
	if out.Kind != "stream_end" || calls.Load() != 1 || !strings.Contains(output.String(), "model-text") {
		t.Fatalf("positive model transport failed: %+v", out)
	}
}
