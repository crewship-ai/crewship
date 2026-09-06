package orchestrator

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestOpenCodeAuthFile(t *testing.T) {
	for _, sidecar := range []bool{false, true} {
		req := AgentRunRequest{CLIAdapter: "OPENCODE", LLMModel: "openrouter/openai/gpt-6-astra", sidecarActive: sidecar,
			Credentials: []Credential{
				{ID: "key", Type: "API_KEY", Provider: "OPENROUTER", EnvVarName: "OPENROUTER_API_KEY", PlainValue: "private-key"},
				{ID: "second", Type: "API_KEY", Provider: "OPENROUTER", EnvVarName: "OPENROUTER_API_KEY", PlainValue: "lower-priority-key"},
				{ID: "groq", Type: "API_KEY", Provider: "GROQ", EnvVarName: "GROQ_API_KEY", PlainValue: "groq-key"},
				{ID: "hidden", Type: "API_KEY", EnvVarName: "XAI_API_KEY", PlainValue: "handle-secret", HandleOnly: true},
				{ID: "unknown", Type: "UNREGISTERED", EnvVarName: "DEEPSEEK_API_KEY", PlainValue: "unknown-secret"},
				{ID: "subscription", Type: "AI_CLI_TOKEN", Provider: "ANTHROPIC", EnvVarName: "ANTHROPIC_API_KEY", PlainValue: "sk-ant-oat01-subscription-secret"},
			},
		}
		body, err := renderOpenCodeAuth(req)
		if err != nil {
			t.Fatal(err)
		}
		var auth map[string]struct{ Type, Key string }
		if err := json.Unmarshal(body, &auth); err != nil {
			t.Fatal(err)
		}
		want := "private-key"
		if sidecar {
			want = "dummy-crewship-sidecar"
		}
		if auth["openrouter"].Key != want || auth["openrouter"].Type != "api" || auth["groq"].Key != "groq-key" || len(auth) != 2 {
			t.Fatalf("unexpected provider set/isolation (sidecar=%v)", sidecar)
		}
		if strings.Contains(string(body), "lower-priority") || strings.Contains(string(body), "secret") {
			t.Fatal("non-deliverable material reached auth file")
		}
	}
	body, err := renderOpenCodeAuth(AgentRunRequest{})
	if err != nil || string(body) != "{}" {
		t.Fatal("empty grants must clear the file")
	}
}
