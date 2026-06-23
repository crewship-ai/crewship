package orchestrator

import "testing"

// AgentEnvCredentialExposures must mirror BuildEnvVarsSidecar's injection logic
// exactly: it reports every credential whose plaintext lands in the agent env and
// nothing that the sidecar reverse-proxy isolates. A drift here means operators are
// told the wrong story about which secrets an agent can read.
func TestAgentEnvCredentialExposures(t *testing.T) {
	apiKey := func(env string) Credential {
		return Credential{EnvVarName: env, PlainValue: "real-" + env, Type: "API_KEY"}
	}
	oauthTyped := Credential{EnvVarName: "ANTHROPIC_API_KEY", PlainValue: "tok-oauth", Type: "AI_CLI_TOKEN"}
	oauthByPrefix := Credential{EnvVarName: "ANYTHING", PlainValue: "sk-ant-oat01-yyy", Type: "API_KEY"}
	cli := Credential{EnvVarName: "GH_TOKEN", PlainValue: "ghp_xxx", Type: "CLI_TOKEN"}
	secret := Credential{EnvVarName: "STRIPE_KEY", PlainValue: "sk_live_xxx", Type: "SECRET"}

	// exp is the comparable projection of a CredentialEnvExposure — Reason is
	// free text and intentionally excluded from the assertion.
	type exp struct {
		EnvVar     string
		Type       string
		Actionable bool
	}

	tests := []struct {
		name    string
		adapter string
		keeper  bool
		creds   []Credential
		want    []exp // in the helper's deterministic append order: oauth, apikey, cli, secret
	}{
		{
			name:    "claude api key is isolated by the reverse-proxy",
			adapter: "CLAUDE_CODE", keeper: true,
			creds: []Credential{apiKey("ANTHROPIC_API_KEY")},
			want:  nil,
		},
		{
			name:    "codex byo openai key is exposed over CONNECT",
			adapter: "CODEX_CLI", keeper: true,
			creds: []Credential{apiKey("OPENAI_API_KEY")},
			want:  []exp{{"OPENAI_API_KEY", "API_KEY", false}},
		},
		{
			name:    "cross-adapter key the CLI never reads stays isolated",
			adapter: "CODEX_CLI", keeper: true, // codex only reads OPENAI_API_KEY
			creds: []Credential{apiKey("ANTHROPIC_API_KEY")},
			want:  nil,
		},
		{
			name:    "gemini key reported once by its own env var",
			adapter: "GEMINI_CLI", keeper: true, // allowed = {GOOGLE_API_KEY, GEMINI_API_KEY}
			creds: []Credential{apiKey("GOOGLE_API_KEY")},
			want:  []exp{{"GOOGLE_API_KEY", "API_KEY", false}},
		},
		{
			name:    "oauth by type reported, not actionable",
			adapter: "CLAUDE_CODE", keeper: true,
			creds: []Credential{oauthTyped},
			want:  []exp{{"CLAUDE_CODE_OAUTH_TOKEN", "AI_CLI_TOKEN", false}},
		},
		{
			name:    "oauth detected by sk-ant-oat prefix even when typed API_KEY",
			adapter: "CLAUDE_CODE", keeper: true,
			creds: []Credential{oauthByPrefix},
			want:  []exp{{"CLAUDE_CODE_OAUTH_TOKEN", "AI_CLI_TOKEN", false}},
		},
		{
			name:    "only the first oauth token is reported (mirrors break)",
			adapter: "CLAUDE_CODE", keeper: true,
			creds: []Credential{oauthTyped, oauthByPrefix},
			want:  []exp{{"CLAUDE_CODE_OAUTH_TOKEN", "AI_CLI_TOKEN", false}},
		},
		{
			name:    "cli token reported, not actionable",
			adapter: "CLAUDE_CODE", keeper: true,
			creds: []Credential{cli},
			want:  []exp{{"GH_TOKEN", "CLI_TOKEN", false}},
		},
		{
			name:    "secret is actionable when keeper is off",
			adapter: "CLAUDE_CODE", keeper: false,
			creds: []Credential{secret},
			want:  []exp{{"STRIPE_KEY", "SECRET", true}},
		},
		{
			name:    "secret is isolated when keeper is on",
			adapter: "CLAUDE_CODE", keeper: true,
			creds: []Credential{secret},
			want:  nil,
		},
		{
			name:    "empty values are ignored",
			adapter: "CODEX_CLI", keeper: false,
			creds: []Credential{
				{EnvVarName: "OPENAI_API_KEY", PlainValue: "", Type: "API_KEY"},
				{EnvVarName: "X", PlainValue: "", Type: "SECRET"},
			},
			want: nil,
		},
		{
			name:    "claude mixed bag: api key isolated, oauth+cli+secret exposed",
			adapter: "CLAUDE_CODE", keeper: false,
			creds: []Credential{apiKey("ANTHROPIC_API_KEY"), oauthTyped, cli, secret},
			want: []exp{
				{"CLAUDE_CODE_OAUTH_TOKEN", "AI_CLI_TOKEN", false},
				{"GH_TOKEN", "CLI_TOKEN", false},
				{"STRIPE_KEY", "SECRET", true},
			},
		},
		{
			name:    "codex mixed bag: byo key + cli + secret all exposed",
			adapter: "CODEX_CLI", keeper: false,
			creds: []Credential{apiKey("OPENAI_API_KEY"), cli, secret},
			want: []exp{
				{"OPENAI_API_KEY", "API_KEY", false},
				{"GH_TOKEN", "CLI_TOKEN", false},
				{"STRIPE_KEY", "SECRET", true},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := AgentEnvCredentialExposures(
				AgentRunRequest{AgentID: "a1", CLIAdapter: tc.adapter, Credentials: tc.creds},
				tc.keeper,
			)
			if len(got) != len(tc.want) {
				t.Fatalf("got %d exposures, want %d\n got:  %+v\n want: %+v", len(got), len(tc.want), got, tc.want)
			}
			for i, w := range tc.want {
				if got[i].EnvVarName != w.EnvVar || got[i].Type != w.Type || got[i].Actionable != w.Actionable {
					t.Errorf("exposure[%d] = {%q %q actionable=%v}, want {%q %q actionable=%v}",
						i, got[i].EnvVarName, got[i].Type, got[i].Actionable, w.EnvVar, w.Type, w.Actionable)
				}
			}
		})
	}
}
