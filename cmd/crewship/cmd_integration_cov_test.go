package main

// Coverage tests for cmd_integration.go — workspace-level integration CRUD,
// enable/disable toggling, and agent bindings. Serial (shared cliCfg/flags);
// helpers live in cmd_skill_cov_test.go.

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

func covStubIntegrationList(s *clitest.StubServer) {
	s.OnGet("/api/v1/integrations", clitest.JSONResponse(200, []map[string]any{
		{"id": covIntgID, "name": "gmail", "display_name": "Google Gmail",
			"transport": "streamable-http",
			"endpoint":  "https://mcp.example.com/really/long/endpoint/path/over/forty",
			"enabled":   true, "agent_binding_count": 2, "crew_server_count": 1},
		{"id": "cintg20123456789abcdefgh", "name": "local", "display_name": "Local Tools",
			"transport": "stdio", "endpoint": "", "enabled": false,
			"agent_binding_count": 0, "crew_server_count": 0},
	}))
}

// ─── toggleIntegration (enable / disable) ────────────────────────────────

func TestToggleIntegrationCov_NoAuth(t *testing.T) {
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{}
	err := toggleIntegration("gmail", true)
	if err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("want not-logged-in; got %v", err)
	}
}

func TestToggleIntegrationCov_NotFound(t *testing.T) {
	s := covSetup(t)
	s.OnGet("/api/v1/integrations", clitest.JSONResponse(200, []map[string]string{}))
	err := toggleIntegration("ghost", true)
	if err == nil || !strings.Contains(err.Error(), `integration "ghost" not found`) {
		t.Errorf("want not-found error; got %v", err)
	}
}

