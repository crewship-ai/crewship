package orchestrator

import "testing"

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
