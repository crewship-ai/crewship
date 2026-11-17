package orchestrator

import (
	"strings"
	"testing"
)

// File credentials must reach child processes as paths on every adapter.
func TestNativeCredentialProcessAliases(t *testing.T) {
	for _, adapter := range []string{"CLAUDE_CODE", "CODEX_CLI", "OPENCODE", "GEMINI_CLI"} {
		t.Run(adapter, func(t *testing.T) {
			req := AgentRunRequest{AgentSlug: "synthetic", RunID: "synthetic-run", CLIAdapter: adapter,
				Credentials: []Credential{
					{ID: "ssh", Type: "SSH_KEY", EnvVarName: "SYNTH_SSH", PlainValue: "synthetic"},
					{ID: "generic", Type: "GENERIC_SECRET", EnvVarName: "SYNTH_FILE", PlainValue: "synthetic"},
					{ID: "cert", Type: "CERTIFICATE", EnvVarName: "SYNTH_CERT", PlainValue: "synthetic"},
					{ID: "userpass", Type: "USERPASS", EnvVarName: "SYNTH_LOGIN", Username: "fixture", PlainValue: "synthetic"},
				}}
			env, err := appendCredentialFileEnv(BuildEnvVarsSidecar(req, true), req.Credentials, agentSecretsDir(req.AgentSlug, req.RunID), true)
			if err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"SYNTH_SSH_PATH", "SYNTH_FILE", "SYNTH_CERT_PATH", "SYNTH_LOGIN_USERNAME", "SYNTH_LOGIN_PASSWORD"} {
				if !envHasName(env, name) {
					t.Errorf("process env missing %s", name)
				}
			}
		})
	}
}

func TestNativeFileEnvironmentValuesAndPolicy(t *testing.T) {
	req := AgentRunRequest{AgentSlug: "fixture", RunID: "run-one", Credentials: []Credential{
		{ID: "key", Type: "SSH_KEY", EnvVarName: "KEY", PlainValue: "synthetic-key"},
		{ID: "login", Type: "USERPASS", EnvVarName: "LOGIN", Username: "synthetic-user", PlainValue: "synthetic-password"},
		{ID: "token", Type: "CLI_TOKEN", EnvVarName: "GH_TOKEN", PlainValue: "synthetic-token"},
		{ID: "secret", Type: "SECRET", EnvVarName: "GATED", PlainValue: "synthetic-gated"},
		{ID: "unknown", Type: "UNKNOWN", EnvVarName: "UNCLASSIFIED", PlainValue: "synthetic-unknown"},
	}}
	dir := agentSecretsDir(req.AgentSlug, req.RunID)
	env, err := appendCredentialFileEnv(BuildEnvVarsSidecar(req, true), req.Credentials, dir, true)
	if err != nil {
		t.Fatal(err)
	}
	got := envMap(t, env)
	for name, want := range map[string]string{"KEY_PATH": dir + "/ssh/KEY", "LOGIN_USERNAME": dir + "/LOGIN_USERNAME", "LOGIN_PASSWORD": dir + "/LOGIN_PASSWORD", "GH_TOKEN": "synthetic-token"} {
		if got[name] != want {
			t.Fatalf("incorrect delivery for %s", name)
		}
	}
	if _, ok := got["GATED"]; ok {
		t.Fatal("Keeper-gated credential delivered")
	}
	if _, ok := got["UNCLASSIFIED"]; ok {
		t.Fatal("unknown credential delivered")
	}
	if strings.Contains(strings.Join(env, "\n"), "synthetic-password") {
		t.Fatal("USERPASS password leaked into process env")
	}
	req.Credentials = append(req.Credentials, Credential{ID: "collision", Type: "GENERIC_SECRET", EnvVarName: "KEY_PATH", PlainValue: "other"})
	if _, err := appendCredentialFileEnv(nil, req.Credentials, dir, true); err == nil {
		t.Fatal("derived alias collision accepted")
	}
}

