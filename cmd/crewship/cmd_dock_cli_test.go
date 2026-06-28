package main

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

// TestIssueRunsRunE drives `issue runs <identifier>` against a stubbed
// runs endpoint and asserts the table surfaces id / status / result.
func TestIssueRunsRunE(t *testing.T) {
	s := covSetup(t)
	covStubIssue(s)
	path := "/api/v1/crews/" + covCrewID + "/issues/" + covIssueIdent + "/runs"
	s.OnGet(path, clitest.JSONResponse(200, []map[string]any{
		{"id": "asg_a", "status": "COMPLETED", "agent_name": "Sam", "task": "Write report",
			"started_at": "2026-06-01T10:00:00Z", "duration_ms": 3041, "result_summary": "wrote report"},
		{"id": "asg_b", "status": "FAILED", "agent_name": "Riley", "task": "Trace DNS",
			"started_at": "2026-06-01T09:00:00Z", "duration_ms": 520, "error_message": "boom"},
	}))
	out, err := covCaptureStdout(t, func() error {
		return issueRunsCmd.RunE(issueRunsCmd, []string{covIssueIdent})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	for _, want := range []string{"Sam", "Riley", "COMPLETED", "FAILED", "boom", "wrote report"} {
		if !strings.Contains(out, want) {
			t.Errorf("runs table missing %q:\n%s", want, out)
		}
	}
}

// TestRoutineLogsRunE_FullTimeline exercises the slug-free `--full` path
// that hits the new /pipeline-runs/{runId}/logs endpoint.
func TestRoutineLogsRunE_FullTimeline(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli8(t, stub.URL())
	stub.OnGet("/api/v1/workspaces/"+covWSCli8+"/pipeline-runs/run_full/logs",
		clitest.JSONResponse(200, []map[string]any{
			{"ts": "2026-06-01T10:00:01Z", "level": "info", "message": "started", "type": "pipeline.run.started"},
			{"ts": "2026-06-01T10:00:09Z", "level": "error", "message": "kaboom", "type": "pipeline.run.failed"},
		}))

	if err := routineLogsCmd.Flags().Set("full", "true"); err != nil {
		t.Fatalf("set full: %v", err)
	}
	defer func() { _ = routineLogsCmd.Flags().Set("full", "false") }()

	out := covCaptureStdoutCli8(t, func() {
		if err := routineLogsCmd.RunE(routineLogsCmd, []string{"run_full"}); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	for _, want := range []string{"started", "kaboom", "error", "run.started"} {
		if !strings.Contains(out, want) {
			t.Errorf("full timeline missing %q:\n%s", want, out)
		}
	}
}
