package orchestrator

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestOpenCodeGatewaysRouteWithoutExposingKeys(t *testing.T) {
	for _, tc := range []struct{ provider, native, slot string }{
		{"OPENCODE", "opencode", "OPENCODE_API_KEY"},
		{"OPENCODE_GO", "opencode-go", "OPENCODE_GO_API_KEY"},
	} {
		t.Run(tc.provider, func(t *testing.T) {
			for _, model := range []string{"gpt-5.6-luna", "minimax-m3", "gemini-3.1-pro"} {
				req := AgentRunRequest{CLIAdapter: "OPENCODE", LLMProvider: tc.provider, LLMModel: model, sidecarActive: true,
					Credentials: []Credential{{ID: "gateway-key", Type: "PROVIDER_LOGIN", Provider: tc.provider, EnvVarName: tc.slot, PlainValue: "private-gateway-key"}},
				}
				if got := qualifyOpenCodeModel(tc.provider, model); got != tc.native+"/"+model {
					t.Fatalf("wrong payer: %s", got)
				}
				raw, ok := localModelConfigEnv(req, true)
				if !ok {
					t.Fatal("gateway did not route")
				}
				if strings.Contains(raw, "private-gateway-key") || strings.Contains(raw, `"npm"`) || strings.Contains(raw, `"models"`) {
					t.Fatalf("secret exposure or native model metadata overridden: %s", raw)
				}
				if !strings.Contains(raw, "/llm/"+tc.native) {
					t.Fatal(raw)
				}
				auth, err := renderOpenCodeAuth(req)
				if err != nil {
					t.Fatal(err)
				}
				var entries map[string]struct{ Type, Key string }
				if err := json.Unmarshal(auth, &entries); err != nil {
					t.Fatal(err)
				}
				if entries[tc.native].Type != "api" || entries[tc.native].Key != routedProviderDummyKey {
					t.Fatalf("auth: %s", auth)
				}
				env := BuildEnvVarsSidecar(req, true)
				if strings.Contains(strings.Join(env, "\n"), "private-gateway-key") {
					t.Fatal("real gateway key entered agent environment")
				}
				report := ModelCredentialReadiness("OPENCODE", tc.provider, tc.native+"/"+model, req.Credentials)
				if report.State != ModelCredentialReady || report.Delivery != DeliverySidecar {
					t.Fatalf("readiness: %+v", report)
				}
				req.Credentials = nil
				if _, ok := resolveRoutedProvider(req, true); ok {
					t.Fatal("unassigned credential still routes")
				}
				auth, _ = renderOpenCodeAuth(req)
				if string(auth) != "{}" {
					t.Fatal("revoked auth retained")
				}
			}
		})
	}
}
