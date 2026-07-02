package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

// routine run --wait: a WAITING receipt keeps polling until the approval
// resolves and the run completes; without --wait the receipt returns
// immediately (existing behaviour, pinned here so --wait can't regress it).

func TestRoutineRunWait_WaitingRunCompletes(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()

	s.OnPost("/api/v1/workspaces/"+covWorkspaceIDCli10+"/pipelines/deploy-gate/run",
		clitest.JSONResponse(200, map[string]any{
			"run_id": "prun_w", "status": "WAITING",
			"waitpoint_token": "wp_tok", "current_step": "approve",
		}))
	s.OnGet("/api/v1/workspaces/"+covWorkspaceIDCli10+"/pipeline-runs/prun_w",
		clitest.JSONResponse(200, map[string]any{
			"id": "prun_w", "status": "completed", "output": "shipped",
			"cost_usd": 0.01, "duration_ms": 1200,
		}))

	covSetupCli10(t, s.URL())
	setFlagCovCli10(t, pipelineRunCmd, "wait", "true")
	setFlagCovCli10(t, pipelineRunCmd, "wait-timeout", (5 * time.Second).String())
	pipelineRunCmd.SetContext(context.Background())

	out, err := captureStdoutCovCli10(t, func() error {
		return pipelineRunCmd.RunE(pipelineRunCmd, []string{"deploy-gate"})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "COMPLETED") {
		t.Errorf("expected final COMPLETED outcome, got: %q", out)
	}
	if !strings.Contains(out, "shipped") {
		t.Errorf("expected final output in stdout, got: %q", out)
	}
	if got := len(s.CallsFor("GET", "/api/v1/workspaces/"+covWorkspaceIDCli10+"/pipeline-runs/prun_w")); got < 1 {
		t.Errorf("pipeline-run polls = %d, want >= 1", got)
	}
}

func TestRoutineRunWithoutWait_WaitingReturnsReceiptImmediately(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()

	s.OnPost("/api/v1/workspaces/"+covWorkspaceIDCli10+"/pipelines/deploy-gate/run",
		clitest.JSONResponse(200, map[string]any{
			"run_id": "prun_x", "status": "WAITING",
			"waitpoint_token": "wp_tok2", "current_step": "approve",
		}))

	covSetupCli10(t, s.URL())
	pipelineRunCmd.SetContext(context.Background())

	out, err := captureStdoutCovCli10(t, func() error {
		return pipelineRunCmd.RunE(pipelineRunCmd, []string{"deploy-gate"})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "wp_tok2") {
		t.Errorf("expected waitpoint token in receipt, got: %q", out)
	}
	if got := len(s.CallsFor("GET", "/api/v1/workspaces/"+covWorkspaceIDCli10+"/pipeline-runs/prun_x")); got != 0 {
		t.Errorf("pipeline-run polls = %d, want 0 without --wait", got)
	}
}
