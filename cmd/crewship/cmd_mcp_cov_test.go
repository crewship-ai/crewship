package main

// Coverage tests for cmd_mcp.go — MCP JSON validation, the audit/registry
// commands, and the crew/agent `mcp` get/set/resolved surfaces. Serial;
// shared cov* helpers live in cmd_skill_cov_test.go.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

// ─── validateAndNormalizeMCPJSON ─────────────────────────────────────────

func TestValidateAndNormalizeMCPJSONCov(t *testing.T) {
	t.Run("empty passthrough", func(t *testing.T) {
		got, n, err := validateAndNormalizeMCPJSON("")
		if got != "" || n != 0 || err != nil {
			t.Errorf("got (%q,%d,%v), want (\"\",0,nil)", got, n, err)
		}
	})
	t.Run("invalid json", func(t *testing.T) {
		_, _, err := validateAndNormalizeMCPJSON("{nope")
		if err == nil || !strings.Contains(err.Error(), "invalid MCP JSON") {
			t.Errorf("want invalid-JSON error; got %v", err)
		}
	})
	t.Run("missing mcpServers key", func(t *testing.T) {
		_, _, err := validateAndNormalizeMCPJSON(`{"servers":{}}`)
		if err == nil || !strings.Contains(err.Error(), `"mcpServers"`) {
			t.Errorf("want mcpServers-required error; got %v", err)
		}
	})
	t.Run("valid two servers compacted", func(t *testing.T) {
		in := `{
			"mcpServers": {
				"github": {"command": "npx"},
				"jira":   {"type": "http"}
			}
		}`
		got, n, err := validateAndNormalizeMCPJSON(in)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if n != 2 {
			t.Errorf("server count = %d, want 2", n)
		}
		if strings.ContainsAny(got, "\n\t") {
			t.Errorf("output not compact: %q", got)
		}
		if !strings.Contains(got, `"mcpServers"`) || !strings.Contains(got, `"github"`) {
			t.Errorf("normalized output missing keys: %q", got)
		}
	})
}

// ─── mcp audit list ──────────────────────────────────────────────────────

