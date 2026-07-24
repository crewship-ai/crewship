package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

// #1426 (3.1) — cancelling a parked WAITING run must succeed. The run
// released its slot + registry entry when it parked, so the in-memory scan
// 404s; the handler falls back to the run store, marks the row cancelled and
// cancels its pending waitpoint so the inbox approval card stops being
// actionable.
func TestPipelineRuns_CancelRun_ParkedWaitingRun(t *testing.T) {
	h, db, userID, wsID := runsHandlerRig(t)
	ctx := context.Background()

	registry := pipeline.NewRunRegistry() // empty — the run is not live here
	h.SetRunRegistry(registry)
	runStore := pipeline.NewRunStore(db)
	h.SetRunStore(runStore)
	wpStore := pipeline.NewSQLWaitpointStore(db)
	defer wpStore.Close()
	h.SetWaitpointStore(wpStore)

	const runID = "prn_parked"
	// A real pipelines row (pipeline_runs.pipeline_id FKs to it).
	if _, err := db.ExecContext(ctx, `
INSERT INTO pipelines (id, workspace_id, slug, name, definition_json, definition_hash, ephemeral, workspace_visible, author_crew_id, author_agent_id, authored_via, last_test_run_at, last_test_run_passed, created_at, updated_at)
VALUES ('pln_x', ?, 'x', 'x', '{"name":"x","steps":[]}', 'h', 0, 1, NULL, NULL, 'agent_tool_call', datetime('now'), 1, datetime('now'), datetime('now'))`, wsID); err != nil {
		t.Fatalf("seed pipeline: %v", err)
	}
	if err := runStore.Insert(ctx, &pipeline.RunRecord{
		ID: runID, WorkspaceID: wsID, PipelineID: "pln_x", PipelineSlug: "x",
		Status: pipeline.RunStatusWaiting, Mode: pipeline.ModeRun,
	}); err != nil {
		t.Fatalf("insert waiting run: %v", err)
	}
	token, err := wpStore.CreateApproval(ctx, pipeline.WaitpointApprovalRequest{
		WorkspaceID: wsID, PipelineRunID: runID, StepID: "gate", Prompt: "ship it?",
	})
	if err != nil {
		t.Fatalf("create approval: %v", err)
	}

	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/workspaces/"+wsID+"/pipelines/runs/"+runID+"/cancel", nil),
		userID, wsID, "OWNER",
	)
	req.SetPathValue("runId", runID)
	rr := httptest.NewRecorder()
	h.CancelRun(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}

	rec, gerr := runStore.Get(ctx, runID)
	if gerr != nil {
		t.Fatalf("get run: %v", gerr)
	}
	if rec.Status != pipeline.RunStatusCancelled {
		t.Errorf("run status = %q, want cancelled", rec.Status)
	}
	var wpStatus string
	if err := db.QueryRowContext(ctx,
		`SELECT status FROM pipeline_waitpoints WHERE token = ?`, token).Scan(&wpStatus); err != nil {
		t.Fatalf("read waitpoint: %v", err)
	}
	if wpStatus != "cancelled" {
		t.Errorf("waitpoint status = %q, want cancelled", wpStatus)
	}
}
