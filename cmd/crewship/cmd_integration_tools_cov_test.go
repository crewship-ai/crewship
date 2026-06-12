package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

func TestIntgToolsList_RendersBindings(t *testing.T) {
	s := covStubCli9(t)
	longDesc := strings.Repeat("x", 60)
	s.OnGet("/api/v1/crews/"+covCrew+"/integrations/intg1/tools", clitest.JSONResponse(200, []map[string]any{
		{"id": "b1", "tool_name": "list_issues", "description": longDesc, "enabled": true, "updated_at": "2026-06-01"},
		{"id": "b2", "tool_name": "create_pr", "description": nil, "enabled": false, "updated_at": "2026-06-02"},
	}))

	out := covCaptureStdoutCli9(t, func() {
		if err := intgToolsListCmd.RunE(intgToolsListCmd, []string{covCrew, "intg1"}); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	for _, want := range []string{"list_issues", "create_pr", strings.Repeat("x", 47) + "..."} {
		if !strings.Contains(out, want) {
			t.Errorf("tools table missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, longDesc) {
		t.Errorf("60-char description should be truncated:\n%s", out)
	}
}

func TestToggleCrewIntegrationTool_EnableDisable(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		name := "disable"
		cmd := intgToolsDisableCmd
		if enabled {
			name = "enable"
			cmd = intgToolsEnableCmd
		}
		t.Run(name, func(t *testing.T) {
			s := covStubCli9(t)
			// Tool name with a space exercises the PathEscape branch:
			// the server sees the decoded path segment.
			s.OnPatch("/api/v1/crews/"+covCrew+"/integrations/intg1/tools/my tool", clitest.JSONResponse(200, map[string]bool{"ok": true}))

			out := covCaptureStdoutCli9(t, func() {
				if err := cmd.RunE(cmd, []string{covCrew, "intg1", "my tool"}); err != nil {
					t.Errorf("RunE: %v", err)
				}
			})
			wantState := "disabled."
			if enabled {
				wantState = "enabled."
			}
			if !strings.Contains(out, "Tool my tool on "+covCrew+"/intg1 "+wantState) {
				t.Errorf("confirmation missing:\n%s", out)
			}

			calls := s.CallsFor("PATCH", "/api/v1/crews/"+covCrew+"/integrations/intg1/tools/my tool")
			if len(calls) != 1 {
				t.Fatalf("expected one PATCH, got %d", len(calls))
			}
			var body map[string]bool
			_ = json.Unmarshal(calls[0].Body, &body)
			if body["enabled"] != enabled {
				t.Errorf("PATCH body = %v, want enabled=%v", body, enabled)
			}
		})
	}
}

func TestToggleCrewIntegrationTool_ResolvesCrewSlug(t *testing.T) {
	s := covStubCli9(t)
	s.OnGet("/api/v1/crews", clitest.JSONResponse(200, []map[string]string{
		{"id": "ccrewresolved12345678", "slug": "backend"},
	}))
	s.OnPatch("/api/v1/crews/ccrewresolved12345678/integrations/intg1/tools/t1", clitest.JSONResponse(200, map[string]bool{"ok": true}))

	_ = covCaptureStdoutCli9(t, func() {
		if err := toggleCrewIntegrationTool("backend", "intg1", "t1", true); err != nil {
			t.Errorf("toggle: %v", err)
		}
	})
	if got := len(s.CallsFor("PATCH", "/api/v1/crews/ccrewresolved12345678/integrations/intg1/tools/t1")); got != 1 {
		t.Errorf("PATCH after slug resolution = %d calls, want 1", got)
	}
}

func TestToggleCrewIntegrationTool_Errors(t *testing.T) {
	t.Run("crew not found", func(t *testing.T) {
		s := covStubCli9(t)
		s.OnGet("/api/v1/crews", clitest.JSONResponse(200, []map[string]string{}))
		err := toggleCrewIntegrationTool("ghost-crew", "intg1", "t1", true)
		if err == nil || !strings.Contains(err.Error(), "crew not found: ghost-crew") {
			t.Errorf("expected crew-not-found; got %v", err)
		}
	})
	t.Run("server rejects patch", func(t *testing.T) {
		s := covStubCli9(t)
		s.OnPatch("/api/v1/crews/"+covCrew+"/integrations/intg1/tools/t1", clitest.ErrorResponse(404, "binding missing"))
		err := toggleCrewIntegrationTool(covCrew, "intg1", "t1", false)
		if err == nil || !strings.Contains(err.Error(), "binding missing") {
			t.Errorf("expected binding error; got %v", err)
		}
	})
	t.Run("no auth", func(t *testing.T) {
		covSaveState(t)
		cliCfg = &cli.CLIConfig{}
		if err := toggleCrewIntegrationTool("c", "i", "t", true); err == nil {
			t.Error("expected not-logged-in error")
		}
	})
	t.Run("no workspace", func(t *testing.T) {
		covSaveState(t)
		cliCfg = &cli.CLIConfig{Token: "tok"}
		err := toggleCrewIntegrationTool("c", "i", "t", true)
		if err == nil || !strings.Contains(err.Error(), "workspace") {
			t.Errorf("expected workspace error; got %v", err)
		}
	})
}

func TestIntgToolsRefresh_EmptyBodyConfirmation(t *testing.T) {
	s := covStubCli9(t)
	s.OnPost("/api/v1/crews/"+covCrew+"/integrations/intg1/tools/refresh", clitest.EmptyResponse(200))

	out := covCaptureStdoutCli9(t, func() {
		if err := intgToolsRefreshCmd.RunE(intgToolsRefreshCmd, []string{covCrew, "intg1"}); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	if !strings.Contains(out, "Tool bindings refresh requested for "+covCrew+"/intg1.") {
		t.Errorf("refresh confirmation missing:\n%s", out)
	}

	calls := s.CallsFor("POST", "/api/v1/crews/"+covCrew+"/integrations/intg1/tools/refresh")
	if len(calls) != 1 || !strings.Contains(string(calls[0].Body), `"tools":[]`) {
		t.Errorf("refresh must send the empty tools list: %+v", calls)
	}
}

func TestIntgToolsRefresh_JSONResult(t *testing.T) {
	s := covStubCli9(t)
	s.OnPost("/api/v1/crews/"+covCrew+"/integrations/intg1/tools/refresh", clitest.JSONResponse(200, map[string]any{
		"upserted": 3, "kept": 1,
	}))

	out := covCaptureStdoutCli9(t, func() {
		if err := intgToolsRefreshCmd.RunE(intgToolsRefreshCmd, []string{covCrew, "intg1"}); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	if !strings.Contains(out, `"upserted"`) {
		t.Errorf("refresh result JSON missing:\n%s", out)
	}
}

func TestIntgToolsRefresh_BadJSONResult(t *testing.T) {
	s := covStubCli9(t)
	s.OnPost("/api/v1/crews/"+covCrew+"/integrations/intg1/tools/refresh", clitest.TextResponse(200, "{nope"))

	err := intgToolsRefreshCmd.RunE(intgToolsRefreshCmd, []string{covCrew, "intg1"})
	if err == nil || !strings.Contains(err.Error(), "decode refresh response") {
		t.Errorf("expected decode error; got %v", err)
	}
}

func TestIntgToolsListAndRefresh_AuthGates(t *testing.T) {
	covSaveState(t)
	args := []string{covCrew, "intg1"}
	for _, cmd := range []struct {
		name string
		run  func() error
	}{
		{"list", func() error { return intgToolsListCmd.RunE(intgToolsListCmd, args) }},
		{"refresh", func() error { return intgToolsRefreshCmd.RunE(intgToolsRefreshCmd, args) }},
	} {
		cliCfg = &cli.CLIConfig{}
		if err := cmd.run(); err == nil || !strings.Contains(err.Error(), "not logged in") {
			t.Errorf("%s: expected not-logged-in; got %v", cmd.name, err)
		}
		cliCfg = &cli.CLIConfig{Token: "tok"}
		if err := cmd.run(); err == nil || !strings.Contains(err.Error(), "workspace") {
			t.Errorf("%s: expected workspace error; got %v", cmd.name, err)
		}
	}
}

func TestIntgTools_TransportErrors(t *testing.T) {
	covStubDown(t)
	if err := intgToolsListCmd.RunE(intgToolsListCmd, []string{covCrew, "intg1"}); err == nil {
		t.Error("list: expected transport error")
	}
	if err := intgToolsRefreshCmd.RunE(intgToolsRefreshCmd, []string{covCrew, "intg1"}); err == nil {
		t.Error("refresh: expected transport error")
	}
	if err := toggleCrewIntegrationTool(covCrew, "intg1", "t1", true); err == nil {
		t.Error("toggle: expected transport error")
	}
}

func TestIntgTools_CrewResolutionFailures(t *testing.T) {
	s := covStubCli9(t)
	s.OnGet("/api/v1/crews", clitest.JSONResponse(200, []map[string]string{}))
	if err := intgToolsListCmd.RunE(intgToolsListCmd, []string{"ghost", "intg1"}); err == nil || !strings.Contains(err.Error(), "crew not found") {
		t.Errorf("list: expected crew-not-found; got %v", err)
	}
	if err := intgToolsRefreshCmd.RunE(intgToolsRefreshCmd, []string{"ghost", "intg1"}); err == nil || !strings.Contains(err.Error(), "crew not found") {
		t.Errorf("refresh: expected crew-not-found; got %v", err)
	}
}

func TestIntgToolsList_DecodeError(t *testing.T) {
	s := covStubCli9(t)
	s.OnGet("/api/v1/crews/"+covCrew+"/integrations/intg1/tools", clitest.TextResponse(200, "{nope"))
	if err := intgToolsListCmd.RunE(intgToolsListCmd, []string{covCrew, "intg1"}); err == nil {
		t.Error("expected decode error on malformed tools body")
	}
}

func TestIntgToolsRefresh_ServerError(t *testing.T) {
	s := covStubCli9(t)
	s.OnPost("/api/v1/crews/"+covCrew+"/integrations/intg1/tools/refresh", clitest.ErrorResponse(500, "refresh broke"))
	err := intgToolsRefreshCmd.RunE(intgToolsRefreshCmd, []string{covCrew, "intg1"})
	if err == nil || !strings.Contains(err.Error(), "refresh broke") {
		t.Errorf("expected 500 error; got %v", err)
	}
}

func TestIntgToolsList_ServerError(t *testing.T) {
	s := covStubCli9(t)
	s.OnGet("/api/v1/crews/"+covCrew+"/integrations/intg1/tools", clitest.ErrorResponse(403, "no access"))
	err := intgToolsListCmd.RunE(intgToolsListCmd, []string{covCrew, "intg1"})
	if err == nil || !strings.Contains(err.Error(), "no access") {
		t.Errorf("expected 403 error; got %v", err)
	}
}
