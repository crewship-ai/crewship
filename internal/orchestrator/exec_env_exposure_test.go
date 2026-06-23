package orchestrator

import "testing"

// AgentEnvCredentialExposures must mirror BuildEnvVarsSidecar's injection logic
// exactly: it reports every credential whose plaintext lands in the agent env and
// nothing that the sidecar proxy isolates. A drift here means operators are told
// the wrong story about which secrets an agent can read.
func TestAgentEnvCredentialExposures(t *testing.T) {
	apiKey := Credential{EnvVarName: "ANTHROPIC_API_KEY", PlainValue: "sk-ant-api03-xxx", Type: "API_KEY"}
	oauthTyped := Credential{EnvVarName: "ANTHROPIC_API_KEY", PlainValue: "tok-oauth", Type: "AI_CLI_TOKEN"}
	oauthByPrefix := Credential{EnvVarName: "ANYTHING", PlainValue: "sk-ant-oat01-yyy", Type: "API_KEY"}
	cli := Credential{EnvVarName: "GH_TOKEN", PlainValue: "ghp_xxx", Type: "CLI_TOKEN"}
	secret := Credential{EnvVarName: "STRIPE_KEY", PlainValue: "sk_live_xxx", Type: "SECRET"}

	mkReq := func(creds ...Credential) AgentRunRequest {
		return AgentRunRequest{AgentID: "a1", Credentials: creds}
	}

	t.Run("api key only is isolated by the proxy", func(t *testing.T) {
		if got := AgentEnvCredentialExposures(mkReq(apiKey), true); len(got) != 0 {
			t.Fatalf("API_KEY should never be exposed in env, got %+v", got)
		}
	})

	t.Run("oauth by type is reported, not actionable", func(t *testing.T) {
		got := AgentEnvCredentialExposures(mkReq(oauthTyped), true)
		if len(got) != 1 {
			t.Fatalf("want 1 exposure, got %d (%+v)", len(got), got)
		}
		if got[0].EnvVarName != "CLAUDE_CODE_OAUTH_TOKEN" || got[0].Type != "AI_CLI_TOKEN" {
			t.Errorf("unexpected exposure shape: %+v", got[0])
		}
		if got[0].Actionable {
			t.Error("OAuth exposure is structurally un-isolatable; must not be Actionable")
		}
	})

	t.Run("oauth detected by sk-ant-oat prefix even when typed API_KEY", func(t *testing.T) {
		got := AgentEnvCredentialExposures(mkReq(oauthByPrefix), true)
		if len(got) != 1 || got[0].Type != "AI_CLI_TOKEN" {
			t.Fatalf("prefix-detected OAuth should be reported, got %+v", got)
		}
	})

	t.Run("only the first oauth token is reported (mirrors break)", func(t *testing.T) {
		got := AgentEnvCredentialExposures(mkReq(oauthTyped, oauthByPrefix), true)
		oauthCount := 0
		for _, e := range got {
			if e.Type == "AI_CLI_TOKEN" {
				oauthCount++
			}
		}
		if oauthCount != 1 {
			t.Fatalf("BuildEnvVarsSidecar injects only the first OAuth token; want 1 reported, got %d", oauthCount)
		}
	})

	t.Run("cli token is reported, not actionable", func(t *testing.T) {
		got := AgentEnvCredentialExposures(mkReq(cli), true)
		if len(got) != 1 || got[0].Type != "CLI_TOKEN" || got[0].Actionable {
			t.Fatalf("CLI_TOKEN should be reported as non-actionable, got %+v", got)
		}
	})

	t.Run("secret is actionable only when keeper is disabled", func(t *testing.T) {
		off := AgentEnvCredentialExposures(mkReq(secret), false)
		if len(off) != 1 || off[0].Type != "SECRET" || !off[0].Actionable {
			t.Fatalf("SECRET with Keeper off must be an actionable exposure, got %+v", off)
		}
		on := AgentEnvCredentialExposures(mkReq(secret), true)
		if len(on) != 0 {
			t.Fatalf("SECRET with Keeper on is isolated; want 0 exposures, got %+v", on)
		}
	})

	t.Run("empty values are ignored", func(t *testing.T) {
		blank := Credential{EnvVarName: "X", PlainValue: "", Type: "SECRET"}
		if got := AgentEnvCredentialExposures(mkReq(blank), false); len(got) != 0 {
			t.Fatalf("empty-value credential must not be reported, got %+v", got)
		}
	})

	t.Run("mixed bag reports each exposed credential once", func(t *testing.T) {
		got := AgentEnvCredentialExposures(mkReq(apiKey, oauthTyped, cli, secret), false)
		// oauth + cli + secret = 3; api key isolated.
		if len(got) != 3 {
			t.Fatalf("want 3 exposures (oauth, cli, secret), got %d (%+v)", len(got), got)
		}
	})
}