func TestMcpAuditListRunECov(t *testing.T) {
	s := covSetup(t)
	s.OnGet("/api/v1/mcp-tool-calls", clitest.JSONResponse(200, []map[string]any{
		{"id": "call-1", "tool": "search"},
	}))
	covSetFlag(t, mcpAuditListCmd, "limit", "7")
	covSetFlag(t, mcpAuditListCmd, "since", "2026-06-01T00:00:00Z")

	out, err := covCaptureStdout(t, func() error {
		return mcpAuditListCmd.RunE(mcpAuditListCmd, nil)
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "call-1") {
		t.Errorf("output missing audit row: %q", out)
	}
	calls := s.CallsFor("GET", "/api/v1/mcp-tool-calls")
	if len(calls) != 1 {
		t.Fatalf("want 1 GET, got %d", len(calls))
	}
	q := calls[0].Query
	if !strings.Contains(q, "limit=7") || !strings.Contains(q, "since=2026-06-01T00%3A00%3A00Z") {
		t.Errorf("query = %q, want limit + since", q)
	}
}

// ─── mcp registry list / search / sync ───────────────────────────────────

func TestMcpRegistryListRunECov(t *testing.T) {
	s := covSetup(t)
	s.OnGet("/api/v1/mcp-registry", clitest.JSONResponse(200, map[string]any{
		"servers": []map[string]any{
			{"name": "github", "display_name": "GitHub", "category": "dev",
				"transport": "stdio", "trust_tier": "anthropic",
				"is_featured": true, "package_name": "@mcp/github"},
		},
		"total": 1,
	}))
	covSetFlag(t, mcpRegistryListCmd, "limit", "10")
	covSetFlag(t, mcpRegistryListCmd, "trust-tier", "anthropic")
	covSetFlag(t, mcpRegistryListCmd, "featured", "true")

	out, err := covCaptureStdout(t, func() error {
		return mcpRegistryListCmd.RunE(mcpRegistryListCmd, nil)
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "@mcp/github") {
		t.Errorf("table missing package: %q", out)
	}
	call := s.CallsFor("GET", "/api/v1/mcp-registry")[0]
	for _, want := range []string{"limit=10", "trust_tier=anthropic", "featured=true"} {
		if !strings.Contains(call.Query, want) {
			t.Errorf("query %q missing %q", call.Query, want)
		}
	}
}

func TestMcpRegistrySearchRunECov(t *testing.T) {
	s := covSetup(t)
	s.OnGet("/api/v1/mcp-registry/search", clitest.JSONResponse(200, map[string]any{
		"servers": []map[string]string{{"name": "slack"}},
	}))
	covSetFlag(t, mcpRegistrySearchCmd, "limit", "5")
	covSetFlag(t, mcpRegistrySearchCmd, "trust-tier", "community")

	out, err := covCaptureStdout(t, func() error {
		return mcpRegistrySearchCmd.RunE(mcpRegistrySearchCmd, []string{"slack"})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "slack") {
		t.Errorf("output missing result: %q", out)
	}
	q := s.CallsFor("GET", "/api/v1/mcp-registry/search")[0].Query
	for _, want := range []string{"q=slack", "limit=5", "trust_tier=community"} {
		if !strings.Contains(q, want) {
			t.Errorf("query %q missing %q", q, want)
		}
	}
}

func TestMcpRegistrySyncRunECov(t *testing.T) {
	s := covSetup(t)
	s.OnPost("/api/v1/mcp-registry/sync", clitest.JSONResponse(200, map[string]string{
		"status": "ok", "message": "synced 120 servers",
	}))
	if _, err := covCaptureStdout(t, func() error {
		return mcpRegistrySyncCmd.RunE(mcpRegistrySyncCmd, nil)
	}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if n := len(s.CallsFor("POST", "/api/v1/mcp-registry/sync")); n != 1 {
		t.Errorf("sync POST calls = %d, want 1", n)
	}

	// Empty message falls back to the generic success line.
	s.OnPost("/api/v1/mcp-registry/sync", clitest.JSONResponse(200, map[string]string{"status": "ok"}))
	if _, err := covCaptureStdout(t, func() error {
		return mcpRegistrySyncCmd.RunE(mcpRegistrySyncCmd, nil)
	}); err != nil {
		t.Fatalf("RunE (no message): %v", err)
	}
}

// ─── crew mcp ────────────────────────────────────────────────────────────

func covStubCrew(s *clitest.StubServer) {
	s.OnGet("/api/v1/crews", clitest.JSONResponse(200, []map[string]string{
		{"id": covCrewID, "slug": "engineering"},
	}))
}

func TestCrewMCPRunECov_GetEmpty(t *testing.T) {
	s := covSetup(t)
	covStubCrew(s)
	s.OnGet("/api/v1/crews/"+covCrewID, clitest.JSONResponse(200, map[string]any{
		"mcp_config_json": nil,
	}))
	out, err := covCaptureStdout(t, func() error {
		return crewMCPCmd.RunE(crewMCPCmd, []string{"engineering"})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "Crew engineering: no MCP config set.") {
		t.Errorf("output = %q", out)
	}
}

func TestCrewMCPRunECov_GetPrettyPrints(t *testing.T) {
	s := covSetup(t)
	covStubCrew(s)
	cfg := `{"mcpServers":{"github":{"command":"npx"}}}`
	s.OnGet("/api/v1/crews/"+covCrewID, clitest.JSONResponse(200, map[string]any{
		"mcp_config_json": cfg,
	}))
	out, err := covCaptureStdout(t, func() error {
		return crewMCPCmd.RunE(crewMCPCmd, []string{"engineering"})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, `"github"`) || !strings.Contains(out, "\n  ") {
		t.Errorf("want pretty-printed JSON; got %q", out)
	}
}

func TestCrewMCPRunECov_GetMalformedPrintsRaw(t *testing.T) {
	s := covSetup(t)
	covStubCrew(s)
	s.OnGet("/api/v1/crews/"+covCrewID, clitest.JSONResponse(200, map[string]any{
		"mcp_config_json": "{broken",
	}))
	out, err := covCaptureStdout(t, func() error {
		return crewMCPCmd.RunE(crewMCPCmd, []string{"engineering"})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "{broken") {
		t.Errorf("malformed config must print raw; got %q", out)
	}
}

func TestCrewMCPRunECov_SetInline(t *testing.T) {
	s := covSetup(t)
	covStubCrew(s)
	s.OnPatch("/api/v1/crews/"+covCrewID, clitest.JSONResponse(200, map[string]string{"id": covCrewID}))
	covSetFlag(t, crewMCPCmd, "set", `{"mcpServers":{"github":{"command":"npx"},"jira":{"type":"http"}}}`)

	out, err := covCaptureStdout(t, func() error {
		return crewMCPCmd.RunE(crewMCPCmd, []string{"engineering"})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "Crew engineering: MCP config set (2 servers).") {
		t.Errorf("output = %q", out)
	}
	body := covJSONBody(t, s.CallsFor("PATCH", "/api/v1/crews/"+covCrewID)[0].Body)
	val, _ := body["mcp_config_json"].(string)
	if !strings.Contains(val, `"github"`) || !strings.Contains(val, `"jira"`) {
		t.Errorf("PATCH payload missing normalized config: %v", body)
	}
}

func TestCrewMCPRunECov_SetEmptyClears(t *testing.T) {
	s := covSetup(t)
	covStubCrew(s)
	s.OnPatch("/api/v1/crews/"+covCrewID, clitest.JSONResponse(200, map[string]string{"id": covCrewID}))
	covSetFlag(t, crewMCPCmd, "set", "")

	out, err := covCaptureStdout(t, func() error {
		return crewMCPCmd.RunE(crewMCPCmd, []string{"engineering"})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "Crew engineering: MCP config cleared.") {
		t.Errorf("output = %q", out)
	}
	body := covJSONBody(t, s.CallsFor("PATCH", "/api/v1/crews/"+covCrewID)[0].Body)
	if body["mcp_config_json"] != nil {
		t.Errorf("clear must send null; got %v", body["mcp_config_json"])
	}
}

func TestCrewMCPRunECov_SetAndSetFileConflict(t *testing.T) {
	s := covSetup(t)
	covStubCrew(s)
	covSetFlag(t, crewMCPCmd, "set", `{"mcpServers":{}}`)
	covSetFlag(t, crewMCPCmd, "set-file", "whatever.json")
	err := crewMCPCmd.RunE(crewMCPCmd, []string{"engineering"})
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("want mutual-exclusion error; got %v", err)
	}
}

func TestCrewMCPRunECov_SetFile(t *testing.T) {
	s := covSetup(t)
	covStubCrew(s)
	s.OnPatch("/api/v1/crews/"+covCrewID, clitest.JSONResponse(200, map[string]string{"id": covCrewID}))
	path := filepath.Join(t.TempDir(), "mcp.json")
	if err := os.WriteFile(path, []byte(`{"mcpServers":{"sentry":{"type":"http"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	covSetFlag(t, crewMCPCmd, "set-file", path)

	out, err := covCaptureStdout(t, func() error {
		return crewMCPCmd.RunE(crewMCPCmd, []string{"engineering"})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "MCP config set (1 servers).") {
		t.Errorf("output = %q", out)
	}
}

func TestCrewMCPRunECov_SetFileMissing(t *testing.T) {
	s := covSetup(t)
	covStubCrew(s)
	covSetFlag(t, crewMCPCmd, "set-file", filepath.Join(t.TempDir(), "ghost.json"))
	err := crewMCPCmd.RunE(crewMCPCmd, []string{"engineering"})
	if err == nil || !strings.Contains(err.Error(), "read file") {
		t.Errorf("want read-file error; got %v", err)
	}
}

func TestCrewMCPRunECov_SetInvalidJSON(t *testing.T) {
	s := covSetup(t)
	covStubCrew(s)
	covSetFlag(t, crewMCPCmd, "set", "{nope")
	err := crewMCPCmd.RunE(crewMCPCmd, []string{"engineering"})
	if err == nil || !strings.Contains(err.Error(), "invalid MCP JSON") {
		t.Errorf("want validation error; got %v", err)
	}
}

// ─── agent mcp ───────────────────────────────────────────────────────────

func covStubAgentForMCP(s *clitest.StubServer, agentBody map[string]any) {
	s.OnGet("/api/v1/agents", clitest.JSONResponse(200, []map[string]string{
		{"id": covAgentIDCli1, "slug": "viktor"},
	}))
	s.OnGet("/api/v1/agents/"+covAgentIDCli1, clitest.JSONResponse(200, agentBody))
}

func TestAgentMCPRunECov_GetEmpty(t *testing.T) {
	s := covSetup(t)
	covStubAgentForMCP(s, map[string]any{"mcp_config_json": nil})
	out, err := covCaptureStdout(t, func() error {
		return agentMCPCmd.RunE(agentMCPCmd, []string{"viktor"})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "Agent viktor: no agent-specific MCP config.") {
		t.Errorf("output = %q", out)
	}
}

func TestAgentMCPRunECov_ResolvedConflictsWithSet(t *testing.T) {
	s := covSetup(t)
	covStubAgentForMCP(s, map[string]any{})
	covSetFlag(t, agentMCPCmd, "resolved", "true")
	covSetFlag(t, agentMCPCmd, "set", `{"mcpServers":{}}`)
	err := agentMCPCmd.RunE(agentMCPCmd, []string{"viktor"})
	if err == nil || !strings.Contains(err.Error(), "--resolved cannot be combined") {
		t.Errorf("want conflict error; got %v", err)
	}
}

func TestAgentMCPRunECov_SetInline(t *testing.T) {
	s := covSetup(t)
	covStubAgentForMCP(s, map[string]any{})
	s.OnPatch("/api/v1/agents/"+covAgentIDCli1, clitest.JSONResponse(200, map[string]string{"id": covAgentIDCli1}))
	covSetFlag(t, agentMCPCmd, "set", `{"mcpServers":{"jira":{"type":"http"}}}`)

	out, err := covCaptureStdout(t, func() error {
		return agentMCPCmd.RunE(agentMCPCmd, []string{"viktor"})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "Agent viktor: MCP config set (1 servers).") {
		t.Errorf("output = %q", out)
	}
	body := covJSONBody(t, s.CallsFor("PATCH", "/api/v1/agents/"+covAgentIDCli1)[0].Body)
	if val, _ := body["mcp_config_json"].(string); !strings.Contains(val, `"jira"`) {
		t.Errorf("PATCH body wrong: %v", body)
	}
}

func TestAgentMCPRunECov_ResolvedMergesAgentOverCrew(t *testing.T) {
	s := covSetup(t)
	crewID := covCrewID
	crewCfg := `{"mcpServers":{"github":{"command":"crew-version"},"sentry":{"type":"http"}}}`
	agentCfg := `{"mcpServers":{"github":{"command":"agent-version"}}}`
	covStubAgentForMCP(s, map[string]any{
		"crew_id":         crewID,
		"mcp_config_json": agentCfg,
	})
	s.OnGet("/api/v1/crews/"+crewID, clitest.JSONResponse(200, map[string]any{
		"mcp_config_json": crewCfg,
	}))
	covSetFlag(t, agentMCPCmd, "resolved", "true")

	out, err := covCaptureStdout(t, func() error {
		return agentMCPCmd.RunE(agentMCPCmd, []string{"viktor"})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	// Agent entry overrides crew's for "github"; crew-only "sentry" survives.
	if !strings.Contains(out, "agent-version") {
		t.Errorf("merged output must prefer agent config:\n%s", out)
	}
	if strings.Contains(out, "crew-version") {
		t.Errorf("crew github entry must be overridden:\n%s", out)
	}
	if !strings.Contains(out, "sentry") {
		t.Errorf("crew-only server must survive merge:\n%s", out)
	}
}

func TestAgentMCPRunECov_ResolvedBothEmpty(t *testing.T) {
	s := covSetup(t)
	covStubAgentForMCP(s, map[string]any{"crew_id": nil, "mcp_config_json": nil})
	covSetFlag(t, agentMCPCmd, "resolved", "true")
	out, err := covCaptureStdout(t, func() error {
		return agentMCPCmd.RunE(agentMCPCmd, []string{"viktor"})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "Agent viktor: no MCP servers (crew + agent both empty).") {
		t.Errorf("output = %q", out)
	}
}

func TestAgentMCPRunECov_ResolvedMalformedAgentConfig(t *testing.T) {
	s := covSetup(t)
	covStubAgentForMCP(s, map[string]any{"crew_id": nil, "mcp_config_json": "{broken"})
	covSetFlag(t, agentMCPCmd, "resolved", "true")
	err := agentMCPCmd.RunE(agentMCPCmd, []string{"viktor"})
	if err == nil || !strings.Contains(err.Error(), "malformed agent MCP config") {
		t.Errorf("want malformed-config error; got %v", err)
	}
}
