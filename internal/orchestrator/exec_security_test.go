package orchestrator

import (
	"strings"
	"testing"
)

// TestBuildEnvVarsSidecar_SecretExclusion verifies that SECRET-type credentials
// are excluded from sidecar env vars (agents use Keeper API instead), while
// non-SECRET types like API_KEY and AI_CLI_TOKEN are correctly injected.
func TestBuildEnvVarsSidecar_SecretExclusion(t *testing.T) {
	cases := []struct {
		name        string
		req         AgentRunRequest
		forbidNames []string // env var names that must NOT appear
		forbidVals  []string // values that must NOT appear
		requireEnv  map[string]string // env var name → expected value (must be present)
	}{
		{
			name: "mixed creds: SECRET excluded, API_KEY kept",
			req: AgentRunRequest{
				AgentID: "a1", AgentSlug: "alice", CrewID: "crew-1", ChatID: "chat-1",
				Credentials: []Credential{
					{ID: "c1", Type: "API_KEY", EnvVarName: "ANTHROPIC_API_KEY", PlainValue: "test-ant-123"},
					{ID: "c2", Type: "SECRET", EnvVarName: "PROD_DB_PASSWORD", PlainValue: "hunter2"},
				},
			},
			forbidNames: []string{"PROD_DB_PASSWORD"},
			forbidVals:  []string{"hunter2"},
		},
		{
			name: "all SECRET creds excluded",
			req: AgentRunRequest{
				AgentID: "a1", AgentSlug: "bob", CrewID: "crew-1", ChatID: "chat-1",
				Credentials: []Credential{
					{ID: "s1", Type: "SECRET", EnvVarName: "GITHUB_TOKEN", PlainValue: "ghp_faketoken"},
					{ID: "s2", Type: "SECRET", EnvVarName: "SLACK_WEBHOOK", PlainValue: "https://hooks.slack.example/fake"},
					{ID: "s3", Type: "SECRET", EnvVarName: "STRIPE_SECRET_KEY", PlainValue: "fake_stripe_key_placeholder"},
					{ID: "s4", Type: "SECRET", EnvVarName: "DATABASE_URL", PlainValue: "postgres://fake:fake@localhost/fake"},
				},
			},
			forbidNames: []string{"GITHUB_TOKEN", "SLACK_WEBHOOK", "STRIPE_SECRET_KEY", "DATABASE_URL"},
			forbidVals:  []string{"ghp_faketoken", "https://hooks.slack.example/fake", "fake_stripe_key_placeholder", "postgres://fake:fake@localhost/fake"},
		},
		{
			name: "OAuth token injected, SECRET excluded",
			req: AgentRunRequest{
				AgentID: "a1", AgentSlug: "carol", CrewID: "crew-1", ChatID: "chat-1",
				Credentials: []Credential{
					{ID: "o1", Type: "AI_CLI_TOKEN", EnvVarName: "CLAUDE_CODE_OAUTH_TOKEN", PlainValue: "test-ant-oat-valid"},
					{ID: "s1", Type: "SECRET", EnvVarName: "PROD_SECRET", PlainValue: "do-not-expose"},
				},
			},
			forbidNames: []string{"PROD_SECRET"},
			forbidVals:  []string{"do-not-expose"},
			requireEnv:  map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": "test-ant-oat-valid"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := BuildEnvVarsSidecar(tc.req, true)

			envMap := make(map[string]string)
			for _, e := range env {
				parts := strings.SplitN(e, "=", 2)
				if len(parts) == 2 {
					envMap[parts[0]] = parts[1]
				}
			}

			joined := strings.Join(env, " ")

			for _, name := range tc.forbidNames {
				if _, ok := envMap[name]; ok {
					t.Errorf("SECRET env var %q must not be in sidecar env vars", name)
				}
			}
			for _, val := range tc.forbidVals {
				if strings.Contains(joined, val) {
					t.Errorf("SECRET value %q must not appear in sidecar env vars", val)
				}
			}
			for k, v := range tc.requireEnv {
				if envMap[k] != v {
					t.Errorf("expected %s=%q, got %q", k, v, envMap[k])
				}
			}
		})
	}
}
