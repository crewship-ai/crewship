package main

// Error-branch mop-up for cmd_mcp.go: auth gates, API/decode/transport
// failures for the audit + registry commands, and the remaining crew/agent
// `mcp` branches (agent set-file, agent GET pretty-print, resolved-mode crew
// fetch failures). Helpers in cmd_skill_cov_test.go / cov2.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
	"github.com/spf13/cobra"
)

func TestMcpCmdAuthGatesCov2(t *testing.T) {
	cases := []struct {
		name           string
		cmd            *cobra.Command
		args           []string
		needsWorkspace bool
	}{
		{"audit list", mcpAuditListCmd, nil, true},
		{"registry list", mcpRegistryListCmd, nil, false}, // workspace-agnostic
		{"registry search", mcpRegistrySearchCmd, []string{"q"}, false},
		{"registry sync", mcpRegistrySyncCmd, nil, true},
		{"crew mcp", crewMCPCmd, []string{"engineering"}, true},
		{"agent mcp", agentMCPCmd, []string{"viktor"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			covAuthGates(t, tc.cmd, tc.args, tc.needsWorkspace)
		})
	}
}

func TestMcpAuditListRunECov2_ErrorBranches(t *testing.T) {
	t.Run("transport error", func(t *testing.T) {
		covSetupDead(t)
		if err := mcpAuditListCmd.RunE(mcpAuditListCmd, nil); err == nil {
			t.Error("want transport error; got nil")
		}
	})
	t.Run("api error", func(t *testing.T) {
		s := covSetup(t)
		s.OnGet("/api/v1/mcp-tool-calls", clitest.ErrorResponse(500, "audit broke"))
		err := mcpAuditListCmd.RunE(mcpAuditListCmd, nil)
		if err == nil || !strings.Contains(err.Error(), "audit broke") {
			t.Errorf("want API error; got %v", err)
		}
	})
	t.Run("bad json", func(t *testing.T) {
		s := covSetup(t)
		s.OnGet("/api/v1/mcp-tool-calls", clitest.TextResponse(200, "\x00"))
		if err := mcpAuditListCmd.RunE(mcpAuditListCmd, nil); err == nil {
			t.Error("want decode error; got nil")
		}
	})
}

func TestMcpRegistryListRunECov2_ErrorBranches(t *testing.T) {
	t.Run("transport error", func(t *testing.T) {
		covSetupDead(t)
		if err := mcpRegistryListCmd.RunE(mcpRegistryListCmd, nil); err == nil {
			t.Error("want transport error; got nil")
		}
	})
	t.Run("api error", func(t *testing.T) {
		s := covSetup(t)
		s.OnGet("/api/v1/mcp-registry", clitest.ErrorResponse(500, "registry broke"))
		err := mcpRegistryListCmd.RunE(mcpRegistryListCmd, nil)
		if err == nil || !strings.Contains(err.Error(), "registry broke") {
			t.Errorf("want API error; got %v", err)
		}
	})
	t.Run("bad json", func(t *testing.T) {
		s := covSetup(t)
		s.OnGet("/api/v1/mcp-registry", clitest.TextResponse(200, "nope"))
		if err := mcpRegistryListCmd.RunE(mcpRegistryListCmd, nil); err == nil {
			t.Error("want decode error; got nil")
		}
	})
}

func TestMcpRegistrySearchRunECov2_ErrorBranches(t *testing.T) {
	t.Run("transport error", func(t *testing.T) {
		covSetupDead(t)
		if err := mcpRegistrySearchCmd.RunE(mcpRegistrySearchCmd, []string{"x"}); err == nil {
			t.Error("want transport error; got nil")
		}
	})
	t.Run("api error", func(t *testing.T) {
		s := covSetup(t)
		s.OnGet("/api/v1/mcp-registry/search", clitest.ErrorResponse(500, "search broke"))
		err := mcpRegistrySearchCmd.RunE(mcpRegistrySearchCmd, []string{"x"})
		if err == nil || !strings.Contains(err.Error(), "search broke") {
			t.Errorf("want API error; got %v", err)
		}
	})
	t.Run("bad json", func(t *testing.T) {
		s := covSetup(t)
		s.OnGet("/api/v1/mcp-registry/search", clitest.TextResponse(200, "\x00"))
		if err := mcpRegistrySearchCmd.RunE(mcpRegistrySearchCmd, []string{"x"}); err == nil {
			t.Error("want decode error; got nil")
		}
	})
}

