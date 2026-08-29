package main

// Coverage for `routine bulk-replay` (cmd_routine_replay.go,
// routineBulkReplayCmd). Fixture shaped from
// PipelineHandler.BulkReplayRuns in internal/api/pipeline_runs_replay.go,
// which replies {"requested": N, "replayed": N, "results": [...]} — the
// exact struct the CLI decodes, so the response body below is a literal
// transcription of that handler's writeJSON call rather than of the CLI's
// own decode struct.

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

func TestRoutineBulkReplayRunE_RequiresFingerprint(t *testing.T) {
	covSetupCli5(t)
	err := routineBulkReplayCmd.RunE(routineBulkReplayCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "--fingerprint required") {
		t.Errorf("want fingerprint-required error, got %v", err)
	}
}

func TestRoutineBulkReplayRunE_Happy(t *testing.T) {
	stub := covSetupCli5(t)
	path := "/api/v1/workspaces/" + covWSCli5 + "/pipelines/runs/bulk_replay"
	stub.OnPost(path, clitest.JSONResponse(200, map[string]any{
		"requested": 3, "replayed": 2,
		"results": []map[string]any{
			{"source_run_id": "run_1", "new_run_id": "run_10", "status": "COMPLETED"},
			{"source_run_id": "run_2", "new_run_id": "run_11", "status": "FAILED"},
			{"source_run_id": "run_3", "error": "already replaying"},
		},
	}))
	if err := routineBulkReplayCmd.Flags().Set("fingerprint", "fp_abc123"); err != nil {
		t.Fatal(err)
	}
	if err := routineBulkReplayCmd.Flags().Set("limit", "10"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = routineBulkReplayCmd.Flags().Set("fingerprint", "")
		_ = routineBulkReplayCmd.Flags().Set("limit", "50")
	})

	var runErr error
	out := covCaptureStdoutCli5(t, func() {
		runErr = routineBulkReplayCmd.RunE(routineBulkReplayCmd, nil)
	})
	if runErr != nil {
		t.Fatalf("RunE: %v", runErr)
	}
	if !strings.Contains(out, "2/3 runs re-triggered for fingerprint fp_abc123") {
		t.Errorf("output = %q", out)
	}

	calls := stub.CallsFor("POST", path)
	if len(calls) != 1 {
		t.Fatalf("want 1 POST call, got %d", len(calls))
	}
	body := covJSONBody(t, calls[0].Body)
	if body["fingerprint"] != "fp_abc123" {
		t.Errorf("fingerprint = %v", body["fingerprint"])
	}
	if body["limit"] != float64(10) {
		t.Errorf("limit = %v, want 10", body["limit"])
	}
}

func TestRoutineBulkReplayRunE_ServerError(t *testing.T) {
	stub := covSetupCli5(t)
	path := "/api/v1/workspaces/" + covWSCli5 + "/pipelines/runs/bulk_replay"
	stub.OnPost(path, clitest.ErrorResponse(500, "run store not wired"))
	if err := routineBulkReplayCmd.Flags().Set("fingerprint", "fp_xyz"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = routineBulkReplayCmd.Flags().Set("fingerprint", "") })

	err := routineBulkReplayCmd.RunE(routineBulkReplayCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "run store not wired") {
		t.Errorf("want server error surfaced, got %v", err)
	}
}
