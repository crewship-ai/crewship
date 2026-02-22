package orchestrator

import (
	"strings"
	"testing"
)

// TestBuildEnvVarsSidecar_SecretCredentials_NotInEnv verifies that SECRET-type
// credentials are NOT injected as env vars in sidecar mode.
// Agents must request secrets via the Keeper API (/keeper/request), which enforces
// access control and creates an audit trail for every access.
func TestBuildEnvVarsSidecar_SecretCredentials_NotInEnv(t *testing.T) {
	req := AgentRunRequest{
		AgentID:   "a1",
		AgentSlug: "alice",
		CrewID:    "crew-1",
		ChatID:    "chat-1",
		Credentials: []Credential{
			{ID: "c1", Type: "API_KEY", EnvVarName: "ANTHROPIC_API_KEY", PlainValue: "sk-ant-123"},
			{ID: "c2", Type: "SECRET", EnvVarName: "PROD_DB_PASSWORD", PlainValue: "hunter2"},
		},
	}
	env := BuildEnvVarsSidecar(req)

	for _, e := range env {
		// The secret value must never appear in env
		if strings.Contains(e, "hunter2") {
			t.Errorf("SECRET credential value found in env vars: %q", e)
		}
		// The env var name for the secret must not be set
		if strings.HasPrefix(e, "PROD_DB_PASSWORD=") {
			t.Errorf("SECRET credential env var PROD_DB_PASSWORD must not be in sidecar env: %q", e)
		}
	}
}

// TestBuildEnvVarsSidecar_NoSecretCredentialTypes_InOutput verifies that ALL
// SECRET-type credentials are excluded from env vars, regardless of how many
// SECRET credentials are present.
func TestBuildEnvVarsSidecar_NoSecretCredentialTypes_InOutput(t *testing.T) {
	secretCreds := []Credential{
		{ID: "s1", Type: "SECRET", EnvVarName: "GITHUB_TOKEN", PlainValue: "ghp_realtoken"},
		{ID: "s2", Type: "SECRET", EnvVarName: "SLACK_WEBHOOK", PlainValue: "https://hooks.slack.com/secret"},
		{ID: "s3", Type: "SECRET", EnvVarName: "STRIPE_SECRET_KEY", PlainValue: "sk_live_realsecret"},
		{ID: "s4", Type: "SECRET", EnvVarName: "DATABASE_URL", PlainValue: "postgres://user:pass@host/db"},
	}

	req := AgentRunRequest{
		AgentID:     "a1",
		AgentSlug:   "bob",
		CrewID:      "crew-1",
		ChatID:      "chat-1",
		Credentials: secretCreds,
	}
	env := BuildEnvVarsSidecar(req)

	secretEnvVarNames := []string{"GITHUB_TOKEN", "SLACK_WEBHOOK", "STRIPE_SECRET_KEY", "DATABASE_URL"}
	secretValues := []string{"ghp_realtoken", "https://hooks.slack.com/secret", "sk_live_realsecret", "postgres://user:pass@host/db"}

	for _, e := range env {
		for _, name := range secretEnvVarNames {
			if strings.HasPrefix(e, name+"=") {
				t.Errorf("SECRET credential %q must not appear in sidecar env vars: %q", name, e)
			}
		}
		for _, val := range secretValues {
			if strings.Contains(e, val) {
				t.Errorf("SECRET credential value %q must not appear in sidecar env vars: %q", val, e)
			}
		}
	}
}

// TestBuildEnvVarsSidecar_OAuthToken_NotTreatedAsSecret verifies that OAuth tokens
// (AI_CLI_TOKEN type) ARE injected as CLAUDE_CODE_OAUTH_TOKEN, since they work via
// HTTPS CONNECT tunnel and require env var injection (sidecar cannot inject them).
func TestBuildEnvVarsSidecar_OAuthToken_NotTreatedAsSecret(t *testing.T) {
	req := AgentRunRequest{
		AgentID:   "a1",
		AgentSlug: "carol",
		CrewID:    "crew-1",
		ChatID:    "chat-1",
		Credentials: []Credential{
			{ID: "o1", Type: "AI_CLI_TOKEN", EnvVarName: "CLAUDE_CODE_OAUTH_TOKEN", PlainValue: "sk-ant-oat-valid"},
			{ID: "s1", Type: "SECRET", EnvVarName: "PROD_SECRET", PlainValue: "do-not-expose"},
		},
	}
	env := BuildEnvVarsSidecar(req)

	envMap := make(map[string]string)
	for _, e := range env {
		parts := strings.SplitN(e, "=", 2)
		if len(parts) == 2 {
			envMap[parts[0]] = parts[1]
		}
	}

	// OAuth token must be in env (required for HTTPS CONNECT tunnel)
	if envMap["CLAUDE_CODE_OAUTH_TOKEN"] != "sk-ant-oat-valid" {
		t.Errorf("expected CLAUDE_CODE_OAUTH_TOKEN to be set, got %q", envMap["CLAUDE_CODE_OAUTH_TOKEN"])
	}

	// SECRET credential must NOT be in env
	if _, ok := envMap["PROD_SECRET"]; ok {
		t.Error("SECRET credential PROD_SECRET must not be in sidecar env vars")
	}
	if strings.Contains(strings.Join(env, " "), "do-not-expose") {
		t.Error("SECRET credential value 'do-not-expose' must not appear in env vars")
	}
}