func TestMcpRegistrySyncRunECov2_ErrorBranches(t *testing.T) {
	t.Run("transport error", func(t *testing.T) {
		covSetupDead(t)
		if err := mcpRegistrySyncCmd.RunE(mcpRegistrySyncCmd, nil); err == nil {
			t.Error("want transport error; got nil")
		}
	})
	t.Run("api error 429 cooldown", func(t *testing.T) {
		s := covSetup(t)
		s.OnPost("/api/v1/mcp-registry/sync", clitest.ErrorResponse(429, "synced recently"))
		err := mcpRegistrySyncCmd.RunE(mcpRegistrySyncCmd, nil)
		if err == nil || !strings.Contains(err.Error(), "synced recently") {
			t.Errorf("want cooldown error; got %v", err)
		}
	})
	t.Run("bad json", func(t *testing.T) {
		s := covSetup(t)
		s.OnPost("/api/v1/mcp-registry/sync", clitest.TextResponse(200, "nope"))
		if err := mcpRegistrySyncCmd.RunE(mcpRegistrySyncCmd, nil); err == nil {
			t.Error("want decode error; got nil")
		}
	})
}

// ─── crew mcp remaining branches ─────────────────────────────────────────

func TestCrewMCPRunECov2_ErrorBranches(t *testing.T) {
	t.Run("crew resolve fails", func(t *testing.T) {
		s := covSetup(t)
		s.OnGet("/api/v1/crews", clitest.ErrorResponse(500, "crews broke"))
		err := crewMCPCmd.RunE(crewMCPCmd, []string{"engineering"})
		if err == nil || !strings.Contains(err.Error(), "crews broke") {
			t.Errorf("want resolve error; got %v", err)
		}
	})
	t.Run("patch fails", func(t *testing.T) {
		s := covSetup(t)
		covStubCrew(s)
		s.OnPatch("/api/v1/crews/"+covCrewID, clitest.ErrorResponse(500, "patch broke"))
		covSetFlag(t, crewMCPCmd, "set", `{"mcpServers":{}}`)
		err := crewMCPCmd.RunE(crewMCPCmd, []string{"engineering"})
		if err == nil || !strings.Contains(err.Error(), "patch broke") {
			t.Errorf("want patch error; got %v", err)
		}
	})
	t.Run("get crew fails", func(t *testing.T) {
		s := covSetup(t)
		covStubCrew(s)
		s.OnGet("/api/v1/crews/"+covCrewID, clitest.ErrorResponse(500, "get broke"))
		err := crewMCPCmd.RunE(crewMCPCmd, []string{"engineering"})
		if err == nil || !strings.Contains(err.Error(), "get broke") {
			t.Errorf("want get error; got %v", err)
		}
	})
	t.Run("get crew bad json", func(t *testing.T) {
		s := covSetup(t)
		covStubCrew(s)
		s.OnGet("/api/v1/crews/"+covCrewID, clitest.TextResponse(200, "nope"))
		if err := crewMCPCmd.RunE(crewMCPCmd, []string{"engineering"}); err == nil {
			t.Error("want decode error; got nil")
		}
	})
}

// ─── agent mcp remaining branches ────────────────────────────────────────

