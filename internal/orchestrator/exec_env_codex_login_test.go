package orchestrator

// #2428: a Codex ChatGPT-subscription login is an AI_CLI_TOKEN with provider
// OPENAI. It is delivered as $CODEX_HOME/auth.json — never as an env var,
// never as CLAUDE_CODE_OAUTH_TOKEN, never into the sidecar CredStore — and it
// flips the run to flat-rate billing labelled with the plan the token names.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/codexauth"
)

func codexLoginJSON(t *testing.T, plan string) string {
	t.Helper()
	claims, _ := json.Marshal(map[string]any{
		"exp": float64(1789000000),
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_plan_type":  plan,
			"chatgpt_account_id": "acct-123",
		},
	})
	access := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256"}`)) + "." +
		base64.RawURLEncoding.EncodeToString(claims) + ".sig"
	return `{"auth_mode":"chatgpt","OPENAI_API_KEY":null,"tokens":{"id_token":"id.tok.en","access_token":"` + access +
		`","refresh_token":"rt.REAL-SECRET","account_id":"acct-123"},"last_refresh":"2026-08-29T20:31:08Z"}`
}

func codexLoginReq(t *testing.T) AgentRunRequest {
	t.Helper()
	return AgentRunRequest{
		AgentID:       "a1",
		AgentSlug:     "reviewer",
		CLIAdapter:    "CODEX_CLI",
		LLMProvider:   "OPENAI",
		LLMModel:      "gpt-5.5",
		sidecarActive: true,
		Credentials: []Credential{
			{ID: "login1", EnvVarName: "OPENAI_API_KEY", PlainValue: codexLoginJSON(t, "plus"), Type: "AI_CLI_TOKEN", Provider: "OPENAI"},
		},
	}
}

func TestBuildEnvVarsSidecar_CodexLogin_IsNotAnEnvVar(t *testing.T) {
	req := codexLoginReq(t)
	env := BuildEnvVarsSidecar(req, false)
	joined := strings.Join(env, "\n")

	if strings.Contains(joined, "rt.REAL-SECRET") || strings.Contains(joined, "id.tok.en") {
		t.Fatalf("login material leaked into agent env:\n%s", joined)
	}
	if _, ok := envValue(env, "CLAUDE_CODE_OAUTH_TOKEN"); ok {
		t.Errorf("an OpenAI login must not be written to CLAUDE_CODE_OAUTH_TOKEN")
	}
	// The Anthropic branch must not fire: its side effect is dropping the
	// Anthropic reverse-proxy base URL and dummy key.
	if got, ok := envValue(env, "ANTHROPIC_BASE_URL"); !ok || got != "http://127.0.0.1:9119" {
		t.Errorf("ANTHROPIC_BASE_URL = %q (present=%v): the Anthropic OAuth branch fired for an OpenAI login", got, ok)
	}
	if got, _ := envValue(env, "OPENAI_API_KEY"); got != "sk-dummy-crewship-sidecar" {
		t.Errorf("OPENAI_API_KEY = %q, want the harmless dummy (auth.json wins over it)", got)
	}
	if _, ok := envValue(env, "CODEX_API_KEY"); ok {
		t.Errorf("CODEX_API_KEY must never be set: it overrides auth.json")
	}
	if got, ok := envValue(env, "CODEX_HOME"); !ok || got != "/crew/agents/reviewer/.codex" {
		t.Errorf("CODEX_HOME = %q (present=%v)", got, ok)
	}
	if got, _ := envValue(env, "CREWSHIP_BILLING_MODE"); got != "flat_rate" {
		t.Errorf("CREWSHIP_BILLING_MODE = %q, want flat_rate", got)
	}
	if got, _ := envValue(env, "CREWSHIP_SUBSCRIPTION_PLAN"); got != "ChatGPT Plus" {
		t.Errorf("CREWSHIP_SUBSCRIPTION_PLAN = %q, want ChatGPT Plus", got)
	}
}

// The non-sidecar path must not write the JSON blob to the env either.
func TestBuildEnvVars_CodexLogin_IsNotAnEnvVar(t *testing.T) {
	req := codexLoginReq(t)
	for _, env := range [][]string{BuildEnvVars(req, nil), BuildEnvVars(req, &req.Credentials[0])} {
		joined := strings.Join(env, "\n")
		if strings.Contains(joined, "rt.REAL-SECRET") {
			t.Fatalf("login leaked on the non-sidecar path:\n%s", joined)
		}
		if _, ok := envValue(env, "CLAUDE_CODE_OAUTH_TOKEN"); ok {
			t.Errorf("OpenAI login written to CLAUDE_CODE_OAUTH_TOKEN on the non-sidecar path")
		}
	}
}

// A subscription run is NOT proxy-routed: Codex ignores base URLs in that
// mode, and pointing it at the proxy would only make the dummy key win.
func TestCodexAdapter_LoginRunIsNotRouted(t *testing.T) {
	req := codexLoginReq(t)
	cmd := codexAdapter{}.BuildCommand(req)
	if strings.Contains(strings.Join(cmd, " "), "model_provider") {
		t.Errorf("subscription run must not carry a provider block:\n%s", strings.Join(cmd, " "))
	}
}

