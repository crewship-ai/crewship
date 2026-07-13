package main

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

// These drive the real `crewship crew container-status` command against a stub
// API server — the supported agent contract (API↔CLI parity), not a
// hand-rolled HTTP request.

func TestCrewContainerStatusRunE_Running(t *testing.T) {
	stub := covSetupCli4(t)
	// slug → id resolution
	stub.OnGet("/api/v1/crews", clitest.JSONResponse(200, []map[string]string{
		{"id": covCrewIDCli4, "slug": "engineering"},
	}))
	stub.OnGet("/api/v1/crews/"+covCrewIDCli4+"/container-status", clitest.JSONResponse(200, map[string]any{
		"crew_id": covCrewIDCli4, "status": "running", "uptime": "2026-07-13T00:00:00Z",
	}))

	c := covFreshCmd(crewContainerStatusCmd, nil)
	out, err := covCaptureStdoutCli4(t, func() error { return c.RunE(c, []string{"engineering"}) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "Container:") || !strings.Contains(out, "running") {
		t.Errorf("status line missing: %q", out)
	}
	if !strings.Contains(out, "Started:") || !strings.Contains(out, "2026-07-13") {
		t.Errorf("uptime line missing: %q", out)
	}
}

func TestCrewContainerStatusRunE_NotConfiguredNoUptime(t *testing.T) {
	stub := covSetupCli4(t)
	stub.OnGet("/api/v1/crews/"+covCrewIDCli4+"/container-status", clitest.JSONResponse(200, map[string]any{
		"crew_id": covCrewIDCli4, "status": "not_configured",
	}))

	c := covFreshCmd(crewContainerStatusCmd, nil)
	// Pass the CUID directly so no /api/v1/crews resolution round-trip is needed.
	out, err := covCaptureStdoutCli4(t, func() error { return c.RunE(c, []string{covCrewIDCli4}) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "not_configured") {
		t.Errorf("want not_configured, got %q", out)
	}
	if strings.Contains(out, "Started:") {
		t.Errorf("no uptime should be printed when absent: %q", out)
	}
}

func TestCrewContainerStatusRunE_ServerError(t *testing.T) {
	stub := covSetupCli4(t)
	stub.OnGet("/api/v1/crews/"+covCrewIDCli4+"/container-status", clitest.ErrorResponse(404, "Crew not found"))

	c := covFreshCmd(crewContainerStatusCmd, nil)
	_, err := covCaptureStdoutCli4(t, func() error { return c.RunE(c, []string{covCrewIDCli4}) })
	if err == nil {
		t.Fatalf("expected error on 404, got nil")
	}
}
