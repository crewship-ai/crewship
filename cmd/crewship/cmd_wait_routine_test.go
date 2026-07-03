package main

import (
	"context"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

// The wait command's routine-run support: an id unknown to the agent-run
// endpoint falls through to /pipeline-runs/{id}, and --routine skips the
// agent probe entirely. Only COMPLETED/dry_run paths return from RunE
// (other terminal statuses os.Exit), same constraint as cmd_wait_cov_test.

func TestWaitRunE_FallsBackToRoutineRunOn404(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet("/api/v1/runs/prun_1", clitest.ErrorResponse(404, "run not found"))
	s.OnGet("/api/v1/workspaces/"+covWorkspaceIDCli10+"/pipeline-runs/prun_1",
		clitest.JSONResponse(200, map[string]any{
			"id": "prun_1", "status": "completed", "pipeline_slug": "summarize-text",
		}))
	covSetupCli10(t, s.URL())
	waitCmd.SetContext(context.Background())

	out, err := captureStdoutCovCli10(t, func() error {
		return waitCmd.RunE(waitCmd, []string{"prun_1"})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "status=COMPLETED") {
		t.Errorf("done banner missing: %q", out)
	}
	if got := len(s.CallsFor("GET", "/api/v1/runs/prun_1")); got != 1 {
		t.Errorf("agent probe calls = %d, want 1", got)
	}
	if got := len(s.CallsFor("GET", "/api/v1/workspaces/"+covWorkspaceIDCli10+"/pipeline-runs/prun_1")); got != 1 {
		t.Errorf("pipeline poll calls = %d, want 1 (already terminal)", got)
	}
}

func TestWaitRunE_RoutineFlagSkipsAgentProbe(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet("/api/v1/workspaces/"+covWorkspaceIDCli10+"/pipeline-runs/prun_2",
		clitest.JSONResponse(200, map[string]any{
			"id": "prun_2", "status": "dry_run", "pipeline_slug": "s",
		}))
	covSetupCli10(t, s.URL())
	setFlagCovCli10(t, waitCmd, "routine", "true")
	waitCmd.SetContext(context.Background())

	out, err := captureStdoutCovCli10(t, func() error {
		return waitCmd.RunE(waitCmd, []string{"prun_2"})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "status=DRY_RUN") {
		t.Errorf("dry_run banner missing: %q", out)
	}
	if got := len(s.CallsFor("GET", "/api/v1/runs/prun_2")); got != 0 {
		t.Errorf("agent probe calls = %d, want 0 with --routine", got)
	}
}
