package main

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

func TestHooksListRunE_TableOutput(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet("/api/v1/hooks", clitest.JSONResponse(200, map[string]any{
		"rows": []map[string]any{
			{"id": "hk_1", "crew_id": "cc1", "event": "assignment.completed", "handler_kind": "webhook",
				"target": "https://example.com/hook", "enabled": true, "created_at": "2026-06-01"},
			{"id": "hk_2", "crew_id": "cc1", "event": "journal.entry", "handler_kind": "script",
				"target": strings.Repeat("x", 50), "enabled": false, "created_at": "2026-06-02"},
		},
		"count": 2,
	}))
	covSetupCli10(t, s.URL())

	out, err := captureStdoutCovCli10(t, func() error {
		return hooksListCmd.RunE(hooksListCmd, nil)
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "hk_1") || !strings.Contains(out, "assignment.completed") {
		t.Errorf("row missing:\n%s", out)
	}
	if !strings.Contains(out, "yes") || !strings.Contains(out, "no") {
		t.Errorf("enabled badges missing:\n%s", out)
	}
	// 50-char target must be truncated with ellipsis.
	if !strings.Contains(out, "…") {
		t.Errorf("long target not truncated:\n%s", out)
	}
}

func TestHooksListRunE_CrewFilterAndEmpty(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet("/api/v1/hooks", clitest.JSONResponse(200, map[string]any{"rows": []map[string]any{}, "count": 0}))
	covSetupCli10(t, s.URL())
	setFlagCovCli10(t, hooksListCmd, "crew", "backend")

	out, err := captureStdoutCovCli10(t, func() error {
		return hooksListCmd.RunE(hooksListCmd, nil)
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "(no hooks registered)") {
		t.Errorf("empty list message missing: %q", out)
	}
	calls := s.CallsFor("GET", "/api/v1/hooks")
	if len(calls) != 1 || !strings.Contains(calls[0].Query, "crew_id=backend") {
		t.Errorf("crew filter not propagated: %+v", calls)
	}
}

func TestHooksListRunE_JSONFormat(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet("/api/v1/hooks", clitest.JSONResponse(200, map[string]any{
		"rows": []map[string]any{{"id": "hk_9", "event": "keeper.decision", "handler_kind": "webhook",
			"target": "t", "enabled": true, "created_at": "2026-06-01"}},
		"count": 1,
	}))
	covSetupCli10(t, s.URL())
	flagFormat = "json"

	out, err := captureStdoutCovCli10(t, func() error {
		return hooksListCmd.RunE(hooksListCmd, nil)
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, `"hk_9"`) || !strings.Contains(out, `"keeper.decision"`) {
		t.Errorf("json rows missing: %q", out)
	}
}

func TestHooksListRunE_NoAuth(t *testing.T) {
	covSetupCli10(t, "http://127.0.0.1:0")
	cliCfg = &cli.CLIConfig{}
	if err := hooksListCmd.RunE(hooksListCmd, nil); err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("expected not-logged-in, got %v", err)
	}
}

func TestHooksToggle_EnablePostsToEnableURL(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnPost("/api/v1/hooks/hk_abc/enable", clitest.JSONResponse(200, map[string]any{"ok": true}))
	covSetupCli10(t, s.URL())

	out, err := captureStdoutCovCli10(t, func() error {
		return hooksEnableCmd.RunE(hooksEnableCmd, []string{"hk_abc"})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if got := len(s.CallsFor("POST", "/api/v1/hooks/hk_abc/enable")); got != 1 {
		t.Errorf("enable POST calls = %d, want 1", got)
	}
	if !strings.Contains(out, "Hook hk_abc: enabled") {
		t.Errorf("confirmation missing: %q", out)
	}
}

func TestHooksToggle_DisablePostsToDisableURL(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnPost("/api/v1/hooks/hk_abc/disable", clitest.JSONResponse(200, map[string]any{"ok": true}))
	covSetupCli10(t, s.URL())

	out, err := captureStdoutCovCli10(t, func() error {
		return hooksDisableCmd.RunE(hooksDisableCmd, []string{"hk_abc"})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if got := len(s.CallsFor("POST", "/api/v1/hooks/hk_abc/disable")); got != 1 {
		t.Errorf("disable POST calls = %d, want 1", got)
	}
	if !strings.Contains(out, "Hook hk_abc: disabled") {
		t.Errorf("confirmation missing: %q", out)
	}
}

func TestHooksToggle_NoAuth(t *testing.T) {
	covSetupCli10(t, "http://127.0.0.1:0")
	cliCfg = &cli.CLIConfig{}
	if err := hooksToggle("hk_1", true); err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("expected not-logged-in, got %v", err)
	}
}

func TestHooksToggle_NoWorkspace(t *testing.T) {
	covSetupCli10(t, "http://127.0.0.1:0")
	cliCfg = &cli.CLIConfig{Token: "tok"}
	if err := hooksToggle("hk_1", true); err == nil || !strings.Contains(err.Error(), "workspace") {
		t.Errorf("expected workspace error, got %v", err)
	}
}

func TestHooksToggle_ServerError(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnPost("/api/v1/hooks/hk_1/enable", clitest.ErrorResponse(404, "hook missing"))
	covSetupCli10(t, s.URL())
	err := hooksToggle("hk_1", true)
	if err == nil || !strings.Contains(err.Error(), "hook missing") {
		t.Errorf("expected server error surfaced, got %v", err)
	}
}