func TestAgentMCPRunECov2_SetFile(t *testing.T) {
	s := covSetup(t)
	covStubAgentForMCP(s, map[string]any{})
	s.OnPatch("/api/v1/agents/"+covAgentIDCli1, clitest.JSONResponse(200, map[string]string{"id": covAgentIDCli1}))
	path := filepath.Join(t.TempDir(), "agent-mcp.json")
	if err := os.WriteFile(path, []byte(`{"mcpServers":{"jira":{"type":"http"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	covSetFlag(t, agentMCPCmd, "set-file", path)

	out, err := covCaptureStdout(t, func() error {
		return agentMCPCmd.RunE(agentMCPCmd, []string{"viktor"})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "Agent viktor: MCP config set (1 servers).") {
		t.Errorf("output = %q", out)
	}
}

func TestAgentMCPRunECov2_SetFileMissingAndConflicts(t *testing.T) {
	t.Run("set-file missing", func(t *testing.T) {
		s := covSetup(t)
		covStubAgentForMCP(s, map[string]any{})
		covSetFlag(t, agentMCPCmd, "set-file", filepath.Join(t.TempDir(), "ghost.json"))
		err := agentMCPCmd.RunE(agentMCPCmd, []string{"viktor"})
		if err == nil || !strings.Contains(err.Error(), "read file") {
			t.Errorf("want read error; got %v", err)
		}
	})
	t.Run("set and set-file conflict", func(t *testing.T) {
		s := covSetup(t)
		covStubAgentForMCP(s, map[string]any{})
		covSetFlag(t, agentMCPCmd, "set", `{"mcpServers":{}}`)
		covSetFlag(t, agentMCPCmd, "set-file", "x.json")
		err := agentMCPCmd.RunE(agentMCPCmd, []string{"viktor"})
		if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
			t.Errorf("want conflict error; got %v", err)
		}
	})
	t.Run("set invalid json", func(t *testing.T) {
		s := covSetup(t)
		covStubAgentForMCP(s, map[string]any{})
		covSetFlag(t, agentMCPCmd, "set", "{broken")
		err := agentMCPCmd.RunE(agentMCPCmd, []string{"viktor"})
		if err == nil || !strings.Contains(err.Error(), "invalid MCP JSON") {
			t.Errorf("want validation error; got %v", err)
		}
	})
	t.Run("set empty clears", func(t *testing.T) {
		s := covSetup(t)
		covStubAgentForMCP(s, map[string]any{})
		s.OnPatch("/api/v1/agents/"+covAgentIDCli1, clitest.JSONResponse(200, map[string]string{"id": covAgentIDCli1}))
		covSetFlag(t, agentMCPCmd, "set", "")
		out, err := covCaptureStdout(t, func() error {
			return agentMCPCmd.RunE(agentMCPCmd, []string{"viktor"})
		})
		if err != nil {
			t.Fatalf("RunE: %v", err)
		}
		if !strings.Contains(out, "Agent viktor: MCP config cleared.") {
			t.Errorf("output = %q", out)
		}
		body := covJSONBody(t, s.CallsFor("PATCH", "/api/v1/agents/"+covAgentIDCli1)[0].Body)
		if body["mcp_config_json"] != nil {
			t.Errorf("clear must send null; got %v", body["mcp_config_json"])
		}
	})
	t.Run("patch fails", func(t *testing.T) {
		s := covSetup(t)
		covStubAgentForMCP(s, map[string]any{})
		s.OnPatch("/api/v1/agents/"+covAgentIDCli1, clitest.ErrorResponse(500, "patch broke"))
		covSetFlag(t, agentMCPCmd, "set", `{"mcpServers":{}}`)
		err := agentMCPCmd.RunE(agentMCPCmd, []string{"viktor"})
		if err == nil || !strings.Contains(err.Error(), "patch broke") {
			t.Errorf("want patch error; got %v", err)
		}
	})
}

func TestAgentMCPRunECov2_ResolvedCrewFetchFailures(t *testing.T) {
	agentBody := map[string]any{"crew_id": covCrewID, "mcp_config_json": nil}

	t.Run("crew api error", func(t *testing.T) {
		s := covSetup(t)
		covStubAgentForMCP(s, agentBody)
		s.OnGet("/api/v1/crews/"+covCrewID, clitest.ErrorResponse(500, "crew broke"))
		covSetFlag(t, agentMCPCmd, "resolved", "true")
		err := agentMCPCmd.RunE(agentMCPCmd, []string{"viktor"})
		if err == nil || !strings.Contains(err.Error(), "crew broke") {
			t.Errorf("want crew error; got %v", err)
		}
	})
	t.Run("crew bad json", func(t *testing.T) {
		s := covSetup(t)
		covStubAgentForMCP(s, agentBody)
		s.OnGet("/api/v1/crews/"+covCrewID, clitest.TextResponse(200, "nope"))
		covSetFlag(t, agentMCPCmd, "resolved", "true")
		err := agentMCPCmd.RunE(agentMCPCmd, []string{"viktor"})
		if err == nil || !strings.Contains(err.Error(), "read crew response") {
			t.Errorf("want crew decode error; got %v", err)
		}
	})
	t.Run("crew config malformed", func(t *testing.T) {
		s := covSetup(t)
		covStubAgentForMCP(s, agentBody)
		s.OnGet("/api/v1/crews/"+covCrewID, clitest.JSONResponse(200, map[string]any{
			"mcp_config_json": "{broken",
		}))
		covSetFlag(t, agentMCPCmd, "resolved", "true")
		err := agentMCPCmd.RunE(agentMCPCmd, []string{"viktor"})
		if err == nil || !strings.Contains(err.Error(), "parse crew MCP config") {
			t.Errorf("want crew parse error; got %v", err)
		}
	})
	t.Run("crew-only servers survive", func(t *testing.T) {
		s := covSetup(t)
		covStubAgentForMCP(s, agentBody)
		s.OnGet("/api/v1/crews/"+covCrewID, clitest.JSONResponse(200, map[string]any{
			"mcp_config_json": `{"mcpServers":{"sentry":{"type":"http"}}}`,
		}))
		covSetFlag(t, agentMCPCmd, "resolved", "true")
		out, err := covCaptureStdout(t, func() error {
			return agentMCPCmd.RunE(agentMCPCmd, []string{"viktor"})
		})
		if err != nil {
			t.Fatalf("RunE: %v", err)
		}
		if !strings.Contains(out, "sentry") {
			t.Errorf("crew-only server missing from merge:\n%s", out)
		}
	})
}

func TestAgentMCPRunECov2_GetPrettyAndMalformed(t *testing.T) {
	t.Run("pretty prints valid config", func(t *testing.T) {
		s := covSetup(t)
		covStubAgentForMCP(s, map[string]any{
			"mcp_config_json": `{"mcpServers":{"jira":{"type":"http"}}}`,
		})
		out, err := covCaptureStdout(t, func() error {
			return agentMCPCmd.RunE(agentMCPCmd, []string{"viktor"})
		})
		if err != nil {
			t.Fatalf("RunE: %v", err)
		}
		if !strings.Contains(out, `"jira"`) || !strings.Contains(out, "\n  ") {
			t.Errorf("want pretty JSON; got %q", out)
		}
	})
	t.Run("malformed prints raw", func(t *testing.T) {
		s := covSetup(t)
		covStubAgentForMCP(s, map[string]any{"mcp_config_json": "{raw-broken"})
		out, err := covCaptureStdout(t, func() error {
			return agentMCPCmd.RunE(agentMCPCmd, []string{"viktor"})
		})
		if err != nil {
			t.Fatalf("RunE: %v", err)
		}
		if !strings.Contains(out, "{raw-broken") {
			t.Errorf("want raw passthrough; got %q", out)
		}
	})
	t.Run("agent resolve fails", func(t *testing.T) {
		s := covSetup(t)
		s.OnGet("/api/v1/agents", clitest.JSONResponse(200, []map[string]string{}))
		err := agentMCPCmd.RunE(agentMCPCmd, []string{"ghost"})
		if err == nil || !strings.Contains(err.Error(), "agent not found") {
			t.Errorf("want resolve error; got %v", err)
		}
	})
	t.Run("agent get bad json", func(t *testing.T) {
		s := covSetup(t)
		s.OnGet("/api/v1/agents", clitest.JSONResponse(200, []map[string]string{
			{"id": covAgentIDCli1, "slug": "viktor"},
		}))
		s.OnGet("/api/v1/agents/"+covAgentIDCli1, clitest.TextResponse(200, "nope"))
		if err := agentMCPCmd.RunE(agentMCPCmd, []string{"viktor"}); err == nil {
			t.Error("want decode error; got nil")
		}
	})
}

// Guard: the registry commands must clear the workspace header (they are
// workspace-agnostic on the server). A regression that reintroduces the
// wsCtx lookup would 404 for users without a default workspace.
func TestMcpRegistryListCov2_NoWorkspaceHeader(t *testing.T) {
	s := covSetup(t)
	cliCfg = &cli.CLIConfig{Token: "tok", Server: s.URL()} // no workspace at all
	s.OnGet("/api/v1/mcp-registry", clitest.JSONResponse(200, map[string]any{
		"servers": []map[string]string{}, "total": 0,
	}))
	if _, err := covCaptureStdout(t, func() error {
		return mcpRegistryListCmd.RunE(mcpRegistryListCmd, nil)
	}); err != nil {
		t.Fatalf("RunE without workspace: %v", err)
	}
}
