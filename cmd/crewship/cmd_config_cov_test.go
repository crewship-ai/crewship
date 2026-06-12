package main

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

func TestConfigShowRunE_PopulatedConfig(t *testing.T) {
	covSetupCli10(t, "http://127.0.0.1:0")
	if err := cli.SaveConfig(&cli.CLIConfig{
		Server:       "http://dev1.local:8080",
		Workspace:    "main",
		Format:       "json",
		DefaultAgent: "viktor",
		Markdown:     "on",
		Token:        "crewship_cli_0123456789abcdef_tail",
	}); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	out, err := captureStdoutCovCli10(t, func() error {
		return configShowCmd.RunE(configShowCmd, nil)
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	for _, want := range []string{"http://dev1.local:8080", "main", "json", "viktor", "on"} {
		if !strings.Contains(out, want) {
			t.Errorf("show missing %q:\n%s", want, out)
		}
	}
	// Token is masked: first 20 chars + last 4, never the middle.
	if !strings.Contains(out, "crewship_cli_0123456...tail") {
		t.Errorf("token mask wrong:\n%s", out)
	}
}

func TestConfigShowRunE_EmptyTokenBranch(t *testing.T) {
	covSetupCli10(t, "http://127.0.0.1:0")
	if err := cli.SaveConfig(&cli.CLIConfig{}); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	out, err := captureStdoutCovCli10(t, func() error {
		return configShowCmd.RunE(configShowCmd, nil)
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "(not set)") || !strings.Contains(out, "(default: table)") {
		t.Errorf("placeholder defaults missing:\n%s", out)
	}
}

func TestConfigSetRunE_MarkdownAndDefaultAgent(t *testing.T) {
	covSetupCli10(t, "http://127.0.0.1:0")

	if _, err := captureStderrCov(t, func() error {
		return configSetCmd.RunE(configSetCmd, []string{"markdown", "off"})
	}); err != nil {
		t.Fatalf("set markdown: %v", err)
	}
	if _, err := captureStderrCov(t, func() error {
		return configSetCmd.RunE(configSetCmd, []string{"default_agent", "eva"})
	}); err != nil {
		t.Fatalf("set default_agent: %v", err)
	}
	cfg, err := cli.LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Markdown != "off" || cfg.DefaultAgent != "eva" {
		t.Errorf("persisted cfg wrong: %+v", cfg)
	}
}

func TestConfigSetRunE_RejectsInvalidMarkdown(t *testing.T) {
	covSetupCli10(t, "http://127.0.0.1:0")
	err := configSetCmd.RunE(configSetCmd, []string{"markdown", "sometimes"})
	if err == nil || !strings.Contains(err.Error(), "invalid markdown") {
		t.Errorf("expected markdown validation error, got %v", err)
	}
}

func TestConfigSetRunE_PersistsWorkspace(t *testing.T) {
	covSetupCli10(t, "http://127.0.0.1:0")
	if _, err := captureStderrCov(t, func() error {
		return configSetCmd.RunE(configSetCmd, []string{"workspace", "dev-ws"})
	}); err != nil {
		t.Fatalf("set workspace: %v", err)
	}
	cfg, _ := cli.LoadConfig()
	if cfg.Workspace != "dev-ws" {
		t.Errorf("workspace not persisted: %+v", cfg)
	}
}

func TestConfigValidateRunE_FailsWithoutToken(t *testing.T) {
	covSetupCli10(t, "http://127.0.0.1:0")
	if err := cli.SaveConfig(&cli.CLIConfig{}); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	out, err := captureStdoutCovCli10(t, func() error {
		return configValidateCmd.RunE(configValidateCmd, nil)
	})
	if err == nil || !strings.Contains(err.Error(), "config validation failed") {
		t.Errorf("expected validation failure, got %v", err)
	}
	if !strings.Contains(out, "token present") || !strings.Contains(out, "crewship login") {
		t.Errorf("check output missing:\n%s", out)
	}
}

func TestConfigValidateRunE_HappyPathWithServer(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet("/api/v1/auth/cli-token/validate", clitest.JSONResponse(200, map[string]any{"valid": true}))
	s.OnGet("/api/v1/agents", clitest.JSONResponse(200, []map[string]string{
		{"id": "cagent7890abcdefghijklm", "slug": "viktor"},
	}))
	covSetupCli10(t, s.URL())
	if err := cli.SaveConfig(&cli.CLIConfig{
		Token: "tok", Server: s.URL(), Workspace: covWorkspaceIDCli10, DefaultAgent: "viktor",
	}); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	// newAPIClient resolves from the in-memory cliCfg; keep both in sync.
	cliCfg = &cli.CLIConfig{Token: "tok", Server: s.URL(), Workspace: covWorkspaceIDCli10, DefaultAgent: "viktor"}

	out, err := captureStdoutCovCli10(t, func() error {
		return configValidateCmd.RunE(configValidateCmd, nil)
	})
	if err != nil {
		t.Fatalf("RunE: %v\n%s", err, out)
	}
	if !strings.Contains(out, "token validates against server") {
		t.Errorf("token check missing:\n%s", out)
	}
	if !strings.Contains(out, `default-agent "viktor" exists`) {
		t.Errorf("default-agent check missing:\n%s", out)
	}
	if !strings.Contains(out, "0 error(s)") {
		t.Errorf("expected zero errors:\n%s", out)
	}
}

func TestConfigValidateRunE_WarnsOnMissingDefaultAgent(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet("/api/v1/auth/cli-token/validate", clitest.JSONResponse(200, map[string]any{"valid": true}))
	s.OnGet("/api/v1/agents", clitest.JSONResponse(200, []map[string]string{}))
	covSetupCli10(t, s.URL())
	if err := cli.SaveConfig(&cli.CLIConfig{
		Token: "tok", Server: s.URL(), Workspace: covWorkspaceIDCli10, DefaultAgent: "ghost",
	}); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	cliCfg = &cli.CLIConfig{Token: "tok", Server: s.URL(), Workspace: covWorkspaceIDCli10, DefaultAgent: "ghost"}

	out, err := captureStdoutCovCli10(t, func() error {
		return configValidateCmd.RunE(configValidateCmd, nil)
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "1 warning(s)") {
		t.Errorf("missing default-agent should warn, not error:\n%s", out)
	}
}
