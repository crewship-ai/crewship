package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

func TestRestrictedRoutineReceiptAndWaitJSON(t *testing.T) {
	for _, status := range []string{"receipt", "completed", "failed", "needs_reconciliation", "forbidden"} {
		t.Run(status, func(t *testing.T) {
			s := clitest.NewStubServer()
			defer s.Close()
			covSetupCli10(t, s.URL())
			flagFormat = "json"
			setFlagCovCli10(t, pipelineRunCmd, "wait", "false")
			setFlagCovCli10(t, pipelineRunCmd, "delay", "10")
			setFlagCovCli10(t, pipelineRunCmd, "async", "true")
			pipelineRunCmd.SetContext(context.Background())
			s.OnPost("/api/v1/workspaces/"+covWorkspaceIDCli10+"/pipelines/private/run", clitest.JSONResponse(202, map[string]any{
				"run_id": "private-job", "status": "SCHEDULED", "restricted": true,
			}))
			endpoint := "/api/v1/workspaces/" + covWorkspaceIDCli10 + "/restricted-routine-runs/private-job"
			if status != "receipt" {
				setFlagCovCli10(t, pipelineRunCmd, "wait", "true")
				code := 200
				if status == "forbidden" {
					code = 403
				}
				wireStatus := status
				if status == "forbidden" {
					wireStatus = "completed"
				} // an error body must never become a successful result
				s.OnGet(endpoint, clitest.JSONResponse(code, map[string]any{"run_id": "private-job", "status": wireStatus, "step_outputs": map[string]string{"final": "authorized output"}}))
			}
			out, err := captureStdoutCovCli10(t, func() error { return pipelineRunCmd.RunE(pipelineRunCmd, []string{"private"}) })
			wantErr := status == "failed" || status == "needs_reconciliation" || status == "forbidden"
			if (err != nil) != wantErr {
				t.Fatalf("error = %v", err)
			}
			if status == "forbidden" {
				if strings.TrimSpace(out) != "" {
					t.Fatalf("denied result leaked to stdout: %s", out)
				}
				return
			}
			var got map[string]any
			if err := json.Unmarshal([]byte(out), &got); err != nil {
				t.Fatalf("not one JSON document: %q: %v", out, err)
			}
			if got["run_id"] != "private-job" {
				t.Fatalf("receipt: %v", got)
			}
			if status != "receipt" && got["status"] != status {
				t.Fatalf("did not wait: %v", got)
			}
			if status == "receipt" && len(s.CallsFor("GET", endpoint)) != 0 {
				t.Fatal("polled without --wait")
			}
		})
	}
}

func TestBackupCreateJSONContainsOnlyReceipt(t *testing.T) {
	stub := covSetupCli5(t)
	covResetBackupCreateFlags(t)
	flagFormat = "json"
	covSetFlagCli5(t, backupCreateCmd, "recipient", "age1qqqqqqqq")
	stub.OnPost("/api/v1/admin/backups", clitest.JSONResponse(200, map[string]any{
		"path": "/backup.tar", "scope": "workspace", "missing_container_crews": []string{"offline"},
	}))
	out, err := captureStdoutCovCli10(t, func() error { return backupCreateCmd.RunE(backupCreateCmd, nil) })
	if err != nil {
		t.Fatal(err)
	}
	var receipt map[string]any
	if err := json.Unmarshal([]byte(out), &receipt); err != nil {
		t.Fatalf("invalid JSON: %q: %v", out, err)
	}
	if receipt["path"] != "/backup.tar" || len(receipt["missing_container_crews"].([]any)) != 1 {
		t.Fatalf("lost receipt fields: %v", receipt)
	}
}

func TestRestartAgentlessRuntimeReportsRemoval(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnPost("/api/v1/crews/"+covCrewIDCli5+"/restart-agents", clitest.JSONResponse(200, map[string]any{"restarted": 0, "runtime_removed": true}))
	var err error
	out := covCaptureAll(t, func() { err = crewRestartAgentsCmd.RunE(crewRestartAgentsCmd, []string{covCrewIDCli5}) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "runtime container removed") || strings.Contains(out, "nothing to restart") {
		t.Fatalf("misleading receipt: %s", out)
	}
}
