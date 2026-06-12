package main

// Coverage tests for cmd_integration_crew.go (crew-scoped MCP
// integration list / create / update / delete / test). Serial — they
// mutate cliCfg + cobra flag globals.

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

const covIntegrationID = "cinteg0123456789abcde"

// ─── list ────────────────────────────────────────────────────────────────

func TestIntgCrewListRunE_Happy(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)

	longEndpoint := "https://mcp.example.com/very/long/path/that/exceeds/forty/characters/for-sure"
	stub.OnGet("/api/v1/crews/"+covCrewIDCli6+"/integrations", clitest.JSONResponse(200, []map[string]any{
		{
			"id": "i1", "name": "linear", "display_name": "Linear",
			"transport": "streamable-http", "endpoint": longEndpoint, "enabled": true,
		},
		{
			"id": "i2", "name": "local", "display_name": "Local Tools",
			"transport": "stdio", "endpoint": nil, "enabled": false,
		},
	}))

	out, err := covCaptureStdoutCli6(t, func() error {
		return intgCrewListCmd.RunE(intgCrewListCmd, []string{covCrewIDCli6})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "linear") || !strings.Contains(out, "Local Tools") {
		t.Errorf("table missing rows: %q", out)
	}
	// >40 char endpoints are truncated with an ellipsis; nil endpoints
	// render as "-".
	if !strings.Contains(out, longEndpoint[:37]+"...") {
		t.Errorf("long endpoint not truncated: %q", out)
	}
	if strings.Contains(out, longEndpoint) {
		t.Errorf("full endpoint should not appear: %q", out)
	}
}

func TestIntgCrewListRunE_CrewResolveError(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)

	stub.OnGet("/api/v1/crews", clitest.JSONResponse(200, []map[string]string{}))

	err := intgCrewListCmd.RunE(intgCrewListCmd, []string{"ghost"})
	if err == nil || !strings.Contains(err.Error(), "crew not found: ghost") {
		t.Errorf("expected crew-not-found, got %v", err)
	}
}

func TestIntgCrewListRunE_APIError(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)

	stub.OnGet("/api/v1/crews/"+covCrewIDCli6+"/integrations", clitest.ErrorResponse(500, "db down"))

	err := intgCrewListCmd.RunE(intgCrewListCmd, []string{covCrewIDCli6})
	if err == nil || !strings.Contains(err.Error(), "db down") {
		t.Errorf("expected 500 surfaced, got %v", err)
	}
}

func TestIntgCrewListRunE_NoAuth(t *testing.T) {
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{}

	err := intgCrewListCmd.RunE(intgCrewListCmd, []string{covCrewIDCli6})
	if err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("expected 'not logged in', got %v", err)
	}
}

// ─── create ──────────────────────────────────────────────────────────────

func TestIntgCrewCreateRunE_RequiresName(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)
	covResetFlagsCli6(t, intgCrewCreateCmd, "name")

	err := intgCrewCreateCmd.RunE(intgCrewCreateCmd, []string{covCrewIDCli6})
	if err == nil || !strings.Contains(err.Error(), "--name is required") {
		t.Errorf("expected '--name is required', got %v", err)
	}
}

func TestIntgCrewCreateRunE_Defaults(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)
	covSetFlagCli6(t, intgCrewCreateCmd, "name", "gmail")
	covResetFlagsCli6(t, intgCrewCreateCmd, "display", "endpoint", "command", "icon", "link-workspace-server")

	stub.OnPost("/api/v1/crews/"+covCrewIDCli6+"/integrations", clitest.JSONResponse(201, map[string]string{
		"id": covIntegrationID, "name": "gmail",
	}))

	out, err := covCaptureStdoutCli6(t, func() error {
		return intgCrewCreateCmd.RunE(intgCrewCreateCmd, []string{covCrewIDCli6})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "Crew integration created: gmail ("+covIntegrationID+")") {
		t.Errorf("success line missing: %q", out)
	}

	body := covDecodeBody(t, stub.CallsFor("POST", "/api/v1/crews/"+covCrewIDCli6+"/integrations")[0].Body)
	if body["name"] != "gmail" {
		t.Errorf("name = %v", body["name"])
	}
	if body["display_name"] != "gmail" {
		t.Errorf("display_name should default to name, got %v", body["display_name"])
	}
	if body["transport"] != "streamable-http" {
		t.Errorf("transport should default to streamable-http, got %v", body["transport"])
	}
	for _, absent := range []string{"endpoint", "command", "icon", "workspace_mcp_server_id"} {
		if _, ok := body[absent]; ok {
			t.Errorf("optional key %q must be omitted when empty", absent)
		}
	}
}