// The login never enters the sidecar CredStore — even under the env-var name
// an operator is likely to give it.
func TestCredTypeToProvider_CodexLoginStaysOut(t *testing.T) {
	cred := Credential{EnvVarName: "OPENAI_API_KEY", PlainValue: "{}", Type: "AI_CLI_TOKEN", Provider: "OPENAI"}
	if got := credTypeToProvider(cred); got != "" {
		t.Errorf("credTypeToProvider(login) = %q, want \"\"", got)
	}
	if sc := buildSidecarCreds([]Credential{cred}, nil); len(sc) != 0 {
		t.Errorf("login reached the sidecar boot payload: %+v", sc)
	}
}

// The Anthropic login is untouched by all of the above.
func TestBuildEnvVarsSidecar_AnthropicLogin_Unchanged(t *testing.T) {
	req := AgentRunRequest{AgentID: "a1", AgentSlug: "lead", CLIAdapter: "CLAUDE_CODE",
		Credentials: []Credential{{ID: "c1", EnvVarName: "CLAUDE_CODE_OAUTH_TOKEN", PlainValue: "sk-ant-oat01-x", Type: "AI_CLI_TOKEN", Provider: "ANTHROPIC"}}}
	env := BuildEnvVarsSidecar(req, false)
	if got, _ := envValue(env, "CLAUDE_CODE_OAUTH_TOKEN"); got != "sk-ant-oat01-x" {
		t.Errorf("CLAUDE_CODE_OAUTH_TOKEN = %q", got)
	}
	if got, _ := envValue(env, "CREWSHIP_SUBSCRIPTION_PLAN"); got != "Anthropic Max" {
		t.Errorf("CREWSHIP_SUBSCRIPTION_PLAN = %q", got)
	}
	// An untyped credential whose VALUE looks like an Anthropic token keeps
	// its shape-based classification.
	if credentialOAuthKind(Credential{PlainValue: "sk-ant-oat01-y", Type: "API_KEY"}) != oauthAnthropic {
		t.Errorf("sk-ant-oat value must classify as the Anthropic login")
	}
}

func TestAgentEnvCredentialExposures_ReportsCodexLoginAsFile(t *testing.T) {
	req := codexLoginReq(t)
	var got []CredentialEnvExposure
	for _, e := range AgentEnvCredentialExposures(req, false) {
		if e.Type == "AI_CLI_TOKEN" {
			got = append(got, e)
		}
	}
	if len(got) != 1 || got[0].EnvVarName != codexauth.FileRel || got[0].Actionable {
		t.Errorf("exposures = %+v, want one informational entry for %s", got, codexauth.FileRel)
	}
}

// scriptsOf flattens what the countingContainer stub (preflight_exec_count_test.go)
// was asked to run.
func scriptsOf(c *countingContainer) []string {
	var out []string
	for _, e := range c.execs {
		out = append(out, strings.Join(e.cfg.Cmd, " "))
	}
	return out
}

func TestSyncCodexAuthFile_WritesRenderedFileWithoutRefreshToken(t *testing.T) {
	req := codexLoginReq(t)
	rc := &countingContainer{}
	if err := syncLoginFile(context.Background(), rc, "ctr", req, slog.Default()); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if len(scriptsOf(rc)) != 1 {
		t.Fatalf("want one exec, got %d: %v", len(scriptsOf(rc)), scriptsOf(rc))
	}
	script := scriptsOf(rc)[0]
	if !strings.Contains(script, ".codex/auth.json") || !strings.Contains(script, "chmod 600") {
		t.Errorf("script does not write .codex/auth.json 0600: %s", script)
	}
	// The file travels base64-encoded; decode what was written and check it.
	fields := strings.Fields(script)
	var body []byte
	for i, f := range fields {
		if f == "echo" && i+1 < len(fields) {
			if b, err := base64.StdEncoding.DecodeString(fields[i+1]); err == nil && strings.Contains(string(b), "access_token") {
				body = b
			}
		}
	}
	if body == nil {
		t.Fatalf("could not find the rendered file in: %s", script)
	}
	if strings.Contains(string(body), "rt.REAL-SECRET") {
		t.Fatalf("delivered auth.json carries the real refresh token:\n%s", body)
	}
	if !strings.Contains(string(body), codexauth.PlaceholderRefreshToken) || !strings.Contains(string(body), `"account_id": "acct-123"`) {
		t.Errorf("delivered auth.json is not the rendered shape:\n%s", body)
	}
}

// No login on the run → the file from a previous assignment is removed.
func TestSyncCodexAuthFile_RemovesStaleFile(t *testing.T) {
	req := codexLoginReq(t)
	req.Credentials = nil
	rc := &countingContainer{}
	if err := syncLoginFile(context.Background(), rc, "ctr", req, slog.Default()); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if len(scriptsOf(rc)) != 1 || !strings.Contains(scriptsOf(rc)[0], "rm -f") || !strings.Contains(scriptsOf(rc)[0], ".codex/auth.json") {
		t.Errorf("want an rm of .codex/auth.json, got %v", scriptsOf(rc))
	}
}

// A login the container could never use must stop the run, not start it on
// the dummy key.
func TestSyncCodexAuthFile_RejectsUnparseableLogin(t *testing.T) {
	req := codexLoginReq(t)
	req.Credentials[0].PlainValue = "sk-not-a-login"
	rc := &countingContainer{}
	if err := syncLoginFile(context.Background(), rc, "ctr", req, slog.Default()); err == nil {
		t.Fatal("expected an error for a value that is not auth.json")
	}
	if len(scriptsOf(rc)) != 0 {
		t.Errorf("nothing should have been written: %v", scriptsOf(rc))
	}
}
