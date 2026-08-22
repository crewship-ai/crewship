package orchestrator

import (
	"slices"
	"strings"
	"testing"
)

func TestCodexAdapter_RoutesCredentialProviderThroughSidecar(t *testing.T) {
	req := AgentRunRequest{
		CLIAdapter:    "CODEX_CLI",
		LLMProvider:   "OPENROUTER",
		LLMModel:      "openrouter/anthropic/claude-sonnet-4",
		UserMessage:   "hello",
		sidecarActive: true,
		Credentials: []Credential{{
			Provider:   "OPENROUTER",
			EnvVarName: "OPENROUTER_API_KEY",
			PlainValue: "sk-or-v1-secret",
		}},
	}

	cmd := (codexAdapter{}).BuildCommand(req)
	wantArgs := []string{
		`model_provider="crewship"`,
		`model_providers.crewship.name="OpenRouter"`,
		`model_providers.crewship.base_url="http://127.0.0.1:9119/llm/openrouter"`,
		`model_providers.crewship.env_key="OPENAI_API_KEY"`,
		`model_providers.crewship.wire_api="responses"`,
	}
	for _, want := range wantArgs {
		if !slices.Contains(cmd, want) {
			t.Errorf("missing Codex provider override %q in %v", want, cmd)
		}
	}
	modelAt := slices.Index(cmd, "--model")
	if modelAt < 0 || modelAt+1 >= len(cmd) || cmd[modelAt+1] != "anthropic/claude-sonnet-4" {
		t.Errorf("Codex must receive the provider-local model id, got %v", cmd)
	}
}

func TestCodexAdapter_DoesNotRouteWithoutSidecarOrCredential(t *testing.T) {
	base := AgentRunRequest{
		CLIAdapter:  "CODEX_CLI",
		LLMProvider: "OPENROUTER",
		LLMModel:    "openrouter/openai/gpt-5",
		UserMessage: "hello",
	}

	for _, req := range []AgentRunRequest{
		base,
		func() AgentRunRequest {
			r := base
			r.sidecarActive = true
			return r
		}(),
	} {
		cmd := (codexAdapter{}).BuildCommand(req)
		if strings.Contains(strings.Join(cmd, " "), "model_provider") {
			t.Errorf("speculative Codex route emitted without both sidecar and credential: %v", cmd)
		}
		modelAt := slices.Index(cmd, "--model")
		if modelAt < 0 || cmd[modelAt+1] != base.LLMModel {
			t.Errorf("unrouted Codex model changed: %v", cmd)
		}
	}
}

func TestBuildEnvVarsSidecar_CodexCustomProviderKeepsRealKeyOut(t *testing.T) {
	req := AgentRunRequest{
		CLIAdapter:  "CODEX_CLI",
		LLMProvider: "OPENROUTER",
		LLMModel:    "openrouter/openai/gpt-5",
		Credentials: []Credential{{
			Provider:   "OPENROUTER",
			EnvVarName: "OPENROUTER_API_KEY",
			PlainValue: "sk-or-v1-secret",
		}},
	}

	env := BuildEnvVarsSidecar(req, true)
	joined := strings.Join(env, "\n")
	if strings.Contains(joined, "sk-or-v1-secret") {
		t.Fatalf("real OpenRouter key entered Codex environment:\n%s", joined)
	}
	if !strings.Contains(joined, "OPENAI_API_KEY=sk-dummy-crewship-sidecar") {
		t.Fatalf("Codex disposable key missing:\n%s", joined)
	}
}