func TestIntgCrewCreateRunE_FullBody(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)
	covSetFlagCli6(t, intgCrewCreateCmd, "name", "gh")
	covSetFlagCli6(t, intgCrewCreateCmd, "display", "GitHub")
	covSetFlagCli6(t, intgCrewCreateCmd, "transport", "stdio")
	covSetFlagCli6(t, intgCrewCreateCmd, "endpoint", "https://mcp.example.com")
	covSetFlagCli6(t, intgCrewCreateCmd, "command", "npx server-github")
	covSetFlagCli6(t, intgCrewCreateCmd, "icon", "github")
	covSetFlagCli6(t, intgCrewCreateCmd, "link-workspace-server", "ws-mcp-1")

	stub.OnPost("/api/v1/crews/"+covCrewIDCli6+"/integrations", clitest.JSONResponse(201, map[string]string{
		"id": covIntegrationID, "name": "gh",
	}))

	if _, err := covCaptureStdoutCli6(t, func() error {
		return intgCrewCreateCmd.RunE(intgCrewCreateCmd, []string{covCrewIDCli6})
	}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	body := covDecodeBody(t, stub.CallsFor("POST", "/api/v1/crews/"+covCrewIDCli6+"/integrations")[0].Body)
	want := map[string]any{
		"name":                    "gh",
		"display_name":            "GitHub",
		"transport":               "stdio",
		"endpoint":                "https://mcp.example.com",
		"command":                 "npx server-github",
		"icon":                    "github",
		"workspace_mcp_server_id": "ws-mcp-1",
	}
	for k, v := range want {
		if body[k] != v {
			t.Errorf("body[%q] = %v, want %v", k, body[k], v)
		}
	}
}

func TestIntgCrewCreateRunE_APIError(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)
	covSetFlagCli6(t, intgCrewCreateCmd, "name", "gmail")

	stub.OnPost("/api/v1/crews/"+covCrewIDCli6+"/integrations", clitest.ErrorResponse(409, "name taken"))

	err := intgCrewCreateCmd.RunE(intgCrewCreateCmd, []string{covCrewIDCli6})
	if err == nil || !strings.Contains(err.Error(), "name taken") {
		t.Errorf("expected 409 surfaced, got %v", err)
	}
}

// ─── update ──────────────────────────────────────────────────────────────

func TestIntgCrewUpdateRunE_NoFields(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)
	covResetFlagsCli6(t, intgCrewUpdateCmd, "display", "transport", "endpoint", "command", "icon", "enabled")

	err := intgCrewUpdateCmd.RunE(intgCrewUpdateCmd, []string{covCrewIDCli6, covIntegrationID})
	if err == nil || !strings.Contains(err.Error(), "no fields to update") {
		t.Errorf("expected 'no fields to update', got %v", err)
	}
}

func TestIntgCrewUpdateRunE_PatchesChangedFields(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)
	covSetFlagCli6(t, intgCrewUpdateCmd, "display", "New Name")
	covSetFlagCli6(t, intgCrewUpdateCmd, "transport", "stdio")
	covSetFlagCli6(t, intgCrewUpdateCmd, "endpoint", "https://new.example.com")
	covSetFlagCli6(t, intgCrewUpdateCmd, "command", "npx new")
	covSetFlagCli6(t, intgCrewUpdateCmd, "icon", "zap")
	covSetFlagCli6(t, intgCrewUpdateCmd, "enabled", "false")

	path := "/api/v1/crews/" + covCrewIDCli6 + "/integrations/" + covIntegrationID
	stub.OnPatch(path, clitest.EmptyResponse(200))

	out, err := covCaptureStdoutCli6(t, func() error {
		return intgCrewUpdateCmd.RunE(intgCrewUpdateCmd, []string{covCrewIDCli6, covIntegrationID})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "updated") {
		t.Errorf("update confirmation missing: %q", out)
	}
	body := covDecodeBody(t, stub.CallsFor("PATCH", path)[0].Body)
	want := map[string]any{
		"display_name": "New Name",
		"transport":    "stdio",
		"endpoint":     "https://new.example.com",
		"command":      "npx new",
		"icon":         "zap",
		"enabled":      false,
	}
	for k, v := range want {
		if body[k] != v {
			t.Errorf("body[%q] = %v, want %v", k, body[k], v)
		}
	}
}

