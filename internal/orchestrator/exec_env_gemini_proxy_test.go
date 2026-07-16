package orchestrator

// #1030 (Gemini leg): with the sidecar Gemini reverse-proxy wired, a
// GEMINI_CLI agent routes its Google traffic through the sidecar
// (GOOGLE_GEMINI_BASE_URL) and the real Google key stays in the sidecar
// CredStore — never the agent env. Dummy GOOGLE_API_KEY / GEMINI_API_KEY
// values remain so the CLI has a syntactically-valid key to send.
//
// Cursor stays on the env path: cursor-agent's configuration surface
// (cursor.com/docs/cli/reference/configuration) has no endpoint / base-URL
// override, so there is no way to point it at the sidecar's reverse proxy —
// its key remains a documented residual, asserted below so a future Cursor
// endpoint override doesn't silently go unnoticed.

import (
	"strings"
	"testing"
)

// envValue is defined in exec_env_localmodel_test.go.

func TestBuildEnvVarsSidecar_Gemini_RoutesGoogleThroughSidecar(t *testing.T) {
	req := AgentRunRequest{
		AgentID:    "a1",
		CLIAdapter: "GEMINI_CLI",
		Credentials: []Credential{
			{EnvVarName: "GOOGLE_API_KEY", PlainValue: "AIzaSy-google-REAL", Type: "API_KEY"},
		},
	}
	env := BuildEnvVarsSidecar(req, false)

	// Gemini base URL points the CLI at the sidecar's /gemini reverse-proxy prefix.
	if got, ok := envValue(env, "GOOGLE_GEMINI_BASE_URL"); !ok || got != "http://127.0.0.1:9119/gemini" {
		t.Errorf("GOOGLE_GEMINI_BASE_URL = %q (present=%v), want http://127.0.0.1:9119/gemini", got, ok)
	}
	// Both key slots the CLI may read stay dummies; the REAL key must NOT be
	// in the env anywhere.
	if got, _ := envValue(env, "GOOGLE_API_KEY"); got != "dummy-crewship-sidecar" {
		t.Errorf("GOOGLE_API_KEY = %q, want the dummy (real key must not leak to env)", got)
	}
	if got, ok := envValue(env, "GEMINI_API_KEY"); !ok || got != "dummy-crewship-sidecar" {
		t.Errorf("GEMINI_API_KEY = %q (present=%v), want the dummy (gemini-cli's canonical AI Studio var)", got, ok)
	}
	for _, e := range env {
		if strings.Contains(e, "AIzaSy-google-REAL") {
			t.Fatalf("real Google key leaked into agent env: %q", e)
		}
	}
}

// A credential stored under GEMINI_API_KEY (the alternate accepted name) is
// isolated exactly the same way.
func TestBuildEnvVarsSidecar_Gemini_GeminiNamedCredAlsoIsolated(t *testing.T) {
	req := AgentRunRequest{
		AgentID:    "a1",
		CLIAdapter: "GEMINI_CLI",
		Credentials: []Credential{
			{EnvVarName: "GEMINI_API_KEY", PlainValue: "AIzaSy-gem-REAL", Type: "API_KEY"},
		},
	}
	env := BuildEnvVarsSidecar(req, false)
	for _, e := range env {
		if strings.Contains(e, "AIzaSy-gem-REAL") {
			t.Fatalf("real Gemini key leaked into agent env: %q", e)
		}
	}
	if got, ok := envValue(env, "GOOGLE_GEMINI_BASE_URL"); !ok || got != "http://127.0.0.1:9119/gemini" {
		t.Errorf("GOOGLE_GEMINI_BASE_URL = %q (present=%v), want the sidecar /gemini prefix", got, ok)
	}
}

// The Gemini reverse-proxy wiring is scoped to GEMINI_CLI. Other adapters
// must NOT get GOOGLE_GEMINI_BASE_URL — forcing a base URL could break
// unrelated tooling (and OpenCode's BYOK driver dials providers directly).
func TestBuildEnvVarsSidecar_ClaudeCode_NoGeminiBaseURL(t *testing.T) {
	req := AgentRunRequest{AgentID: "a1", CLIAdapter: "CLAUDE_CODE"}
	env := BuildEnvVarsSidecar(req, false)
	if _, ok := envValue(env, "GOOGLE_GEMINI_BASE_URL"); ok {
		t.Errorf("GOOGLE_GEMINI_BASE_URL must not be set for CLAUDE_CODE")
	}
	// Anthropic reverse-proxy base URL is unchanged.
	if got, ok := envValue(env, "ANTHROPIC_BASE_URL"); !ok || got != "http://127.0.0.1:9119" {
		t.Errorf("ANTHROPIC_BASE_URL = %q (present=%v), want unchanged", got, ok)
	}
}

// OpenCode stays on the env-var BYOK model (multi-provider); it must NOT be
// force-routed through the Gemini reverse-proxy, and it still gets the real
// key in env (documented residual — its driver dials providers directly).
func TestBuildEnvVarsSidecar_OpenCode_NoGeminiBaseURL_KeepsRealKey(t *testing.T) {
	req := AgentRunRequest{
		AgentID:    "a1",
		CLIAdapter: "OPENCODE",
		Credentials: []Credential{
			{EnvVarName: "GOOGLE_API_KEY", PlainValue: "AIzaSy-oc-real", Type: "API_KEY"},
		},
	}
	env := BuildEnvVarsSidecar(req, false)
	if _, ok := envValue(env, "GOOGLE_GEMINI_BASE_URL"); ok {
		t.Errorf("GOOGLE_GEMINI_BASE_URL must not be set for OPENCODE (BYOK multi-provider)")
	}
	if got, _ := envValue(env, "GOOGLE_API_KEY"); got != "AIzaSy-oc-real" {
		t.Errorf("OPENCODE GOOGLE_API_KEY = %q, want real key (unchanged residual)", got)
	}
	// The GOOGLE↔GEMINI mirroring for env-path adapters is unchanged.
	if got, _ := envValue(env, "GEMINI_API_KEY"); got != "AIzaSy-oc-real" {
		t.Errorf("OPENCODE GEMINI_API_KEY = %q, want mirrored real key (unchanged)", got)
	}
}

// Cursor is the documented #1030 residual: cursor-agent has no endpoint /
// base-URL override in its configuration surface, so the sidecar reverse-
// proxy cannot be interposed and the real key must stay in the env. This
// test pins that behavior on purpose — if it starts failing because someone
// wired a Cursor base URL, the residual note in docs/guides/credentials.mdx
// and issue #1030 must be updated in the same change.
func TestBuildEnvVarsSidecar_Cursor_ResidualEnvPath(t *testing.T) {
	req := AgentRunRequest{
		AgentID:    "a1",
		CLIAdapter: "CURSOR_CLI",
		Credentials: []Credential{
			{EnvVarName: "CURSOR_API_KEY", PlainValue: "cur_real-key", Type: "API_KEY"},
		},
	}
	env := BuildEnvVarsSidecar(req, false)
	if got, ok := envValue(env, "CURSOR_API_KEY"); !ok || got != "cur_real-key" {
		t.Errorf("CURSOR_API_KEY = %q (present=%v), want the real key (env path residual)", got, ok)
	}
	if _, ok := envValue(env, "GOOGLE_GEMINI_BASE_URL"); ok {
		t.Errorf("GOOGLE_GEMINI_BASE_URL must not be set for CURSOR_CLI")
	}
	if _, ok := envValue(env, "OPENAI_BASE_URL"); ok {
		t.Errorf("OPENAI_BASE_URL must not be set for CURSOR_CLI")
	}
}
