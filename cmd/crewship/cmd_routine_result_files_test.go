package main

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

// #839 — `routine result` appends a "Files produced:" section sourced from
// the run→files endpoint.

const covResultFilesPath = covResultPath + "/files"

func TestRoutineResultRunE_ListsProducedFiles(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet(covResultPath, clitest.JSONResponse(200, cli.PipelineRunDetail{
		ID: "run_x", Status: "completed", Output: "The report is ready.",
	}))
	s.OnGet(covResultFilesPath, clitest.JSONResponse(200, cli.RunFilesResult{
		CrewID: "crew1",
		Files: []cli.RunFile{
			{Name: "report.pdf", Size: 1234, Path: "crew1/writer/report.pdf", ModTime: "2026-07-01T10:30:00Z"},
			{Name: "summary.md", Size: 340, Path: "crews/crew1/shared/summary.md", ModTime: "2026-07-01T10:20:00Z"},
		},
	}))
	covSetupCli10(t, s.URL())

	out, err := captureStdoutCovCli10(t, func() error {
		return routineResultCmd.RunE(routineResultCmd, []string{"run_x"})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	for _, want := range []string{"Files produced:", "report.pdf", "summary.md", "crew1/writer/report.pdf"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in output:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "crew files get crew1") {
		t.Errorf("fetch hint missing:\n%s", out)
	}
}

func TestRoutineResultRunE_NoFilesTerminalSaysNone(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet(covResultPath, clitest.JSONResponse(200, cli.PipelineRunDetail{
		ID: "run_x", Status: "completed", Output: "done",
	}))
	s.OnGet(covResultFilesPath, clitest.JSONResponse(200, cli.RunFilesResult{CrewID: "crew1", Files: []cli.RunFile{}}))
	covSetupCli10(t, s.URL())

	out, err := captureStdoutCovCli10(t, func() error {
		return routineResultCmd.RunE(routineResultCmd, []string{"run_x"})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "Files produced: (none)") {
		t.Errorf("expected '(none)' for a finished run with no files:\n%s", out)
	}
}

// The files section is best-effort: a failing /files call must not fail
// the command — the deliverable output still prints.
func TestRoutineResultRunE_FilesEndpointErrorIsSwallowed(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet(covResultPath, clitest.JSONResponse(200, cli.PipelineRunDetail{
		ID: "run_x", Status: "completed", Output: "still delivered",
	}))
	s.OnGet(covResultFilesPath, clitest.ErrorResponse(500, "files boom"))
	covSetupCli10(t, s.URL())

	out, err := captureStdoutCovCli10(t, func() error {
		return routineResultCmd.RunE(routineResultCmd, []string{"run_x"})
	})
	if err != nil {
		t.Fatalf("RunE should not fail on a files-endpoint error: %v", err)
	}
	if !strings.Contains(out, "still delivered") {
		t.Errorf("deliverable output missing:\n%s", out)
	}
	if strings.Contains(out, "Files produced:") {
		t.Errorf("no files section should render on a fetch error:\n%s", out)
	}
}
