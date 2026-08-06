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

// #1642: a crew container created by an older build keeps that build's
// container configuration until it is recreated, and this command is where an
// operator can see it. The line has to name the remedy — a status that says
// "stale" and stops there sends the reader to the source.
func TestCrewContainerStatusRunE_ReportsStaleRuntimeContract(t *testing.T) {
	stub := covSetupCli4(t)
	stub.OnGet("/api/v1/crews/"+covCrewIDCli4+"/container-status", clitest.JSONResponse(200, map[string]any{
		"crew_id": covCrewIDCli4, "status": "running", "runtime_contract": "stale",
	}))

	c := covFreshCmd(crewContainerStatusCmd, nil)
	out, err := covCaptureStdoutCli4(t, func() error { return c.RunE(c, []string{covCrewIDCli4}) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "older build") {
		t.Errorf("stale runtime contract not reported: %q", out)
	}
	if !strings.Contains(out, "restart-agents") {
		t.Errorf("the remedy is not named, so the report is not actionable: %q", out)
	}
}

// A provider with no opinion must produce no claim. Printing "current" for an
// absent verdict would assert exactly the thing that was not checked.
func TestCrewContainerStatusRunE_SaysNothingWhenContractUnknown(t *testing.T) {
	stub := covSetupCli4(t)
	stub.OnGet("/api/v1/crews/"+covCrewIDCli4+"/container-status", clitest.JSONResponse(200, map[string]any{
		"crew_id": covCrewIDCli4, "status": "running",
	}))

	c := covFreshCmd(crewContainerStatusCmd, nil)
	out, err := covCaptureStdoutCli4(t, func() error { return c.RunE(c, []string{covCrewIDCli4}) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if strings.Contains(out, "Config:") {
		t.Errorf("a config verdict was printed for a provider that reported none: %q", out)
	}
}

// #1681: a crew whose memory or CPU limit was edited keeps the OLD cgroup
// limit until its container is recreated, and `crew get` reports the
// configured figure either way. This command is where the two are shown side
// by side — the report has to name both numbers and the remedy, or the
// operator is left knowing only what they asked for.
func TestCrewContainerStatusRunE_ReportsConfiguredVsEffectiveLimits(t *testing.T) {
	stub := covSetupCli4(t)
	stub.OnGet("/api/v1/crews/"+covCrewIDCli4+"/container-status", clitest.JSONResponse(200, map[string]any{
		"crew_id": covCrewIDCli4, "status": "running",
		"configured_memory_mb": 8192, "configured_cpus": 2.0,
		"effective_memory_mb": 4096, "effective_cpus": 2.0,
		"config_drift": []map[string]any{
			{"field": "container_memory_mb", "configured": 8192, "effective": 4096},
		},
	}))

	c := covFreshCmd(crewContainerStatusCmd, nil)
	out, err := covCaptureStdoutCli4(t, func() error { return c.RunE(c, []string{covCrewIDCli4}) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "container_memory_mb") {
		t.Errorf("the drifted field is not named: %q", out)
	}
	if !strings.Contains(out, "8192") || !strings.Contains(out, "4096") {
		t.Errorf("both the configured and the effective figure have to appear: %q", out)
	}
	if !strings.Contains(out, "restart-agents") {
		t.Errorf("the remedy is not named, so the report is not actionable: %q", out)
	}
}

// The quiet path: limits that agree print nothing about drift. A command that
// narrates every matching field trains its reader to skip the output.
func TestCrewContainerStatusRunE_SilentWhenLimitsAgree(t *testing.T) {
	stub := covSetupCli4(t)
	stub.OnGet("/api/v1/crews/"+covCrewIDCli4+"/container-status", clitest.JSONResponse(200, map[string]any{
		"crew_id": covCrewIDCli4, "status": "running",
		"configured_memory_mb": 8192, "configured_cpus": 2.0,
		"effective_memory_mb": 8192, "effective_cpus": 2.0,
	}))

	c := covFreshCmd(crewContainerStatusCmd, nil)
	out, err := covCaptureStdoutCli4(t, func() error { return c.RunE(c, []string{covCrewIDCli4}) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if strings.Contains(out, "Limits:") {
		t.Errorf("a limits warning was printed for a container that matches its crew: %q", out)
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
