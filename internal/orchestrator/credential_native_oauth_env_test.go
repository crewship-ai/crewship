package orchestrator

import (
	"strings"
	"testing"
)

func TestNativeOAuthAccessTokenProcessSlot(t *testing.T) {
	for _, adapter := range []string{"CLAUDE_CODE", "CODEX_CLI", "OPENCODE"} {
		for _, keeper := range []bool{false, true} {
			t.Run(adapter+map[bool]string{false: "/keeper-off", true: "/keeper-on"}[keeper], func(t *testing.T) {
				cred := Credential{ID: "oauth", Type: "OAUTH2", Provider: "GITHUB", EnvVarName: "NATIVE_OAUTH_ACCESS", PlainValue: "synthetic-access-token"}
				req := AgentRunRequest{AgentSlug: "fixture", RunID: "run", CLIAdapter: adapter, Credentials: []Credential{cred}}
				env, err := appendCredentialFileEnv(BuildEnvVarsSidecar(req, keeper), req.Credentials, t.TempDir(), keeper)
				if err != nil {
					t.Fatal(err)
				}
				if got := envMap(t, env)[cred.EnvVarName]; got != cred.PlainValue {
					t.Fatal("generic OAuth access-token slot missing from native process environment")
				}
			})
		}
	}
}

func TestNativeOAuthSlotCannotClaimRuntimeOrAnotherCredential(t *testing.T) {
	for _, name := range []string{"PATH", "HOME", "BASH_ENV", "CREWSHIP_AGENT_TOKEN", "OPENCODE_CONFIG_CONTENT"} {
		t.Run(name, func(t *testing.T) {
			cred := Credential{ID: "oauth", Type: "OAUTH2", EnvVarName: name, PlainValue: "synthetic-access"}
			req := AgentRunRequest{AgentSlug: "fixture", RunID: "run", CLIAdapter: "CLAUDE_CODE", Credentials: []Credential{cred}}
			if _, err := appendCredentialFileEnv(BuildEnvVarsSidecar(req, true), req.Credentials, t.TempDir(), true); err == nil {
				t.Fatal("OAuth access-token slot claimed a runtime variable")
			}
		})
	}
	for _, otherType := range []string{"OAUTH2", "CLI_TOKEN", "GENERIC_SECRET"} {
		t.Run("collision-"+otherType, func(t *testing.T) {
			creds := []Credential{{ID: "oauth", Type: "OAUTH2", EnvVarName: "COLLISION", PlainValue: "synthetic-access"}, {ID: "other", Type: otherType, EnvVarName: "COLLISION", PlainValue: "synthetic-other"}}
			req := AgentRunRequest{AgentSlug: "fixture", RunID: "run", CLIAdapter: "CLAUDE_CODE", Credentials: creds}
			if _, err := appendCredentialFileEnv(BuildEnvVarsSidecar(req, true), creds, t.TempDir(), true); err == nil || !strings.Contains(err.Error(), "collision") {
				t.Fatal("ambiguous OAuth credential slot did not refuse delivery")
			}
		})
	}
}

func TestNativeOAuthInternalSelectorsAndSpecializedLoginStaySeparate(t *testing.T) {
	creds := []Credential{
		{ID: "oauth", Type: "OAUTH2", EnvVarName: "_OAUTH_ACCESS_TOKEN:oauth", PlainValue: "synthetic-internal-token"},
		{ID: "login", Type: "OAUTH2", Provider: "ANTHROPIC", EnvVarName: "ANTHROPIC_API_KEY", PlainValue: "sk-ant-oat-synthetic-login"},
		{ID: "unknown", Type: "UNCLASSIFIED", EnvVarName: "UNKNOWN_ACCESS_TOKEN", PlainValue: "synthetic-unknown"},
	}
	req := AgentRunRequest{AgentSlug: "fixture", RunID: "run", CLIAdapter: "CLAUDE_CODE", Credentials: creds}
	env, err := appendCredentialFileEnv(BuildEnvVarsSidecar(req, true), creds, t.TempDir(), true)
	if err != nil {
		t.Fatal(err)
	}
	got := envMap(t, env)
	if got["CLAUDE_CODE_OAUTH_TOKEN"] != creds[1].PlainValue || envHasName(env, "ANTHROPIC_API_KEY") {
		t.Fatal("specialized Anthropic login delivery was changed")
	}
	for _, entry := range env {
		if strings.HasPrefix(entry, "_OAUTH_ACCESS_TOKEN:") || strings.Contains(entry, creds[0].PlainValue) || strings.Contains(entry, creds[2].PlainValue) {
			t.Fatal("internal OAuth selector or unknown type leaked into process environment")
		}
	}
}
