package orchestrator

// #1030: with the sidecar OpenAI reverse-proxy wired, a CODEX_CLI agent
// routes OpenAI traffic through the sidecar (OPENAI_BASE_URL) and the real
// OpenAI key stays in the sidecar CredStore — never the agent env. The dummy
// key remains so Codex has a syntactically-valid key to send.

import (
	"strings"
	"testing"
)

// envValue is defined in exec_env_localmodel_test.go.

func TestBuildEnvVarsSidecar_Codex_RoutesOpenAIThroughSidecar(t *testing.T) {
	req := AgentRunRequest{
		AgentID:    "a1",
		CLIAdapter: "CODEX_CLI",
		Credentials: []Credential{
			{EnvVarName: "OPENAI_API_KEY", PlainValue: "sk-openai-REAL", Type: "API_KEY"},
		},
	}
	env := BuildEnvVarsSidecar(req, false)

	// OpenAI base URL points Codex at the sidecar's /openai reverse-proxy prefix.
	if got, ok := envValue(env, "OPENAI_BASE_URL"); !ok || got != "http://127.0.0.1:9119/openai/v1" {
		t.Errorf("OPENAI_BASE_URL = %q (present=%v), want http://127.0.0.1:9119/openai/v1", got, ok)
	}
	// The dummy key stays; the REAL key must NOT be in the env anywhere.
	if got, _ := envValue(env, "OPENAI_API_KEY"); got != "sk-dummy-crewship-sidecar" {
		t.Errorf("OPENAI_API_KEY = %q, want the dummy (real key must not leak to env)", got)
	}
	for _, e := range env {
		if strings.Contains(e, "sk-openai-REAL") {
			t.Fatalf("real OpenAI key leaked into agent env: %q", e)
		}
	}
}

// The OpenAI reverse-proxy wiring is scoped to Codex. A CLAUDE_CODE agent
// must NOT get OPENAI_BASE_URL — its OpenAI traffic (if any, e.g. a
// cross-adapter key) is out of scope for this PR, and forcing a base URL
// could break unrelated tooling.
func TestBuildEnvVarsSidecar_ClaudeCode_NoOpenAIBaseURL(t *testing.T) {
	req := AgentRunRequest{AgentID: "a1", CLIAdapter: "CLAUDE_CODE"}
	env := BuildEnvVarsSidecar(req, false)
	if _, ok := envValue(env, "OPENAI_BASE_URL"); ok {
		t.Errorf("OPENAI_BASE_URL must not be set for CLAUDE_CODE")
	}
	// Anthropic reverse-proxy base URL is unchanged.
	if got, ok := envValue(env, "ANTHROPIC_BASE_URL"); !ok || got != "http://127.0.0.1:9119" {
		t.Errorf("ANTHROPIC_BASE_URL = %q (present=%v), want unchanged", got, ok)
	}
}

// OpenCode stays on the env-var BYOK model (multi-provider); it must NOT be
// force-routed through the OpenAI reverse-proxy by this PR.
func TestBuildEnvVarsSidecar_OpenCode_NoOpenAIBaseURL(t *testing.T) {
	req := AgentRunRequest{
		AgentID:    "a1",
		CLIAdapter: "OPENCODE",
		Credentials: []Credential{
			{EnvVarName: "OPENAI_API_KEY", PlainValue: "sk-oc-real", Type: "API_KEY"},
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
}
