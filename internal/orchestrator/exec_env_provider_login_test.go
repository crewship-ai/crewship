package orchestrator

// #2428 (docs/prd/provider-logins.md §10.2): a PROVIDER_LOGIN credential
// arrives as an access token in PlainValue plus named parts — refresh_token
// (sealed), id_token, account_id, plan, expires_at, mode. The orchestrator
// renders what the CLI needs from those parts and delivers NONE of them as
// env vars: the refresh token in particular must never reach a container,
// under any name, on any path.

import (
	"context"
	"encoding/base64"
	"log/slog"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/codexauth"
)

// codexAccessJWT is the bare access token of codexLoginJSON.
func codexAccessJWT(t *testing.T, plan string) string {
	t.Helper()
	f, err := codexauth.Parse(codexLoginJSON(t, plan))
	if err != nil {
		t.Fatal(err)
	}
	return f.Tokens.AccessToken
}

// providerLoginParts is the delivered shape of a PROVIDER_LOGIN's parts, named
// the way credential_field_delivery.go names them (<SLOT>_<KEY>).
func providerLoginParts(slot, mode, refresh, idToken, account, plan string) []CredentialField {
	var parts []CredentialField
	add := func(key, value string, secret bool) {
		if value == "" {
			return
		}
		parts = append(parts, CredentialField{Key: key, EnvVar: slot + "_" + strings.ToUpper(key), Value: value, IsSecret: secret})
	}
	add("refresh_token", refresh, true)
	add("id_token", idToken, true)
	add("account_id", account, false)
	add("plan", plan, false)
	add("expires_at", "2026-09-16T08:40:00Z", false)
	add("mode", mode, false)
	return parts
}

func codexProviderLoginCred(t *testing.T) Credential {
	t.Helper()
	return Credential{
		ID: "pl1", EnvVarName: "OPENAI_API_KEY", PlainValue: codexAccessJWT(t, "plus"),
		Type: "PROVIDER_LOGIN", Provider: "OPENAI",
		Fields: providerLoginParts("OPENAI_API_KEY", "subscription", "rt.REAL-SECRET", "id.tok.en", "acct-123", "plus"),
	}
}

func codexProviderLoginReq(t *testing.T) AgentRunRequest {
	t.Helper()
	return AgentRunRequest{
		AgentID: "a1", AgentSlug: "reviewer",
		RunID: "run-1", CLIAdapter: "CODEX_CLI",
		LLMProvider: "OPENAI", LLMModel: "gpt-5.5", sidecarActive: true,
		Credentials: []Credential{codexProviderLoginCred(t)},
	}
}

// assertNoLoginParts fails when any part of a login — by value or by the
// derived variable name — is in the env block.
func assertNoLoginParts(t *testing.T, env []string) {
	t.Helper()
	joined := strings.Join(env, "\n")
	for _, leak := range []string{"rt.REAL-SECRET", "id.tok.en", "_REFRESH_TOKEN=", "_ID_TOKEN=", "_ACCOUNT_ID=", "_EXPIRES_AT="} {
		if strings.Contains(joined, leak) {
			t.Errorf("login part %q reached the agent env:\n%s", leak, joined)
		}
	}
	// PLAN and MODE also name runtime variables (CREWSHIP_SUBSCRIPTION_PLAN,
	// CREWSHIP_BILLING_MODE), so those two are checked by their derived
	// names under every slot the fixtures use.
	for _, slot := range []string{"OPENAI_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN", "CURSOR_API_KEY", "SLOT"} {
		for _, key := range []string{"PLAN", "MODE"} {
			if envHasName(env, slot+"_"+key) {
				t.Errorf("login part %s_%s reached the agent env:\n%s", slot, key, joined)
			}
		}
	}
}

func TestCredentialOAuthKind_ProviderLogin(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		provider string
		mode     string
		value    string
		want     oauthKind
	}{
		{"openai subscription", "OPENAI", "subscription", "eyJ.x.y", oauthOpenAI},
		{"openai default mode is subscription", "OPENAI", "", "eyJ.x.y", oauthOpenAI},
		{"openai api key", "OPENAI", "api_key", "sk-proj-x", oauthNone},
		{"anthropic subscription", "ANTHROPIC", "subscription", "sk-ant-oat01-x", oauthAnthropic},
		{"anthropic api key", "ANTHROPIC", "api_key", "sk-ant-api03-x", oauthNone},
		{"google subscription", "GOOGLE", "subscription", "{}", oauthGoogle},
		{"cursor api key", "CURSOR", "api_key", "cur_x", oauthNone},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			cred := Credential{Type: "PROVIDER_LOGIN", Provider: c.provider, PlainValue: c.value,
				Fields: providerLoginParts("SLOT", c.mode, "", "", "", "")}
			if got := credentialOAuthKind(cred); got != c.want {
				t.Errorf("credentialOAuthKind = %v, want %v", got, c.want)
			}
		})
	}
}