func TestIntgEnableRunECov(t *testing.T) {
	s := covSetup(t)
	covStubIntegrationList(s)
	s.OnPatch("/api/v1/integrations/"+covIntgID, clitest.JSONResponse(200, map[string]any{"id": covIntgID}))

	out, err := covCaptureStdout(t, func() error {
		return intgEnableCmd.RunE(intgEnableCmd, []string{"gmail"})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "Integration gmail enabled.") {
		t.Errorf("output = %q, want enabled confirmation", out)
	}
	calls := s.CallsFor("PATCH", "/api/v1/integrations/"+covIntgID)
	if len(calls) != 1 {
		t.Fatalf("want 1 PATCH, got %d", len(calls))
	}
	if body := covJSONBody(t, calls[0].Body); body["enabled"] != true {
		t.Errorf("enabled = %v, want true", body["enabled"])
	}
}

func TestIntgDisableRunECov(t *testing.T) {
	s := covSetup(t)
	covStubIntegrationList(s)
	s.OnPatch("/api/v1/integrations/"+covIntgID, clitest.JSONResponse(200, map[string]any{"id": covIntgID}))

	out, err := covCaptureStdout(t, func() error {
		return intgDisableCmd.RunE(intgDisableCmd, []string{"gmail"})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "Integration gmail disabled.") {
		t.Errorf("output = %q, want disabled confirmation", out)
	}
	body := covJSONBody(t, s.CallsFor("PATCH", "/api/v1/integrations/"+covIntgID)[0].Body)
	if body["enabled"] != false {
		t.Errorf("enabled = %v, want false", body["enabled"])
	}
}

// ─── intg list ───────────────────────────────────────────────────────────

func TestIntgListRunECov(t *testing.T) {
	s := covSetup(t)
	covStubIntegrationList(s)
	out, err := covCaptureStdout(t, func() error {
		return intgListCmd.RunE(intgListCmd, nil)
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	// Long endpoint must be truncated with an ellipsis, empty endpoint
	// rendered as "-", disabled row as "no".
	for _, want := range []string{"gmail", "...", "no", "stdio"} {
		if !strings.Contains(out, want) {
			t.Errorf("table missing %q:\n%s", want, out)
		}
	}
}

func TestIntgListRunECov_APIError(t *testing.T) {
	s := covSetup(t)
	s.OnGet("/api/v1/integrations", clitest.ErrorResponse(500, "db down"))
	err := intgListCmd.RunE(intgListCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "db down") {
		t.Errorf("want API error surfaced; got %v", err)
	}
}

// ─── intg add ────────────────────────────────────────────────────────────

func TestIntgAddRunECov_RequiresName(t *testing.T) {
	covSetup(t)
	err := intgAddCmd.RunE(intgAddCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "--name is required") {
		t.Errorf("want name-required error; got %v", err)
	}
}

func TestIntgAddRunECov_Happy(t *testing.T) {
	s := covSetup(t)
	s.OnPost("/api/v1/integrations", clitest.JSONResponse(201, map[string]string{
		"id": covIntgID, "name": "gmail",
	}))
	covSetFlag(t, intgAddCmd, "name", "gmail")
	covSetFlag(t, intgAddCmd, "display", "Google Gmail")
	covSetFlag(t, intgAddCmd, "endpoint", "https://mcp.example.com/gmail")
	covSetFlag(t, intgAddCmd, "icon", "mail")

	out, err := covCaptureStdout(t, func() error {
		return intgAddCmd.RunE(intgAddCmd, nil)
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "Integration created: gmail ("+covIntgID+")") {
		t.Errorf("output = %q", out)
	}
	body := covJSONBody(t, s.CallsFor("POST", "/api/v1/integrations")[0].Body)
	if body["name"] != "gmail" || body["display_name"] != "Google Gmail" ||
		body["transport"] != "streamable-http" ||
		body["endpoint"] != "https://mcp.example.com/gmail" || body["icon"] != "mail" {
		t.Errorf("create body wrong: %v", body)
	}
}

func TestIntgAddRunECov_DefaultsDisplayAndTransport(t *testing.T) {
	s := covSetup(t)
	s.OnPost("/api/v1/integrations", clitest.JSONResponse(201, map[string]string{
		"id": covIntgID, "name": "tools",
	}))
	covSetFlag(t, intgAddCmd, "name", "tools")
	// Explicit empty transport exercises the default-fill branch; command
	// goes into the body for the stdio shape.
	covSetFlag(t, intgAddCmd, "transport", "")
	covSetFlag(t, intgAddCmd, "command", "npx server-github")

	if _, err := covCaptureStdout(t, func() error {
		return intgAddCmd.RunE(intgAddCmd, nil)
	}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	body := covJSONBody(t, s.CallsFor("POST", "/api/v1/integrations")[0].Body)
	if body["display_name"] != "tools" {
		t.Errorf("display_name should default to name; got %v", body["display_name"])
	}
	if body["transport"] != "streamable-http" {
		t.Errorf("transport should default; got %v", body["transport"])
	}
	if body["command"] != "npx server-github" {
		t.Errorf("command = %v", body["command"])
	}
}

// ─── intg remove ─────────────────────────────────────────────────────────

func TestIntgRemoveRunECov(t *testing.T) {
	s := covSetup(t)
	covStubIntegrationList(s)
	s.OnDelete("/api/v1/integrations/"+covIntgID, clitest.JSONResponse(200, map[string]string{}))
	covSetFlag(t, intgRemoveCmd, "yes", "true")

	out, err := covCaptureStdout(t, func() error {
		return intgRemoveCmd.RunE(intgRemoveCmd, []string{"gmail"})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "Integration gmail deleted.") {
		t.Errorf("output = %q", out)
	}
	if n := len(s.CallsFor("DELETE", "/api/v1/integrations/"+covIntgID)); n != 1 {
		t.Errorf("DELETE calls = %d, want 1", n)
	}
}

// ─── intg bind / unbind ──────────────────────────────────────────────────

func TestIntgBindRunECov_MissingFlags(t *testing.T) {
	covSetup(t)
	err := intgBindCmd.RunE(intgBindCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "--agent and --server are required") {
		t.Errorf("want required-flags error; got %v", err)
	}
}

func TestIntgBindRunECov_HappyWithCredential(t *testing.T) {
	s := covSetup(t)
	covStubIntegrationList(s)
	s.OnGet("/api/v1/agents", clitest.JSONResponse(200, []map[string]string{
		{"id": covAgentIDCli1, "slug": "pepa"},
	}))
	s.OnGet("/api/v1/credentials", clitest.JSONResponse(200, []map[string]string{
		{"id": covCredID, "name": "pepa-gmail-token"},
	}))
	bindPath := "/api/v1/agents/" + covAgentIDCli1 + "/integrations"
	s.OnPost(bindPath, clitest.JSONResponse(201, map[string]string{"id": "cbind0123456789abcdefghi"}))

	covSetFlag(t, intgBindCmd, "agent", "pepa")
	covSetFlag(t, intgBindCmd, "server", "gmail")
	covSetFlag(t, intgBindCmd, "credential", "pepa-gmail-token")
	covSetFlag(t, intgBindCmd, "cred-header", "X-Api-Key")

	out, err := covCaptureStdout(t, func() error {
		return intgBindCmd.RunE(intgBindCmd, nil)
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "Agent pepa bound to integration gmail.") {
		t.Errorf("output = %q", out)
	}
	body := covJSONBody(t, s.CallsFor("POST", bindPath)[0].Body)
	if body["mcp_server_id"] != covIntgID || body["mcp_server_scope"] != "workspace" {
		t.Errorf("server fields wrong: %v", body)
	}
	if body["credential_id"] != covCredID {
		t.Errorf("credential_id = %v, want %q", body["credential_id"], covCredID)
	}
	if body["cred_type"] != "bearer" { // flag default
		t.Errorf("cred_type = %v, want bearer", body["cred_type"])
	}
	if body["cred_header"] != "X-Api-Key" {
		t.Errorf("cred_header = %v", body["cred_header"])
	}
}

func TestIntgUnbindRunECov_MissingFlags(t *testing.T) {
	covSetup(t)
	err := intgUnbindCmd.RunE(intgUnbindCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "--agent and --binding-id are required") {
		t.Errorf("want required-flags error; got %v", err)
	}
}

func TestIntgUnbindRunECov_Happy(t *testing.T) {
	s := covSetup(t)
	s.OnGet("/api/v1/agents", clitest.JSONResponse(200, []map[string]string{
		{"id": covAgentIDCli1, "slug": "pepa"},
	}))
	delPath := "/api/v1/agents/" + covAgentIDCli1 + "/integrations/bind-1"
	s.OnDelete(delPath, clitest.JSONResponse(200, map[string]string{}))
	covSetFlag(t, intgUnbindCmd, "agent", "pepa")
	covSetFlag(t, intgUnbindCmd, "binding-id", "bind-1")

	out, err := covCaptureStdout(t, func() error {
		return intgUnbindCmd.RunE(intgUnbindCmd, nil)
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "Binding bind-1 removed from agent pepa.") {
		t.Errorf("output = %q", out)
	}
	if n := len(s.CallsFor("DELETE", delPath)); n != 1 {
		t.Errorf("DELETE calls = %d, want 1", n)
	}
}

// ─── intg agent-bindings / resolve ───────────────────────────────────────

func TestIntgAgentBindingsRunECov(t *testing.T) {
	s := covSetup(t)
	s.OnGet("/api/v1/agents", clitest.JSONResponse(200, []map[string]string{
		{"id": covAgentIDCli1, "slug": "pepa"},
	}))
	credName := "pepa-gmail-token"
	credType := "api_key"
	s.OnGet("/api/v1/agents/"+covAgentIDCli1+"/integrations", clitest.JSONResponse(200, []map[string]any{
		{"id": "b1", "mcp_server_id": covIntgID, "mcp_server_scope": "workspace",
			"enabled": true, "server_name": "gmail", "server_display_name": "Google Gmail",
			"credential_name": credName, "cred_type": credType},
		{"id": "b2", "mcp_server_id": covIntgID, "mcp_server_scope": "crew",
			"enabled": false, "server_name": "jira", "server_display_name": "Jira",
			"credential_name": nil, "cred_type": nil},
	}))
	out, err := covCaptureStdout(t, func() error {
		return intgAgentListCmd.RunE(intgAgentListCmd, []string{"pepa"})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	for _, want := range []string{"Google Gmail", "api_key", "bearer", "-", "no"} {
		if !strings.Contains(out, want) {
			t.Errorf("table missing %q:\n%s", want, out)
		}
	}
}

func TestIntgResolveRunECov(t *testing.T) {
	s := covSetup(t)
	s.OnGet("/api/v1/agents", clitest.JSONResponse(200, []map[string]string{
		{"id": covAgentIDCli1, "slug": "pepa"},
	}))
	cred := "tok"
	s.OnGet("/api/v1/agents/"+covAgentIDCli1+"/integrations/resolved", clitest.JSONResponse(200, []map[string]any{
		{"server_id": covIntgID, "scope": "workspace", "name": "gmail",
			"display_name": "Google Gmail", "transport": "streamable-http", "credential_name": cred},
		{"server_id": "x", "scope": "crew", "name": "jira",
			"display_name": "Jira", "transport": "stdio", "credential_name": nil},
	}))
	out, err := covCaptureStdout(t, func() error {
		return intgResolveCmd.RunE(intgResolveCmd, []string{"pepa"})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	for _, want := range []string{"gmail", "jira", "tok"} {
		if !strings.Contains(out, want) {
			t.Errorf("table missing %q:\n%s", want, out)
		}
	}
}

// ─── intg get / test / crews-overview ────────────────────────────────────

func TestIntgGetRunECov(t *testing.T) {
	s := covSetup(t)
	covStubIntegrationList(s)
	endpoint := "https://mcp.example.com/gmail"
	s.OnGet("/api/v1/integrations/"+covIntgID, clitest.JSONResponse(200, map[string]any{
		"id": covIntgID, "name": "gmail", "display_name": "Google Gmail",
		"transport": "streamable-http", "endpoint": endpoint,
		"command": nil, "enabled": true, "icon": nil,
		"created_at": "2026-01-01", "updated_at": "2026-01-02",
	}))
	out, err := covCaptureStdout(t, func() error {
		return intgGetCmd.RunE(intgGetCmd, []string{"gmail"})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	for _, want := range []string{covIntgID, endpoint, "yes"} {
		if !strings.Contains(out, want) {
			t.Errorf("detail missing %q:\n%s", want, out)
		}
	}
}

func TestIntgTestRunECov(t *testing.T) {
	s := covSetup(t)
	covStubIntegrationList(s)
	s.OnPost("/api/v1/integrations/"+covIntgID+"/test", clitest.JSONResponse(200, map[string]any{
		"success": true, "latency_ms": 42,
	}))
	out, err := covCaptureStdout(t, func() error {
		return intgTestCmd.RunE(intgTestCmd, []string{"gmail"})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "latency_ms") {
		t.Errorf("test result JSON missing latency_ms: %q", out)
	}
}

func TestIntgCrewsOverviewRunECov(t *testing.T) {
	s := covSetup(t)
	s.OnGet("/api/v1/integrations/crews", clitest.JSONResponse(200, []map[string]any{
		{"crew_slug": "engineering", "integrations": 2},
	}))
	out, err := covCaptureStdout(t, func() error {
		return intgCrewsOverviewCmd.RunE(intgCrewsOverviewCmd, nil)
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "engineering") {
		t.Errorf("overview missing crew: %q", out)
	}
}