func TestIntgCrewUpdateRunE_APIError(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)
	covSetFlagCli6(t, intgCrewUpdateCmd, "display", "x")

	stub.OnPatch("/api/v1/crews/"+covCrewIDCli6+"/integrations/"+covIntegrationID,
		clitest.ErrorResponse(404, "integration not found"))

	err := intgCrewUpdateCmd.RunE(intgCrewUpdateCmd, []string{covCrewIDCli6, covIntegrationID})
	if err == nil || !strings.Contains(err.Error(), "integration not found") {
		t.Errorf("expected 404 surfaced, got %v", err)
	}
}

// ─── delete ──────────────────────────────────────────────────────────────

func TestIntgCrewDeleteRunE_AbortedWithoutYes(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)
	covResetFlagsCli6(t, intgCrewDeleteCmd, "yes")

	err := intgCrewDeleteCmd.RunE(intgCrewDeleteCmd, []string{covCrewIDCli6, covIntegrationID})
	if err == nil || !strings.Contains(err.Error(), "aborted") {
		t.Fatalf("expected aborted, got %v", err)
	}
	if n := len(stub.Calls()); n != 0 {
		t.Errorf("aborted delete must not issue HTTP calls, got %d", n)
	}
}

func TestIntgCrewDeleteRunE_Happy(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)
	covSetFlagCli6(t, intgCrewDeleteCmd, "yes", "true")

	path := "/api/v1/crews/" + covCrewIDCli6 + "/integrations/" + covIntegrationID
	stub.OnDelete(path, clitest.EmptyResponse(204))

	out, err := covCaptureStdoutCli6(t, func() error {
		return intgCrewDeleteCmd.RunE(intgCrewDeleteCmd, []string{covCrewIDCli6, covIntegrationID})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "deleted") {
		t.Errorf("delete confirmation missing: %q", out)
	}
	if n := len(stub.CallsFor("DELETE", path)); n != 1 {
		t.Errorf("expected exactly 1 DELETE, got %d", n)
	}
}

// ─── shared error-path tables ────────────────────────────────────────────

// intgCrewRunners enumerates each crew-integration RunE with valid args
// + flags so the shared short-circuits can be table-tested.
func intgCrewRunners(t *testing.T) map[string]func() error {
	t.Helper()
	covSetFlagCli6(t, intgCrewCreateCmd, "name", "gmail")
	covSetFlagCli6(t, intgCrewUpdateCmd, "display", "x")
	covSetFlagCli6(t, intgCrewDeleteCmd, "yes", "true")
	two := []string{covCrewIDCli6, covIntegrationID}
	return map[string]func() error{
		"list":   func() error { return intgCrewListCmd.RunE(intgCrewListCmd, []string{covCrewIDCli6}) },
		"create": func() error { return intgCrewCreateCmd.RunE(intgCrewCreateCmd, []string{covCrewIDCli6}) },
		"update": func() error { return intgCrewUpdateCmd.RunE(intgCrewUpdateCmd, two) },
		"delete": func() error { return intgCrewDeleteCmd.RunE(intgCrewDeleteCmd, two) },
		"test":   func() error { return intgCrewTestCmd.RunE(intgCrewTestCmd, two) },
	}
}

func TestIntgCrewCmds_NoAuth(t *testing.T) {
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{}
	for name, run := range intgCrewRunners(t) {
		if err := run(); err == nil || !strings.Contains(err.Error(), "not logged in") {
			t.Errorf("%s: expected 'not logged in', got %v", name, err)
		}
	}
}

func TestIntgCrewCmds_NoWorkspace(t *testing.T) {
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{Token: "fake-token"}
	flagWorkspace = ""
	t.Setenv("CREWSHIP_WORKSPACE", "")
	for name, run := range intgCrewRunners(t) {
		if err := run(); err == nil || !strings.Contains(err.Error(), "workspace") {
			t.Errorf("%s: expected workspace error, got %v", name, err)
		}
	}
}

func TestIntgCrewCmds_TransportError(t *testing.T) {
	stub := clitest.NewStubServer()
	stub.Close()
	covSetupCli6(t, stub)
	for name, run := range intgCrewRunners(t) {
		if err := run(); err == nil || !strings.Contains(err.Error(), "request failed") {
			t.Errorf("%s: expected transport error, got %v", name, err)
		}
	}
}