func TestCredTypeToProvider_ProviderLogin(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		cred Credential
		want string
	}{
		{"openai subscription stays out of the credstore",
			Credential{EnvVarName: "OPENAI_API_KEY", PlainValue: "eyJ.x.y", Type: "PROVIDER_LOGIN", Provider: "OPENAI",
				Fields: providerLoginParts("OPENAI_API_KEY", "subscription", "rt.x", "id", "acct", "")}, ""},
		{"openai api key lands under OPENAI",
			Credential{EnvVarName: "OPENAI_API_KEY", PlainValue: "sk-proj-x", Type: "PROVIDER_LOGIN", Provider: "OPENAI",
				Fields: providerLoginParts("OPENAI_API_KEY", "api_key", "", "", "", "")}, "OPENAI"},
		{"api key by provider column when the slot is custom",
			Credential{EnvVarName: "MY_OPENAI", PlainValue: "sk-proj-x", Type: "PROVIDER_LOGIN", Provider: "OPENAI",
				Fields: providerLoginParts("MY_OPENAI", "api_key", "", "", "", "")}, "OPENAI"},
		{"cursor api key",
			Credential{EnvVarName: "CURSOR_API_KEY", PlainValue: "cur_x", Type: "PROVIDER_LOGIN", Provider: "CURSOR",
				Fields: providerLoginParts("CURSOR_API_KEY", "api_key", "", "", "", "")}, "CURSOR"},
	}
	for _, c := range cases {
		if got := credTypeToProvider(c.cred); got != c.want {
			t.Errorf("%s: credTypeToProvider = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestBuildEnvVarsSidecar_ProviderLoginCodex_NoPartsInEnv(t *testing.T) {
	req := codexProviderLoginReq(t)
	env := BuildEnvVarsSidecar(req, false)
	assertNoLoginParts(t, env)
	if _, ok := envValue(env, "CLAUDE_CODE_OAUTH_TOKEN"); ok {
		t.Errorf("an OpenAI login must not be written to CLAUDE_CODE_OAUTH_TOKEN")
	}
	if got, _ := envValue(env, "OPENAI_API_KEY"); got != "sk-dummy-crewship-sidecar" {
		t.Errorf("OPENAI_API_KEY = %q, want the harmless dummy", got)
	}
	if _, ok := envValue(env, "CODEX_API_KEY"); ok {
		t.Errorf("CODEX_API_KEY must never be set: it overrides auth.json")
	}
	if got, ok := envValue(env, "CODEX_HOME"); !ok || got != "/crew/runs/reviewer/run-1/.codex" {
		t.Errorf("CODEX_HOME = %q (present=%v)", got, ok)
	}
	if got, _ := envValue(env, "CREWSHIP_BILLING_MODE"); got != "flat_rate" {
		t.Errorf("CREWSHIP_BILLING_MODE = %q, want flat_rate", got)
	}
	if got, _ := envValue(env, "CREWSHIP_SUBSCRIPTION_PLAN"); got != "ChatGPT Plus" {
		t.Errorf("CREWSHIP_SUBSCRIPTION_PLAN = %q, want ChatGPT Plus", got)
	}
	// The access token itself is delivered as the file, never as a variable.
	if strings.Contains(strings.Join(env, "\n"), req.Credentials[0].PlainValue) {
		t.Errorf("access token written to the env")
	}
}

func TestBuildEnvVars_ProviderLoginCodex_NoPartsInEnv(t *testing.T) {
	req := codexProviderLoginReq(t)
	for _, env := range [][]string{BuildEnvVars(req, nil), BuildEnvVars(req, &req.Credentials[0])} {
		assertNoLoginParts(t, env)
		if _, ok := envValue(env, "CLAUDE_CODE_OAUTH_TOKEN"); ok {
			t.Errorf("OpenAI login written to CLAUDE_CODE_OAUTH_TOKEN on the non-sidecar path")
		}
		if strings.Contains(strings.Join(env, "\n"), req.Credentials[0].PlainValue) {
			t.Errorf("access token written to the env on the non-sidecar path")
		}
	}
}

// An Anthropic subscription login is delivered exactly like an AI_CLI_TOKEN:
// CLAUDE_CODE_OAUTH_TOKEN, no Anthropic proxy base URL, flat-rate — and its
// parts (plan, mode, expiry) stay out of the env like every other login's.
func TestBuildEnvVarsSidecar_ProviderLoginAnthropic(t *testing.T) {
	cred := Credential{ID: "pl2", EnvVarName: "CLAUDE_CODE_OAUTH_TOKEN", PlainValue: "sk-ant-oat01-x",
		Type: "PROVIDER_LOGIN", Provider: "ANTHROPIC",
		Fields: providerLoginParts("CLAUDE_CODE_OAUTH_TOKEN", "subscription", "", "", "org-1", "max")}
	req := AgentRunRequest{AgentID: "a1", AgentSlug: "lead", CLIAdapter: "CLAUDE_CODE", Credentials: []Credential{cred}}
	for name, env := range map[string][]string{
		"sidecar":     BuildEnvVarsSidecar(req, false),
		"non-sidecar": BuildEnvVars(req, nil),
	} {
		if got, _ := envValue(env, "CLAUDE_CODE_OAUTH_TOKEN"); got != "sk-ant-oat01-x" {
			t.Errorf("%s: CLAUDE_CODE_OAUTH_TOKEN = %q", name, got)
		}
		assertNoLoginParts(t, env)
	}
	env := BuildEnvVarsSidecar(req, false)
	if _, ok := envValue(env, "ANTHROPIC_BASE_URL"); ok {
		t.Errorf("ANTHROPIC_BASE_URL must be absent in OAuth mode")
	}
	if got, _ := envValue(env, "CREWSHIP_BILLING_MODE"); got != "flat_rate" {
		t.Errorf("CREWSHIP_BILLING_MODE = %q", got)
	}
	if got, _ := envValue(env, "CREWSHIP_SUBSCRIPTION_PLAN"); got != "Claude Max" {
		t.Errorf("CREWSHIP_SUBSCRIPTION_PLAN = %q, want the plan the login names", got)
	}
	if resolveEnvVar(&cred) != "CLAUDE_CODE_OAUTH_TOKEN" {
		t.Errorf("resolveEnvVar = %q", resolveEnvVar(&cred))
	}
}

// api_key mode behaves exactly like an API_KEY of the same provider: the
// sidecar CredStore holds it, Codex is routed through the proxy, the env
// keeps the dummy, billing is metered.
func TestProviderLoginAPIKey_BehavesLikeAPIKey(t *testing.T) {
	key := Credential{ID: "pl3", EnvVarName: "OPENAI_API_KEY", PlainValue: "sk-proj-real", Type: "PROVIDER_LOGIN", Provider: "OPENAI",
		Fields: providerLoginParts("OPENAI_API_KEY", "api_key", "", "", "", "")}
	req := AgentRunRequest{AgentID: "a1", AgentSlug: "coder", CLIAdapter: "CODEX_CLI", LLMProvider: "OPENAI", LLMModel: "gpt-5.5",
		sidecarActive: true, Credentials: []Credential{key}}

	sc := buildSidecarCreds(req.Credentials, nil)
	if len(sc) != 1 || sc[0].Provider != "OPENAI" || sc[0].Token != "sk-proj-real" {
		t.Fatalf("sidecar creds = %+v, want the key under OPENAI", sc)
	}
	if _, ok := codexLoginCredential(req); ok {
		t.Errorf("an api_key login must not be taken for a ChatGPT login")
	}
	env := BuildEnvVarsSidecar(req, false)
	if got, _ := envValue(env, "OPENAI_API_KEY"); got != "sk-dummy-crewship-sidecar" {
		t.Errorf("OPENAI_API_KEY = %q, want the dummy: the sidecar injects the real key", got)
	}
	if got, _ := envValue(env, "CREWSHIP_BILLING_MODE"); got != "metered" {
		t.Errorf("CREWSHIP_BILLING_MODE = %q, want metered", got)
	}
	assertNoLoginParts(t, env)
	if cmd := strings.Join(codexAdapter{}.BuildCommand(req), " "); !strings.Contains(cmd, "model_provider") {
		t.Errorf("api-key run must be routed through the sidecar provider block:\n%s", cmd)
	}
	if _, isRouted := resolveRoutedProvider(req, true); !isRouted {
		t.Errorf("resolveRoutedProvider must route an api_key login")
	}

	// A CONNECT-tunnelled adapter still gets the real key in env, as it
	// does for an API_KEY.
	cursor := Credential{ID: "pl4", EnvVarName: "CURSOR_API_KEY", PlainValue: "cur_real", Type: "PROVIDER_LOGIN", Provider: "CURSOR",
		Fields: providerLoginParts("CURSOR_API_KEY", "api_key", "", "", "", "")}
	creq := AgentRunRequest{AgentID: "a1", AgentSlug: "c", CLIAdapter: "CURSOR_CLI", Credentials: []Credential{cursor}}
	cenv := BuildEnvVarsSidecar(creq, false)
	if got, _ := envValue(cenv, "CURSOR_API_KEY"); got != "cur_real" {
		t.Errorf("CURSOR_API_KEY = %q, want the real key (no endpoint override for Cursor)", got)
	}
	assertNoLoginParts(t, cenv)
}

func TestSyncCodexAuthFile_RendersFromProviderLoginParts(t *testing.T) {
	req := codexProviderLoginReq(t)
	rc := &countingContainer{}
	if err := syncLoginFile(context.Background(), rc, "ctr", req, slog.Default()); err != nil {
		t.Fatalf("sync: %v", err)
	}
	scripts := scriptsOf(rc)
	if len(scripts) != 1 {
		t.Fatalf("want one exec, got %d: %v", len(scripts), scripts)
	}
	var body []byte
	for i, f := range strings.Fields(scripts[0]) {
		if f == "echo" && i+1 < len(strings.Fields(scripts[0])) {
			if b, err := base64.StdEncoding.DecodeString(strings.Fields(scripts[0])[i+1]); err == nil && strings.Contains(string(b), "access_token") {
				body = b
			}
		}
	}
	if body == nil {
		t.Fatalf("could not find the rendered file in: %s", scripts[0])
	}
	s := string(body)
	if strings.Contains(s, "rt.REAL-SECRET") {
		t.Fatalf("delivered auth.json carries the real refresh token:\n%s", s)
	}
	for _, want := range []string{codexauth.PlaceholderRefreshToken, `"account_id": "acct-123"`, `"id_token": "id.tok.en"`, req.Credentials[0].PlainValue, `"auth_mode": "chatgpt"`} {
		if !strings.Contains(s, want) {
			t.Errorf("rendered auth.json lacks %q:\n%s", want, s)
		}
	}
}

func TestSyncCodexAuthFile_ProviderLoginWithoutIDTokenFailsTheRun(t *testing.T) {
	req := codexProviderLoginReq(t)
	req.Credentials[0].Fields = providerLoginParts("OPENAI_API_KEY", "subscription", "rt.x", "", "acct-123", "plus")
	rc := &countingContainer{}
	if err := syncLoginFile(context.Background(), rc, "ctr", req, slog.Default()); err == nil {
		t.Fatal("expected an error: Codex refuses a file without id_token")
	}
	if len(scriptsOf(rc)) != 0 {
		t.Errorf("nothing should have been written: %v", scriptsOf(rc))
	}
}

func TestAgentEnvCredentialExposures_ProviderLogin(t *testing.T) {
	req := codexProviderLoginReq(t)
	var file []CredentialEnvExposure
	for _, e := range AgentEnvCredentialExposures(req, false) {
		if strings.Contains(e.EnvVarName, "REFRESH_TOKEN") || strings.Contains(e.EnvVarName, "ID_TOKEN") {
			t.Errorf("a login part reported as exposed — it is not delivered: %+v", e)
		}
		if e.EnvVarName == codexauth.FileRel {
			file = append(file, e)
		}
	}
	if len(file) != 1 || file[0].Type != "PROVIDER_LOGIN" || file[0].Actionable {
		t.Errorf("file exposures = %+v, want one informational PROVIDER_LOGIN entry", file)
	}
}

// A login is never a /secrets file, and neither are its parts.
func TestBuildCredFileScript_ProviderLoginWritesNothing(t *testing.T) {
	script, n, skipped, err := buildCredFileScript([]Credential{codexProviderLoginCred(t)}, "/secrets/reviewer", false)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 || len(skipped) != 0 || strings.Contains(script, "rt.REAL-SECRET") || strings.Contains(script, "REFRESH_TOKEN") {
		t.Errorf("script wrote a login to /secrets: n=%d skipped=%v\n%s", n, skipped, script)
	}
}
