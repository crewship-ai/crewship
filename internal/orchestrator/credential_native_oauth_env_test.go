package orchestrator

import (
	"log/slog"
	"strings"
	"testing"
)

func TestNativeOAuthAccessTokenProcessSlot(t *testing.T) {
	for _, adapter := range []string{"CLAUDE_CODE", "CODEX_CLI", "OPENCODE"} {
		for _, keeper := range []bool{false, true} {
			t.Run(adapter+map[bool]string{false: "/keeper-off", true: "/keeper-on"}[keeper], func(t *testing.T) {
				cred := Credential{ID: "oauth", Type: "OAUTH2", OAuthPrimary: true, Provider: "GITHUB", EnvVarName: "NATIVE_OAUTH_ACCESS", PlainValue: "synthetic-access-token"}
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

func TestNativeOAuthMCPPartsDoNotBecomeGenericProcessTokens(t *testing.T) {
	creds := []Credential{
		{ID: "modern", Type: "OAUTH2", EnvVarName: "APP_CLIENT_ID", PlainValue: "fixture-client-id"},
		{ID: "modern", Type: "OAUTH2", EnvVarName: "APP_CLIENT_SECRET", PlainValue: "fixture-client-secret"},
		{ID: "modern", Type: "OAUTH2", EnvVarName: "_OAUTH_ACCESS_TOKEN:modern", PlainValue: "fixture-access"},
	}
	req := AgentRunRequest{AgentSlug: "fixture", RunID: "run", CLIAdapter: "CLAUDE_CODE", Credentials: creds}
	env := BuildEnvVarsSidecar(req, true)
	for _, c := range creds {
		for _, entry := range env {
			if strings.Contains(entry, c.PlainValue) {
				t.Fatal("an MCP-only OAuth companion entered generic process delivery")
			}
		}
	}
	req.CrewMCPConfigJSON = `{"mcpServers":{"fixture":{"command":"fixture","env":{"id":"${APP_CLIENT_ID}","secret":"${APP_CLIENT_SECRET}"}}}}`
	explicit := injectMCPCredentialEnvVars(req, BuildEnvVarsSidecar(req, true), true, slog.Default())
	if got := envMap(t, explicit); got["APP_CLIENT_ID"] != creds[0].PlainValue || got["APP_CLIENT_SECRET"] != creds[1].PlainValue {
		t.Fatal("explicit OAuth MCP references no longer work")
	}
	for _, entry := range explicit {
		if strings.Contains(entry, creds[2].PlainValue) {
			t.Fatal("synthetic OAuth access-token selector became an env assignment")
		}
	}
}

func TestNativeOAuthPrimarySlotIsNotInferredFromItsSuffix(t *testing.T) {
	c := Credential{ID: "primary", Type: "OAUTH2", OAuthPrimary: true, EnvVarName: "CUSTOM_CLIENT_SECRET", PlainValue: "fixture-primary-access"}
	req := AgentRunRequest{CLIAdapter: "CLAUDE_CODE", Credentials: []Credential{c}}
	if got := envMap(t, BuildEnvVarsSidecar(req, true))[c.EnvVarName]; got != c.PlainValue {
		t.Fatal("a primary access-token slot was treated as a client-secret companion by name")
	}
}

func TestNativeOAuthExpiredSnapshotAndSecretPartExposure(t *testing.T) {
	base := Credential{ID: "primary", Type: "OAUTH2", OAuthPrimary: true, EnvVarName: "NATIVE_OAUTH_ACCESS", PlainValue: "fixture-primary-access",
		Fields: []CredentialField{{Key: "secret_part", EnvVar: "NATIVE_OAUTH_SECRET_PART", Value: "fixture-secret-part", IsSecret: true}}}
	for _, deadline := range []string{"", "2000-01-01T00:00:00Z", "not-a-deadline"} {
		t.Run("lease-"+deadline, func(t *testing.T) {
			c := base
			c.LeaseExpiresAt = deadline
			req := AgentRunRequest{CLIAdapter: "CLAUDE_CODE", Credentials: []Credential{c}}
			env := BuildEnvVarsSidecar(req, true)
			got := envMap(t, env)
			exposures := AgentEnvCredentialExposures(req, true)
			if deadline == "" {
				if got[c.EnvVarName] != c.PlainValue || got[c.Fields[0].EnvVar] != c.Fields[0].Value {
					t.Fatal("primary OAuth token or its declared secret part did not reach the process")
				}
				seen := map[string]bool{}
				for _, e := range exposures {
					seen[e.EnvVarName] = true
				}
				if !seen[c.EnvVarName] || !seen[c.Fields[0].EnvVar] {
					t.Fatal("native OAuth plaintext delivery missing from exposure reporting")
				}
			} else {
				for _, entry := range env {
					if strings.Contains(entry, c.PlainValue) || strings.Contains(entry, c.Fields[0].Value) {
						t.Fatal("expired or malformed cached grant leaked access-token material")
					}
				}
				if len(exposures) != 0 {
					t.Fatal("withheld OAuth snapshot was reported as delivered")
				}
			}
		})
	}
}

func TestNativeOAuthSlotCannotClaimRuntimeOrAnotherCredential(t *testing.T) {
	for _, name := range []string{"PATH", "HOME", "BASH_ENV", "CREWSHIP_AGENT_TOKEN", "OPENCODE_CONFIG_CONTENT"} {
		t.Run(name, func(t *testing.T) {
			cred := Credential{ID: "oauth", Type: "OAUTH2", OAuthPrimary: true, EnvVarName: name, PlainValue: "synthetic-access"}
			req := AgentRunRequest{AgentSlug: "fixture", RunID: "run", CLIAdapter: "CLAUDE_CODE", Credentials: []Credential{cred}}
			if _, err := appendCredentialFileEnv(BuildEnvVarsSidecar(req, true), req.Credentials, t.TempDir(), true); err == nil {
				t.Fatal("OAuth access-token slot claimed a runtime variable")
			}
		})
	}
	for _, otherType := range []string{"OAUTH2", "CLI_TOKEN", "GENERIC_SECRET"} {
		t.Run("collision-"+otherType, func(t *testing.T) {
			creds := []Credential{{ID: "oauth", Type: "OAUTH2", OAuthPrimary: true, EnvVarName: "COLLISION", PlainValue: "synthetic-access"}, {ID: "other", Type: otherType, OAuthPrimary: otherType == "OAUTH2", EnvVarName: "COLLISION", PlainValue: "synthetic-other"}}
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
		{ID: "login", Type: "OAUTH2", OAuthPrimary: true, Provider: "ANTHROPIC", EnvVarName: "ANTHROPIC_API_KEY", PlainValue: "sk-ant-oat-synthetic-login"},
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
