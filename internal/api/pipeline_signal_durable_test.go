package api

// Durable wait:event delivery through the HTTP signal endpoint (#1409).
//
// SignalRun used to deliver ONLY through the in-memory SignalRegistry —
// nothing durable, so a signal aimed at a run that had already parked
// (no live goroutine registered) or a process restart between park and
// delivery would 404 or vanish silently. This test drives the real
// endpoint end-to-end: a top-level wait:event step parks the run, the
// HTTP signal call durably delivers + un-parks it via
// Executor.ResumeAfterSignal, and the run completes with the delivered
// payload as the step's output.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

const signalEventWaitDSL = `{
  "dsl_version": "1.0",
  "name": "event-wait",
  "steps": [
    {"id": "gate", "type": "wait", "wait": {"kind": "event", "event_type": "approve"}, "timeout_seconds": 3600}
  ]
}`

func TestSignalRun_DeliversDurably_ResumesParkedRun(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	logger := testLogger()
	h := NewPipelineHandler(db, logger, nil, nil)
	h.SetRunner(&stubRunner{output: "unused"})
	h.SetRunStore(pipeline.NewRunStore(db))
	h.SetSignalRegistry(pipeline.NewSignalRegistry())

	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := db.Exec(`
		INSERT INTO pipelines (id, workspace_id, slug, name, definition_json, definition_hash, created_at, updated_at, last_test_run_at)
		VALUES ('pln_evt', ?, 'event-wait', 'event-wait', ?, 'hash', ?, ?, ?)`,
		wsID, signalEventWaitDSL, now, now, now); err != nil {
		t.Fatalf("seed pipeline: %v", err)
	}

	res, err := h.newExecutor().Run(context.Background(), pipeline.RunInput{
		PipelineID:  "pln_evt",
		WorkspaceID: wsID,
		Mode:        pipeline.ModeRun,
	})
	if err != nil {
		t.Fatalf("start run: %v", err)
	}
	if res.Status != "WAITING" {
		t.Fatalf("status = %q, want WAITING (top-level wait:event must park)", res.Status)
	}

	body := `{"event_type":"approve","payload":"approved-by-test"}`
	req := httptest.NewRequest("POST", "/api/v1/workspaces/"+wsID+"/pipeline-runs/"+res.RunID+"/signal", strings.NewReader(body))
	req.SetPathValue("runId", res.RunID)
	req = withWorkspaceCtx(req, wsID)
	rr := httptest.NewRecorder()
	h.SignalRun(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["delivered"] != true {
		t.Errorf("delivered = %v, want true", resp["delivered"])
	}

	runStore := pipeline.NewRunStore(db)
	deadline := time.Now().Add(3 * time.Second)
	var rec *pipeline.RunRecord
	for time.Now().Before(deadline) {
		rec, err = runStore.Get(context.Background(), res.RunID)
		if err != nil {
			t.Fatalf("get run: %v", err)
		}
		if rec.Status == "completed" || rec.Status == "failed" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if rec.Status != "completed" {
		t.Fatalf("final run status = %q (error=%q), want completed", rec.Status, rec.ErrorMessage)
	}
	var outputs map[string]string
	if err := json.Unmarshal([]byte(rec.StepOutputsJSON), &outputs); err != nil {
		t.Fatalf("unmarshal step outputs: %v", err)
	}
	if outputs["gate"] != "approved-by-test" {
		t.Errorf("gate step output = %q, want approved-by-test", outputs["gate"])
	}
}

// TestSignalRun_NoWaitingRun_Returns404 pins the existing "nothing to
// deliver to" contract: durable Deliver reports armed=false (no pending
// wait row), the in-memory registry also has nothing live, so the
// endpoint still 404s exactly as before #1409.
func TestSignalRun_NoWaitingRun_Returns404(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	logger := testLogger()
	h := NewPipelineHandler(db, logger, nil, nil)
	h.SetRunStore(pipeline.NewRunStore(db))
	h.SetSignalRegistry(pipeline.NewSignalRegistry())

	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := db.Exec(`
		INSERT INTO pipelines (id, workspace_id, slug, name, definition_json, definition_hash, created_at, updated_at, last_test_run_at)
		VALUES ('pln_x', ?, 'x', 'x', '{"name":"x","steps":[]}', 'hash', ?, ?, ?)`,
		wsID, now, now, now); err != nil {
		t.Fatalf("seed pipeline: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO pipeline_runs (id, workspace_id, pipeline_id, pipeline_slug, status, mode, started_at, step_outputs_json, inputs_json, triggered_via, created_at, updated_at)
		VALUES ('run_idle', ?, 'pln_x', 'x', 'running', 'run', ?, '{}', '{}', 'manual', ?, ?)`,
		wsID, now, now, now); err != nil {
		t.Fatalf("seed run: %v", err)
	}

	body := `{"event_type":"approve","payload":"x"}`
	req := httptest.NewRequest("POST", "/api/v1/workspaces/"+wsID+"/pipeline-runs/run_idle/signal", strings.NewReader(body))
	req.SetPathValue("runId", "run_idle")
	req = withWorkspaceCtx(req, wsID)
	rr := httptest.NewRecorder()
	h.SignalRun(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404; body=%s", rr.Code, rr.Body.String())
	}
}
