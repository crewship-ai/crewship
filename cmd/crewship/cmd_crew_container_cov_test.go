package main

import (
	"encoding/json"
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

// A stopped container must not be described in the present tense, and must not
// be handed a remedy it does not need.
//
// Found on a live daemon: with the container `Exited (143)` this command still
// printed "container running with 2048" and told the operator to force an
// idle-TTL stop or `restart-agents` — both of which have effectively already
// happened. #1681's own fix rebuilds a STOPPED container with the configured
// limits on the next wake, so the honest report is "this is picked up when the
// crew next starts", and the drift is informational rather than actionable.
//
// This is the same class of defect the rest of this PR exists to remove: a
// surface asserting a state it did not check.
func TestCrewContainerStatusRunE_StoppedContainerIsNotDescribedAsRunning(t *testing.T) {
	stub := covSetupCli4(t)
	stub.OnGet("/api/v1/crews/"+covCrewIDCli4+"/container-status", clitest.JSONResponse(200, map[string]any{
		"crew_id": covCrewIDCli4, "status": "stopped",
		"configured_memory_mb": 6144, "configured_cpus": 2.0,
		"effective_memory_mb": 2048, "effective_cpus": 2.0,
		"config_drift": []map[string]any{
			{"field": "container_memory_mb", "configured": 6144, "effective": 2048},
		},
	}))

	c := covFreshCmd(crewContainerStatusCmd, nil)
	out, err := covCaptureStdoutCli4(t, func() error { return c.RunE(c, []string{covCrewIDCli4}) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	// The gap itself is still reported — the container really does carry 2048.
	if !strings.Contains(out, "container_memory_mb") || !strings.Contains(out, "6144") || !strings.Contains(out, "2048") {
		t.Errorf("the drift is no longer reported at all: %q", out)
	}
	if strings.Contains(out, "running with") {
		t.Errorf("a stopped container is described as running: %q", out)
	}
	if strings.Contains(out, "on a running one") {
		t.Errorf("the explanation asserts the container is running when it is stopped: %q", out)
	}
	if strings.Contains(out, "restart-agents") || strings.Contains(out, "idle-TTL") {
		t.Errorf("a stopped container is told to force the recreation it already gets for free: %q", out)
	}
	if !strings.Contains(out, "next time it starts") {
		t.Errorf("the report does not say the crew fixes itself on the next start: %q", out)
	}
}

// The running case keeps its present-tense wording and its remedy: it is the
// one state where the operator has to do something.
func TestCrewContainerStatusRunE_RunningContainerKeepsItsRemedy(t *testing.T) {
	stub := covSetupCli4(t)
	stub.OnGet("/api/v1/crews/"+covCrewIDCli4+"/container-status", clitest.JSONResponse(200, map[string]any{
		"crew_id": covCrewIDCli4, "status": "running",
		"configured_memory_mb": 6144, "configured_cpus": 2.0,
		"effective_memory_mb": 2048, "effective_cpus": 2.0,
		"config_drift": []map[string]any{
			{"field": "container_memory_mb", "configured": 6144, "effective": 2048},
		},
	}))

	c := covFreshCmd(crewContainerStatusCmd, nil)
	out, err := covCaptureStdoutCli4(t, func() error { return c.RunE(c, []string{covCrewIDCli4}) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "running with") {
		t.Errorf("the running container lost the wording that says it is still serving: %q", out)
	}
	if !strings.Contains(out, "restart-agents") {
		t.Errorf("the remedy is not named for the one state that needs it: %q", out)
	}
}

// #1681 adds configured_* / effective_* / config_drift to the API. Core rule #3
// makes the CLI the supported way for an agent to drive Crewship, so a field
// only a human can read is half-shipped — and this command ignored --format
// entirely, printing its prose block (ANSI included) for `-f json` too.
func TestCrewContainerStatusRunE_JSONFormatEmitsTheLimitFields(t *testing.T) {
	stub := covSetupCli4(t)
	stub.OnGet("/api/v1/crews/"+covCrewIDCli4+"/container-status", clitest.JSONResponse(200, map[string]any{
		"crew_id": covCrewIDCli4, "status": "running", "runtime_contract": "current",
		"configured_memory_mb": 6144, "configured_cpus": 2.0,
		"effective_memory_mb": 2048, "effective_cpus": 2.0,
		"config_drift": []map[string]any{
			{"field": "container_memory_mb", "configured": 6144, "effective": 2048},
		},
	}))

	origFormat := flagFormat
	flagFormat = "json"
	t.Cleanup(func() { flagFormat = origFormat })

	c := covFreshCmd(crewContainerStatusCmd, nil)
	out, err := covCaptureStdoutCli4(t, func() error { return c.RunE(c, []string{covCrewIDCli4}) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if strings.Contains(out, "Container:") {
		t.Errorf("--format json printed the human block: %q", out)
	}

	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("--format json did not emit JSON (%v): %q", err, out)
	}
	for _, key := range []string{
		"status", "configured_memory_mb", "configured_cpus",
		"effective_memory_mb", "effective_cpus", "config_drift",
	} {
		if _, ok := got[key]; !ok {
			t.Errorf("%q is missing from the JSON payload, so it is unreachable from the CLI: %v", key, got)
		}
	}
	if got["configured_memory_mb"] != float64(6144) || got["effective_memory_mb"] != float64(2048) {
		t.Errorf("the limit figures did not survive the round trip: %v", got)
	}
	drift, ok := got["config_drift"].([]any)
	if !ok || len(drift) != 1 {
		t.Fatalf("config_drift is not a one-entry list: %v", got["config_drift"])
	}
	entry, _ := drift[0].(map[string]any)
	if entry["field"] != "container_memory_mb" {
		t.Errorf("the drift entry lost its field name: %v", entry)
	}
}

// yaml goes through the same routing, so a reader that asked for yaml does not
// silently get the prose block either.
func TestCrewContainerStatusRunE_YAMLFormatIsHonoured(t *testing.T) {
	stub := covSetupCli4(t)
	stub.OnGet("/api/v1/crews/"+covCrewIDCli4+"/container-status", clitest.JSONResponse(200, map[string]any{
		"crew_id": covCrewIDCli4, "status": "stopped",
		"configured_memory_mb": 6144, "effective_memory_mb": 2048,
	}))

	origFormat := flagFormat
	flagFormat = "yaml"
	t.Cleanup(func() { flagFormat = origFormat })

	c := covFreshCmd(crewContainerStatusCmd, nil)
	out, err := covCaptureStdoutCli4(t, func() error { return c.RunE(c, []string{covCrewIDCli4}) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if strings.Contains(out, "Container:") {
		t.Errorf("--format yaml printed the human block: %q", out)
	}
	if !strings.Contains(out, "configured_memory_mb: 6144") {
		t.Errorf("yaml output does not carry the configured limit: %q", out)
	}
	if !strings.Contains(out, "effective_memory_mb: 2048") {
		t.Errorf("yaml output does not carry the effective limit: %q", out)
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
