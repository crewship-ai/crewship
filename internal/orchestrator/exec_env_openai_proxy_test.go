package orchestrator

// #1030/#2428: a CODEX_CLI agent's OpenAI traffic goes through the sidecar
// reverse-proxy and the real key stays in the sidecar CredStore — never the
// agent env. The route is a custom model_provider block in the Codex command
// (OPENAI_BASE_URL does not exist in codex-cli 0.153.2, and the built-in
// `openai` provider cannot be overridden), so the assertions here are on the
// command as much as on the env.

import (
	"slices"
	"strings"
	"testing"
)

// envValue is defined in exec_env_localmodel_test.go.

func codexAPIKeyReq() AgentRunRequest {
	return AgentRunRequest{
		AgentID:       "a1",
		AgentSlug:     "coder",
		CLIAdapter:    "CODEX_CLI",
		LLMProvider:   "OPENAI",
		LLMModel:      "gpt-5.5",
		sidecarActive: true,
		Credentials: []Credential{
			{ID: "k1", EnvVarName: "OPENAI_API_KEY", PlainValue: "sk-openai-REAL", Type: "API_KEY", Provider: "OPENAI"},
		},
	}
}

func TestBuildEnvVarsSidecar_Codex_RoutesOpenAIThroughSidecar(t *testing.T) {
	req := codexAPIKeyReq()
	env := BuildEnvVarsSidecar(req, false)

	// The variable Codex no longer reads must not be set — its presence
	// used to LOOK like routing while the request went straight upstream.
	if _, ok := envValue(env, "OPENAI_BASE_URL"); ok {
		t.Errorf("OPENAI_BASE_URL must not be set: Codex ignores it and dials api.openai.com directly")
	}
	// CODEX_API_KEY overrides auth.json; it must never be set, dummy or not.
	if _, ok := envValue(env, "CODEX_API_KEY"); ok {
		t.Errorf("CODEX_API_KEY must not be set")
	}
	// The dummy key stays (the custom provider's env_key reads it); the REAL
	// key must NOT be in the env anywhere.
	if got, _ := envValue(env, "OPENAI_API_KEY"); got != "sk-dummy-crewship-sidecar" {
		t.Errorf("OPENAI_API_KEY = %q, want the dummy (real key must not leak to env)", got)
	}
	for _, e := range env {
		if strings.Contains(e, "sk-openai-REAL") {
			t.Fatalf("real OpenAI key leaked into agent env: %q", e)
		}
	}
	if got, ok := envValue(env, "CODEX_HOME"); !ok || got != "/crew/agents/coder/.codex" {
		t.Errorf("CODEX_HOME = %q (present=%v), want /crew/agents/coder/.codex", got, ok)
	}
	if got, _ := envValue(env, "CREWSHIP_BILLING_MODE"); got != "metered" {
		t.Errorf("CREWSHIP_BILLING_MODE = %q, want metered for an API key", got)
	}
}

// The route itself: the Codex command carries a custom provider whose base
// URL is the sidecar's /openai route WITH the upstream's /v1 (the prefix is
// stripped before forwarding, so the client supplies /v1 — the same shape the
// retired OPENAI_BASE_URL had).
func TestCodexAdapter_APIKeyRunUsesSidecarProvider(t *testing.T) {
	req := codexAPIKeyReq()
	cmd := codexAdapter{}.BuildCommand(req)
	joined := strings.Join(cmd, " ")
	for _, want := range []string{
		`model_provider="crewship"`,
		`model_providers.crewship.name="OpenAI"`,
		`model_providers.crewship.base_url="http://127.0.0.1:9119/openai/v1"`,
		`model_providers.crewship.env_key="OPENAI_API_KEY"`,
		`model_providers.crewship.wire_api="responses"`,
	} {
		if !slices.Contains(cmd, want) {
			t.Errorf("codex command lacks %s:\n%s", want, joined)
		}
	}
	if at := slices.Index(cmd, "--model"); at < 0 || cmd[at+1] != "gpt-5.5" {
		t.Errorf("model must survive routing unchanged: %s", joined)
	}
}

