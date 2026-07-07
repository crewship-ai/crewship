package main

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

func TestPrettyOutput(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain text unchanged", "just a summary", "just a summary"},
		{"empty stays empty", "", ""},
		{"json object indented", `{"a":1,"b":2}`, "{\n  \"a\": 1,\n  \"b\": 2\n}"},
		{"json array indented", `[1,2]`, "[\n  1,\n  2\n]"},
		{"invalid json verbatim", `{not json`, `{not json`},
		{"leading brace but text", "{oops} trailing", "{oops} trailing"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := prettyOutput(tc.in); got != tc.want {
				t.Errorf("prettyOutput(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

const covResultPath = "/api/v1/workspaces/" + covWorkspaceIDCli10 + "/pipeline-runs/run_x"

func TestRoutineResultRunE_PrintsDeliverable(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet(covResultPath, clitest.JSONResponse(200, cli.PipelineRunDetail{
		ID: "run_x", Status: "completed", Output: "The quarterly report is ready.",
		DurationMs: 1200, CostUSD: 0.0042,
	}))
	covSetupCli10(t, s.URL())

	out, err := captureStdoutCovCli10(t, func() error {
		return routineResultCmd.RunE(routineResultCmd, []string{"run_x"})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "COMPLETED") || !strings.Contains(out, "Final output:") {
		t.Errorf("status/header missing:\n%s", out)
	}
	if !strings.Contains(out, "The quarterly report is ready.") {
		t.Errorf("deliverable missing:\n%s", out)
	}
}

func TestRoutineResultRunE_PrettyPrintsJSONOutput(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet(covResultPath, clitest.JSONResponse(200, cli.PipelineRunDetail{
		ID: "run_x", Status: "completed", Output: `{"invoices":3,"total":41.5}`,
	}))
	covSetupCli10(t, s.URL())

	out, err := captureStdoutCovCli10(t, func() error {
		return routineResultCmd.RunE(routineResultCmd, []string{"run_x"})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	// Indented JSON, not the blind single-line form.
	if !strings.Contains(out, "\"invoices\": 3") {
		t.Errorf("structured output was not pretty-printed:\n%s", out)
	}
}

func TestRoutineResultRunE_FormatJSON(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet(covResultPath, clitest.JSONResponse(200, cli.PipelineRunDetail{
		ID: "run_x", Status: "completed", Output: "done",
	}))
	covSetupCli10(t, s.URL())
	// Output routes through the global --format flag (no local --json).
	flagFormat = "json"

	out, err := captureStdoutCovCli10(t, func() error {
		return routineResultCmd.RunE(routineResultCmd, []string{"run_x"})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, `"output": "done"`) {
		t.Errorf("json envelope missing output field:\n%s", out)
	}
}

func TestRoutineResultRunE_FailedRun(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet(covResultPath, clitest.JSONResponse(200, cli.PipelineRunDetail{
		ID: "run_x", Status: "failed", Output: "",
		ErrorMessage: "step summarize timed out", FailedAtStep: "summarize",
	}))
	covSetupCli10(t, s.URL())

	out, err := captureStdoutCovCli10(t, func() error {
		return routineResultCmd.RunE(routineResultCmd, []string{"run_x"})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "FAILED") {
		t.Errorf("status line missing FAILED:\n%s", out)
	}
	if !strings.Contains(out, "step summarize timed out") || !strings.Contains(out, "summarize") {
		t.Errorf("error message / failed step missing:\n%s", out)
	}
	if !strings.Contains(out, "no final output recorded") {
		t.Errorf("failed run with no output should say so:\n%s", out)
	}
}

func TestRoutineResultRunE_NoOutputTerminal(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet(covResultPath, clitest.JSONResponse(200, cli.PipelineRunDetail{
		ID: "run_x", Status: "completed", Output: "",
	}))
	covSetupCli10(t, s.URL())

	out, err := captureStdoutCovCli10(t, func() error {
		return routineResultCmd.RunE(routineResultCmd, []string{"run_x"})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "no final output recorded") {
		t.Errorf("empty-deliverable message missing:\n%s", out)
	}
}

func TestRoutineResultRunE_NoOutputRunning(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet(covResultPath, clitest.JSONResponse(200, cli.PipelineRunDetail{
		ID: "run_x", Status: "running", Output: "",
	}))
	covSetupCli10(t, s.URL())

	out, err := captureStdoutCovCli10(t, func() error {
		return routineResultCmd.RunE(routineResultCmd, []string{"run_x"})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "no final output yet") {
		t.Errorf("in-flight message missing:\n%s", out)
	}
}

func TestRoutineResultRunE_ServerError(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet(covResultPath, clitest.ErrorResponse(500, "boom"))
	covSetupCli10(t, s.URL())
	if err := routineResultCmd.RunE(routineResultCmd, []string{"run_x"}); err == nil {
		t.Error("expected error from 500")
	}
}

func TestRoutineResultRunE_NoAuth(t *testing.T) {
	covSetupCli10(t, "http://127.0.0.1:0")
	cliCfg = &cli.CLIConfig{}
	if err := routineResultCmd.RunE(routineResultCmd, []string{"run_x"}); err == nil ||
		!strings.Contains(err.Error(), "not logged in") {
		t.Errorf("expected not-logged-in, got %v", err)
	}
}
