package api

import (
	"net/http/httptest"
	"testing"
)

func TestRunExecutionsWaitAndWork(t *testing.T) {
	h, db, user, ws := runsHandlerRig(t)
	seedRunsPipeline(t, db, ws, "pl_wait", "wait")
	seedRunRow(t, db, ws, "pl_wait", "wait", "run_wait", "completed")
	for _, row := range []struct {
		id   string
		wait any
	}{{"legacy", nil}, {"wait", int64(3000)}, {"free", int64(0)}} {
		execOrFatal(t, db, `INSERT INTO pipeline_step_executions(id,run_id,step_id,execution_path,attempt,kind,status,started_at,ended_at,agent_wait_ms) VALUES (?,'run_wait','agent',?,1,'agent_attempt','completed','2026-11-11T00:00:00Z','2026-11-11T00:00:05Z',?)`, row.id, "/work/"+row.id, row.wait)
	}
	req := withWorkspaceUser(httptest.NewRequest("GET", "/x", nil), user, ws, "OWNER")
	req.SetPathValue("runId", "run_wait")
	rr := httptest.NewRecorder()
	h.RunExecutions(rr, req)
	if rr.Code != 200 {
		t.Fatalf("%d %s", rr.Code, rr.Body)
	}
	got := decodeJSONMap(t, rr.Body.String())["rows"].([]any)
	legacy := got[0].(map[string]any)
	waited := got[1].(map[string]any)
	free := got[2].(map[string]any)
	if legacy["agent_wait_ms"] != nil || legacy["work_duration_ms"] != nil {
		t.Fatalf("invented legacy timing: %v", legacy)
	}
	if waited["agent_wait_ms"] != float64(3000) || waited["work_duration_ms"] != float64(2000) {
		t.Fatalf("timing %v", waited)
	}
	if free["agent_wait_ms"] != float64(0) || free["work_duration_ms"] != float64(5000) {
		t.Fatalf("free timing %v", free)
	}
}
