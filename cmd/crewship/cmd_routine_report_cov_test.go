package main

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

const covReportRunPath = "/api/v1/workspaces/" + covWorkspaceIDCli10 + "/pipeline-runs/run_r"
const covReportEventsPath = "/api/v1/workspaces/" + covWorkspaceIDCli10 + "/pipelines/acct-routine/runs"

func stubReportRun(s *clitest.StubServer) {
	s.OnGet(covReportRunPath, clitest.JSONResponse(200, cli.PipelineRunDetail{
		ID: "run_r", PipelineSlug: "acct-routine", PipelineName: "Acct Routine",
		Status: "completed", Output: "Reconciled 42 rows.", CostUSD: 0.003, DurationMs: 2000,
		Inputs:      map[string]any{"month": "June"},
		StepOutputs: map[string]any{"parse": "42 rows", "verify": "balanced"},
	}))
	s.OnGet(covReportEventsPath, clitest.JSONResponse(200, []watchEntry{
		ev("run_r", "pipeline.step.completed", "parse", "2026-07-07T12:00:10Z", map[string]any{"cost_usd": 0.002}),
		ev("run_r", "pipeline.step.completed", "verify", "2026-07-07T12:00:20Z", map[string]any{"cost_usd": 0.001}),
	}))
}

func TestRoutineReportRunE_Markdown(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	stubReportRun(s)
	covSetupCli10(t, s.URL())
	defer func() { reportClient = false; reportOutFile = "" }()

	out, err := captureStdoutCovCli10(t, func() error {
		return routineReportCmd.RunE(routineReportCmd, []string{"run_r"})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	for _, want := range []string{"Acct Routine", "June", "parse", "verify", "42 rows", "Reconciled 42 rows", "$0.0030"} {
		if !strings.Contains(out, want) {
			t.Errorf("report missing %q:\n%s", want, out)
		}
	}
}

func TestRoutineReportRunE_ClientRedacts(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	stubReportRun(s)
	covSetupCli10(t, s.URL())
	reportClient = true
	defer func() { reportClient = false; reportOutFile = "" }()

	out, err := captureStdoutCovCli10(t, func() error {
		return routineReportCmd.RunE(routineReportCmd, []string{"run_r"})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "Reconciled 42 rows") || !strings.Contains(out, "Succeeded") {
		t.Errorf("client report dropped deliverable/status:\n%s", out)
	}
	if strings.Contains(out, "run_r") || strings.Contains(out, "$0.00") {
		t.Errorf("client report leaked run-id / cost:\n%s", out)
	}
}

func TestRoutineResultRunE_ClientView(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet("/api/v1/workspaces/"+covWorkspaceIDCli10+"/pipeline-runs/run_c", clitest.JSONResponse(200, cli.PipelineRunDetail{
		ID: "run_c", PipelineName: "Acct", Status: "completed", Output: "The report is ready.",
		CostUSD: 0.5, DurationMs: 9000,
	}))
	covSetupCli10(t, s.URL())
	resultClient = true
	defer func() { resultClient = false }()

	out, err := captureStdoutCovCli10(t, func() error {
		return routineResultCmd.RunE(routineResultCmd, []string{"run_c"})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "Acct — Succeeded") || !strings.Contains(out, "The report is ready.") {
		t.Errorf("client result view wrong:\n%s", out)
	}
	if strings.Contains(out, "run_c") || strings.Contains(out, "$0.5") || strings.Contains(out, "9000") {
		t.Errorf("client result leaked run-id/cost/duration:\n%s", out)
	}
}
