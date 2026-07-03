package main

import (
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
)

// CREWSHIP_TOKEN is the non-interactive auth path (CI, agent containers):
// no login, no config file — the credential rides the environment, wins
// over any stored token, and is NOT host-bound (the operator scoped it to
// this shell deliberately, unlike the persisted config token that the
// issue-#571 guard protects from --server exfiltration).

func TestRequireAuth_EnvTokenSatisfiesAuth(t *testing.T) {
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{} // no stored token
	flagProfile = ""
	t.Setenv("CREWSHIP_PROFILE", "")
	t.Setenv("CREWSHIP_TOKEN", "env-tok-123")

	if err := requireAuth(); err != nil {
		t.Errorf("requireAuth with CREWSHIP_TOKEN = %v, want nil", err)
	}
}

func TestRequireAuth_NoTokenAnywhereFails(t *testing.T) {
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{}
	flagProfile = ""
	t.Setenv("CREWSHIP_PROFILE", "")
	t.Setenv("CREWSHIP_TOKEN", "")

	if err := requireAuth(); err == nil {
		t.Error("requireAuth with no token = nil, want error")
	}
}

func TestNewAPIClient_EnvTokenWinsAndIsNotHostBound(t *testing.T) {
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{
		Server: "https://dev2.example.com",
		Token:  "stored-token",
	}
	t.Setenv("CREWSHIP_TOKEN", "env-tok-456")
	t.Setenv("CREWSHIP_SERVER", "")
	flagServer = ""
	flagProfile = ""
	t.Setenv("CREWSHIP_PROFILE", "")

	c := newAPIClient()
	if c.Token != "env-tok-456" {
		t.Errorf("Token = %q, want env token to win over stored token", c.Token)
	}
	if c.TokenHost != "" {
		t.Errorf("TokenHost = %q, want empty (env token is not host-bound)", c.TokenHost)
	}
}

func TestNewAPIClient_StoredTokenStaysHostBound(t *testing.T) {
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{
		Server: "https://dev2.example.com",
		Token:  "stored-token",
	}
	t.Setenv("CREWSHIP_TOKEN", "")
	t.Setenv("CREWSHIP_SERVER", "")
	flagServer = ""
	flagProfile = ""
	t.Setenv("CREWSHIP_PROFILE", "")

	c := newAPIClient()
	if c.Token != "stored-token" {
		t.Errorf("Token = %q, want stored token", c.Token)
	}
	if c.TokenHost != "dev2.example.com" {
		t.Errorf("TokenHost = %q, want dev2.example.com (config token keeps the #571 guard)", c.TokenHost)
	}
}