func TestNativeFileEnvironmentCannotClaimRuntimeNames(t *testing.T) {
	for _, name := range []string{"CREWSHIP_SECRETS_DIR", "CREWSHIP_OUTPUT_DIR", "LD_PRELOAD", "BASH_ENV", "CODEX_HOME"} {
		creds := []Credential{{ID: "fixture", Type: "GENERIC_SECRET", EnvVarName: name, PlainValue: "synthetic"}}
		if _, err := appendCredentialFileEnv(nil, creds, "/secrets/fixture/run", true); err == nil {
			t.Fatalf("runtime name %s accepted", name)
		}
	}
}

func TestNativeCLITokenProviderSlotKeepsDirectValue(t *testing.T) {
	req := AgentRunRequest{CLIAdapter: "CODEX_CLI", Credentials: []Credential{{ID: "fixture", Type: "CLI_TOKEN", EnvVarName: "OPENAI_API_KEY", PlainValue: "synthetic-direct-key"}}}
	env, err := appendCredentialFileEnv(BuildEnvVarsSidecar(req, true), req.Credentials, "/secrets/fixture/run", true)
	if err != nil {
		t.Fatal(err)
	}
	got := envMap(t, env)
	if got["OPENAI_API_KEY"] != "synthetic-direct-key" {
		t.Fatal("native alias replaced direct provider token")
	}
	assignments := 0
	for _, entry := range env {
		if strings.HasPrefix(entry, "OPENAI_API_KEY=") {
			assignments++
		}
	}
	if assignments != 1 {
		t.Fatal("ambiguous duplicate provider token assignments")
	}
}

func TestNativeFileSelectorsAndExplicitMCP(t *testing.T) {
	req := AgentRunRequest{AgentSlug: "fixture", RunID: "run-one", CLIAdapter: "OPENCODE", Credentials: []Credential{
		{ID: "generic", Type: "GENERIC_SECRET", EnvVarName: "NATIVE", PlainValue: "literal-$HOME-${TOKEN}"},
		{ID: "oauth-shaped", Type: "GENERIC_SECRET", EnvVarName: "OTHER", PlainValue: "sk-ant-oat-synthetic-material"},
		{ID: "gated", Type: "SECRET", EnvVarName: "GATED", PlainValue: "sk-ant-oat-keeper-withheld"},
	}, CrewMCPConfigJSON: `{"mcpServers":{"fixture":{"command":"fixture","env":{"TOKEN":"${NATIVE}"}}}}`}
	env := BuildEnvVarsSidecar(req, true)
	if envHasAssignment(env, "CLAUDE_CODE_OAUTH_TOKEN", req.Credentials[1].PlainValue) || envHasAssignment(env, "CLAUDE_CODE_OAUTH_TOKEN", req.Credentials[2].PlainValue) {
		t.Fatal("value shape bypassed delivery policy")
	}
	env = injectMCPCredentialEnvVars(req, env, true, nil)
	dir := agentSecretsDir(req.AgentSlug, req.RunID)
	env, err := appendCredentialFileEnv(env, req.Credentials, dir, true, collectMCPEnvRefs(req.CrewMCPConfigJSON))
	if err != nil {
		t.Fatal(err)
	}
	got := envMap(t, env)
	if got["NATIVE"] != "literal-$HOME-${TOKEN}" || got["NATIVE_PATH"] != dir+"/NATIVE" {
		t.Fatal("explicit MCP literal or native path changed")
	}
	if envHasName(BuildEnvVars(req, nil, true), "GATED") {
		t.Fatal("non-sidecar path bypassed Keeper")
	}
	req.Credentials = append(req.Credentials, Credential{ID: "collision", Type: "GENERIC_SECRET", EnvVarName: "NATIVE_PATH", PlainValue: "other"})
	env = injectMCPCredentialEnvVars(req, BuildEnvVarsSidecar(req, true), true, nil)
	if _, err := appendCredentialFileEnv(env, req.Credentials, dir, true, collectMCPEnvRefs(req.CrewMCPConfigJSON)); err == nil {
		t.Fatal("MCP extra alias collision accepted")
	}
}