// TestIntgCrewCmds_CrewResolveError drives the slug→ID lookup miss on
// every command that takes a crew slug argument.
func TestIntgCrewCmds_CrewResolveError(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)
	covSetFlagCli6(t, intgCrewCreateCmd, "name", "gmail")
	covSetFlagCli6(t, intgCrewUpdateCmd, "display", "x")
	covSetFlagCli6(t, intgCrewDeleteCmd, "yes", "true")
	stub.OnGet("/api/v1/crews", clitest.JSONResponse(200, []map[string]string{}))

	runners := map[string]func() error{
		"create": func() error { return intgCrewCreateCmd.RunE(intgCrewCreateCmd, []string{"ghost"}) },
		"update": func() error { return intgCrewUpdateCmd.RunE(intgCrewUpdateCmd, []string{"ghost", covIntegrationID}) },
		"delete": func() error { return intgCrewDeleteCmd.RunE(intgCrewDeleteCmd, []string{"ghost", covIntegrationID}) },
		"test":   func() error { return intgCrewTestCmd.RunE(intgCrewTestCmd, []string{"ghost", covIntegrationID}) },
	}
	for name, run := range runners {
		if err := run(); err == nil || !strings.Contains(err.Error(), "crew not found: ghost") {
			t.Errorf("%s: expected crew-not-found, got %v", name, err)
		}
	}
}

func TestIntgCrewListRunE_DecodeError(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)
	stub.OnGet("/api/v1/crews/"+covCrewIDCli6+"/integrations", clitest.TextResponse(200, "not json"))

	err := intgCrewListCmd.RunE(intgCrewListCmd, []string{covCrewIDCli6})
	if err == nil || !strings.Contains(err.Error(), "decode response") {
		t.Errorf("expected decode error, got %v", err)
	}
}

func TestIntgCrewCreateRunE_DecodeError(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)
	covSetFlagCli6(t, intgCrewCreateCmd, "name", "gmail")
	stub.OnPost("/api/v1/crews/"+covCrewIDCli6+"/integrations", clitest.TextResponse(200, "not json"))

	err := intgCrewCreateCmd.RunE(intgCrewCreateCmd, []string{covCrewIDCli6})
	if err == nil || !strings.Contains(err.Error(), "decode response") {
		t.Errorf("expected decode error, got %v", err)
	}
}

func TestIntgCrewDeleteRunE_APIError(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)
	covSetFlagCli6(t, intgCrewDeleteCmd, "yes", "true")
	stub.OnDelete("/api/v1/crews/"+covCrewIDCli6+"/integrations/"+covIntegrationID,
		clitest.ErrorResponse(404, "integration gone"))

	err := intgCrewDeleteCmd.RunE(intgCrewDeleteCmd, []string{covCrewIDCli6, covIntegrationID})
	if err == nil || !strings.Contains(err.Error(), "integration gone") {
		t.Errorf("expected 404 surfaced, got %v", err)
	}
}

func TestIntgCrewTestRunE_DecodeError(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)
	stub.OnPost("/api/v1/crews/"+covCrewIDCli6+"/integrations/"+covIntegrationID+"/test",
		clitest.TextResponse(200, "not json"))

	err := intgCrewTestCmd.RunE(intgCrewTestCmd, []string{covCrewIDCli6, covIntegrationID})
	if err == nil || !strings.Contains(err.Error(), "decode response") {
		t.Errorf("expected decode error, got %v", err)
	}
}

// ─── test connection ─────────────────────────────────────────────────────

func TestIntgCrewTestRunE_Happy(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)

	path := "/api/v1/crews/" + covCrewIDCli6 + "/integrations/" + covIntegrationID + "/test"
	stub.OnPost(path, clitest.JSONResponse(200, map[string]any{
		"status": "ok", "tools": 5,
	}))

	out, err := covCaptureStdoutCli6(t, func() error {
		return intgCrewTestCmd.RunE(intgCrewTestCmd, []string{covCrewIDCli6, covIntegrationID})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, `"status": "ok"`) {
		t.Errorf("test result JSON missing: %q", out)
	}
	if n := len(stub.CallsFor("POST", path)); n != 1 {
		t.Errorf("expected exactly 1 test POST, got %d", n)
	}
}

func TestIntgCrewTestRunE_APIError(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)

	stub.OnPost("/api/v1/crews/"+covCrewIDCli6+"/integrations/"+covIntegrationID+"/test",
		clitest.ErrorResponse(502, "endpoint unreachable"))

	err := intgCrewTestCmd.RunE(intgCrewTestCmd, []string{covCrewIDCli6, covIntegrationID})
	if err == nil || !strings.Contains(err.Error(), "endpoint unreachable") {
		t.Errorf("expected 502 surfaced, got %v", err)
	}
}
