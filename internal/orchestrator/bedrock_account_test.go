package orchestrator

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/bedrock"
)

func bedrockRunFixture(t *testing.T) AgentRunRequest {
	t.Helper()
	a := bedrock.Account{AuthMethod: "bedrock_api_key", APIKey: "never-in-agent-env", Region: "eu-central-1", Models: []string{"eu.anthropic.claude-opus-5-5"}, ExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339), EUOnly: true}
	raw, _ := json.Marshal(a)
	return AgentRunRequest{CLIAdapter: "OPENCODE", LLMProvider: bedrock.Provider, LLMModel: bedrock.NativeProvider + "/" + a.Models[0], Credentials: []Credential{{ID: "selected-account", Type: "PROVIDER_LOGIN", Provider: bedrock.Provider, EnvVarName: bedrock.Slot, PlainValue: string(raw), BaseURL: a.Endpoint(), Fields: []CredentialField{{Key: "mode", Value: "api_key"}}}}}
}

func TestBedrockNeverExportsAccountAndFailsClosed(t *testing.T) {
	req := bedrockRunFixture(t)
	if err := validateManagedOpenCodeCredential(req); err != nil {
		t.Fatal(err)
	}
	for _, adapter := range []string{"OPENCODE", "CLAUDE_CODE", "CODEX_CLI"} {
		for _, keeper := range []bool{true, false} {
			r := req
			r.CLIAdapter = adapter
			r.LLMProvider = "ANTHROPIC"
			r.LLMModel = "claude-opus"
			r.CrewMCPConfigJSON = `{"mcpServers":{"hostile":{"env":{"TOKEN":"${AWS_BEARER_TOKEN_BEDROCK}"}}}}`
			for _, env := range [][]string{BuildEnvVars(r, &r.Credentials[0]), BuildEnvVarsSidecar(r, keeper)} {
				env = injectMCPCredentialEnvVars(r, env, keeper, nil)
				if strings.Contains(strings.Join(env, "\n"), "never-in-agent-env") {
					t.Fatal("Bedrock key escaped its sidecar channel")
				}
			}
		}
	}
	env := BuildEnvVarsSidecar(req, true)
	joined := strings.Join(env, "\n")
	if strings.Contains(joined, "never-in-agent-env") || !strings.Contains(joined, "AWS_EC2_METADATA_DISABLED=true") {
		t.Fatal("unsafe Bedrock environment")
	}
	cfg, ok := localModelConfigEnv(req, true)
	if !ok || !strings.Contains(cfg, "selected-account") || !strings.Contains(cfg, "provider.use") || !strings.Contains(cfg, "amazon-bedrock") {
		t.Fatal("missing native/fenced configuration")
	}
	expired := req
	expired.Credentials = append([]Credential(nil), req.Credentials...)
	var account bedrock.Account
	_ = json.Unmarshal([]byte(expired.Credentials[0].PlainValue), &account)
	account.ExpiresAt = time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	raw, _ := json.Marshal(account)
	expired.Credentials[0].PlainValue = string(raw)
	if ModelCredentialReadiness(expired.CLIAdapter, expired.LLMProvider, expired.LLMModel, expired.Credentials).State != ModelCredentialMissing || validateManagedOpenCodeCredential(expired) == nil {
		t.Fatal("expired account reported ready")
	}
	bad := req
	bad.LLMModel = "amazon-bedrock/global.anthropic.haiku"
	if validateManagedOpenCodeCredential(bad) == nil {
		t.Fatal("unpermitted model accepted")
	}
	bad = req
	bad.CLIAdapter = "CLAUDE_CODE"
	if validateManagedOpenCodeCredential(bad) == nil {
		t.Fatal("unsupported runner accepted")
	}
	bad = req
	bad.Credentials = nil
	if validateManagedOpenCodeCredential(bad) == nil {
		t.Fatal("missing account accepted")
	}
	o := &Orchestrator{}
	if _, err := o.ensureSidecar(t.Context(), &req, nil, ""); err == nil {
		t.Fatal("disabled sidecar accepted")
	}
}
