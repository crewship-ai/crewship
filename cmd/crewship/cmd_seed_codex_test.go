package main

import (
	"context"
	"encoding/json"
	"github.com/spf13/cobra"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/cmd/crewship/seeddata"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
	"github.com/crewship-ai/crewship/internal/llm"
)

func TestResolveSeedCodexLogin(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	content := `{"auth_mode":"chatgpt","OPENAI_API_KEY":null,"tokens":{"id_token":"id","access_token":"access","refresh_token":"refresh","account_id":"account"}}`
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(seedCodexAuthFileEnv, path)
	login, err := resolveSeedCodexLogin()
	if err != nil {
		t.Fatal(err)
	}
	if login.Type != "PROVIDER_LOGIN" || login.Provider != "OPENAI" || login.Mode != "subscription" || login.Value != content {
		t.Fatalf("wrong login metadata: type=%q provider=%q mode=%q value_match=%t", login.Type, login.Provider, login.Mode, login.Value == content)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveSeedCodexLogin(); err == nil || !strings.Contains(err.Error(), "0600") {
		t.Fatalf("insecure file must be refused: %v", err)
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	withoutRefresh := strings.Replace(content, `"refresh_token":"refresh"`, `"refresh_token":""`, 1)
	if err := os.WriteFile(path, []byte(withoutRefresh), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveSeedCodexLogin(); err == nil || !strings.Contains(err.Error(), "renewable") {
		t.Fatalf("login without refresh token must be refused: %v", err)
	}
}

func TestSeedCodexAuthFileFlagOverridesEnv(t *testing.T) {
	writeLogin := func(name string) (string, string) {
		content := `{"auth_mode":"chatgpt","OPENAI_API_KEY":null,"tokens":{"id_token":"id","access_token":"access","refresh_token":"refresh","account_id":"account"}}`
		if name == "flag-auth.json" {
			content = strings.Replace(content, `"account_id":"account"`, `"account_id":"flag-account"`, 1)
		}
		path := filepath.Join(t.TempDir(), name)
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		return path, content
	}
	envPath, envContent := writeLogin("env-auth.json")
	flagPath, flagContent := writeLogin("flag-auth.json")
	t.Setenv(seedCodexAuthFileEnv, envPath)
	t.Cleanup(func() { seedCodexAuthFileOverride = "" })

	// Without the flag the env var is the default and governs.
	seedCodexAuthFileOverride = ""
	login, err := resolveSeedCodexLogin()
	if err != nil {
		t.Fatal(err)
	}
	if login.Value != envContent {
		t.Fatal("env var did not serve as the default login path")
	}

	// An explicit flag value wins over the env var.
	seedCodexAuthFileOverride = flagPath
	login, err = resolveSeedCodexLogin()
	if err != nil {
		t.Fatal(err)
	}
	if login.Value != flagContent || login.Value == envContent {
		t.Fatal("--codex-auth-file did not override SEED_CODEX_AUTH_FILE")
	}

	// Errors name the knob the operator actually turned.
	seedCodexAuthFileOverride = "relative/auth.json"
	if _, err := resolveSeedCodexLogin(); err == nil || !strings.Contains(err.Error(), "--codex-auth-file") {
		t.Fatalf("relative flag path must be refused in the flag's name: %v", err)
	}
}

func TestSeedCmdCodexAuthFileFlagDefaultsEmpty(t *testing.T) {
	t.Setenv(seedCodexAuthFileEnv, "/tmp/seed-review-auth.json")
	flag := seedCmd.Flags().Lookup("codex-auth-file")
	if flag == nil {
		t.Fatal("crewship seed has no --codex-auth-file flag")
	}
	if got := flag.DefValue; got != "" {
		t.Fatalf("--codex-auth-file default = %q, want empty", got)
	}
}

func TestCodexRoutineDefsLeaveOriginalsUntouched(t *testing.T) {
	defs := []seeddata.RoutineDef{{Slug: "test", Definition: map[string]interface{}{
		"credentials_required": []map[string]interface{}{{"type": "API_KEY", "provider": "ANTHROPIC", "scope": "any"}},
		"steps":                []map[string]interface{}{{"type": "agent_run", "model_override": "claude-sonnet-5"}},
	}}}
	got, err := codexRoutineDefs(defs)
	if err != nil {
		t.Fatal(err)
	}
	reqs := got[0].Definition["credentials_required"].([]interface{})
	req := reqs[0].(map[string]interface{})
	if req["type"] != "PROVIDER_LOGIN" || req["provider"] != "OPENAI" {
		t.Fatalf("Codex requirement = %v", req)
	}
	steps := got[0].Definition["steps"].([]interface{})
	if got := steps[0].(map[string]interface{})["model_override"]; got != "codex:"+llm.AdapterDefaultModel("CODEX_CLI") {
		t.Fatalf("Codex routine model override = %v", got)
	}
	original := defs[0].Definition["credentials_required"].([]map[string]interface{})[0]
	if original["provider"] != "ANTHROPIC" {
		t.Fatal("source routine was mutated")
	}
}

func TestCodexDemoCatalogueHasNoClaudeRequirements(t *testing.T) {
	for _, source := range [][]seeddata.RoutineDef{seeddata.Routines, seeddata.EvalScenarios} {
		defs, err := codexRoutineDefs(source)
		if err != nil {
			t.Fatal(err)
		}
		for _, def := range defs {
			body, err := json.Marshal(def.Definition)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(body), `"provider":"ANTHROPIC"`) || strings.Contains(string(body), `"model_override":"claude-`) {
				t.Errorf("routine %s retains a Claude dependency", def.Slug)
			}
		}
	}
}

func TestSeedCodexLoginGrantsOnlyConvertedAgents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	content := `{"auth_mode":"chatgpt","OPENAI_API_KEY":null,"tokens":{"id_token":"id","access_token":"access","refresh_token":"refresh","account_id":"account"}}`
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(seedCodexAuthFileEnv, path)
	t.Setenv("SEED_GOOGLE_EMAIL", "")
	t.Setenv("SEED_GOOGLE_PASSWORD", "")
	t.Setenv("SEED_GITHUB_TOKEN", "")
	t.Setenv("CREWSHIP_SEED_OLLAMA", "")
	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnGet("/api/v1/credentials", clitest.JSONResponse(200, []map[string]string{}))
	stub.OnPost("/api/v1/credentials", clitest.JSONResponse(201, map[string]string{"id": covCredIDCli2}))
	stub.OnPost("/api/v1/agents/alex-id/credentials", clitest.JSONResponse(201, map[string]string{"id": "binding"}))
	if err := seedCredentials(context.Background(), newSeedClient(stub), map[string]string{"alex": "alex-id", "ollie": "ollie-id"}); err != nil {
		t.Fatal(err)
	}
	creates := stub.CallsFor("POST", "/api/v1/credentials")
	if len(creates) != 1 {
		t.Fatalf("credential creates = %d, want one", len(creates))
	}
	var body map[string]interface{}
	clitest.MustDecodeJSONBody(creates[0].Body, &body)
	if body["name"] != "CODEX_DEMO_LOGIN" || body["type"] != "PROVIDER_LOGIN" || body["provider"] != "OPENAI" || body["mode"] != "subscription" {
		t.Fatalf("wrong Codex login metadata: name=%v type=%v provider=%v mode=%v", body["name"], body["type"], body["provider"], body["mode"])
	}
	if len(stub.CallsFor("POST", "/api/v1/agents/alex-id/credentials")) != 1 || len(stub.CallsFor("POST", "/api/v1/agents/ollie-id/credentials")) != 0 {
		t.Fatal("Codex login was not limited to the converted agents")
	}
}

// The env var alone must be named as the source; only a passed flag is
// blamed as --codex-auth-file (CodeRabbit on #2702).
func TestSeedCodexAuthFlagSourceOnlyWhenPassed(t *testing.T) {
	t.Cleanup(func() { seedCodexAuthFileOverride = "" })
	newCmd := func() *cobra.Command {
		c := &cobra.Command{Use: "seed"}
		c.Flags().String("codex-auth-file", "", "")
		return c
	}
	t.Setenv(seedCodexAuthFileEnv, "relative/auth.json")

	cmd := newCmd()
	applySeedCodexAuthFlag(cmd)
	if _, err := resolveSeedCodexLogin(); err == nil || !strings.Contains(err.Error(), seedCodexAuthFileEnv) || strings.Contains(err.Error(), "--codex-auth-file") {
		t.Fatalf("env-only path: err = %v, want it to name %s", err, seedCodexAuthFileEnv)
	}

	cmd = newCmd()
	if err := cmd.Flags().Set("codex-auth-file", "also/relative.json"); err != nil {
		t.Fatal(err)
	}
	applySeedCodexAuthFlag(cmd)
	if _, err := resolveSeedCodexLogin(); err == nil || !strings.Contains(err.Error(), "--codex-auth-file") {
		t.Fatalf("flag path: err = %v, want it to name --codex-auth-file", err)
	}
}