// Without a provider anywhere on the request (older agents carry only the
// model), Codex still means OpenAI.
func TestCodexAdapter_APIKeyRunRoutesWithoutProviderField(t *testing.T) {
	req := codexAPIKeyReq()
	req.LLMProvider = ""
	cmd := codexAdapter{}.BuildCommand(req)
	if !slices.Contains(cmd, `model_providers.crewship.base_url="http://127.0.0.1:9119/openai/v1"`) {
		t.Errorf("route missing when LLMProvider is empty:\n%s", strings.Join(cmd, " "))
	}
}

// No key, no route: a run with nothing the sidecar could inject must not be
// pointed at a proxy that would answer 503.
func TestCodexAdapter_NoKeyNoRoute(t *testing.T) {
	req := codexAPIKeyReq()
	req.Credentials = nil
	cmd := codexAdapter{}.BuildCommand(req)
	if strings.Contains(strings.Join(cmd, " "), "model_provider") {
		t.Errorf("speculative route without a credential:\n%s", strings.Join(cmd, " "))
	}
	// A lapsed lease is the same as no key — the sidecar will refuse it.
	req = codexAPIKeyReq()
	req.Credentials[0].LeaseExpiresAt = "2020-01-01T00:00:00Z"
	if cmd := (codexAdapter{}).BuildCommand(req); strings.Contains(strings.Join(cmd, " "), "model_provider") {
		t.Errorf("route on a lapsed lease:\n%s", strings.Join(cmd, " "))
	}
}

// The route is Codex-only. CLAUDE_CODE and OPENCODE keep their own paths
// (Anthropic's global base URL; OpenCode's BYOK env vars).
func TestBuildEnvVarsSidecar_ClaudeCode_NoOpenAIBaseURL(t *testing.T) {
	req := AgentRunRequest{AgentID: "a1", CLIAdapter: "CLAUDE_CODE"}
	env := BuildEnvVarsSidecar(req, false)
	if _, ok := envValue(env, "OPENAI_BASE_URL"); ok {
		t.Errorf("OPENAI_BASE_URL must not be set for CLAUDE_CODE")
	}
	if _, ok := envValue(env, "CODEX_HOME"); ok {
		t.Errorf("CODEX_HOME must not be set for CLAUDE_CODE")
	}
	if got, ok := envValue(env, "ANTHROPIC_BASE_URL"); !ok || got != "http://127.0.0.1:9119" {
		t.Errorf("ANTHROPIC_BASE_URL = %q (present=%v), want unchanged", got, ok)
	}
}

func TestBuildEnvVarsSidecar_OpenCode_KeepsBYOKPath(t *testing.T) {
	req := AgentRunRequest{
		AgentID:       "a1",
		CLIAdapter:    "OPENCODE",
		LLMProvider:   "OPENAI",
		LLMModel:      "gpt-5.5",
		sidecarActive: true,
		Credentials: []Credential{
			{EnvVarName: "OPENAI_API_KEY", PlainValue: "sk-oc-real", Type: "API_KEY", Provider: "OPENAI"},
		},
	}
	env := BuildEnvVarsSidecar(req, false)
	if _, ok := envValue(env, "OPENAI_BASE_URL"); ok {
		t.Errorf("OPENAI_BASE_URL must not be set for OPENCODE (BYOK multi-provider)")
	}
	// OpenCode still gets the real key in env (documented residual — its
	// driver dials providers directly; out of scope for #1030).
	if got, _ := envValue(env, "OPENAI_API_KEY"); got != "sk-oc-real" {
		t.Errorf("OPENCODE OPENAI_API_KEY = %q, want real key (unchanged)", got)
	}
	if _, routed := resolveRoutedProvider(req, true); routed {
		t.Errorf("OPENCODE must not take the Codex default route")
	}
}
