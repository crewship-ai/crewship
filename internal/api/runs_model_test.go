package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// emitRunRowWithModel writes a run.started + run.completed pair where the
// terminal entry's metadata records the actually-resolved model — mirrors
// what the run driver persists via UpdateRun's completedMeta.
func (f *runsTestFixture) emitRunRowWithModel(t *testing.T, traceID, model string, when time.Time) {
	t.Helper()
	ctx := context.Background()
	insertJournal := func(id, kind string, ts time.Time, payload string) {
		_, err := f.h.db.ExecContext(ctx, `
			INSERT INTO journal_entries
				(id, workspace_id, agent_id, ts, entry_type, severity, priority, actor_type, actor_id, summary, payload, refs, trace_id)
			VALUES (?, ?, ?, ?, ?, 'info', 'normal', 'sidecar', ?, 'r', ?, '{}', ?)`,
			id, f.wsID, f.agent, ts.UTC().Format("2006-01-02T15:04:05.000Z"),
			kind, f.agent, payload, traceID)
		if err != nil {
			t.Fatalf("insert %s/%s: %v", kind, traceID, err)
		}
	}
	insertJournal(traceID+"_s", "run.started", when, `{"trigger_type":"USER"}`)
	insertJournal(traceID+"_t", "run.completed", when.Add(time.Minute),
		`{"exit_code":0,"metadata":{"model":"`+model+`"}}`)
}

// TestRunHandler_List_SurfacesModel asserts the resolved model reaches the
// run JSON the CLI (`crewship inspect`) reads.
func TestRunHandler_List_SurfacesModel(t *testing.T) {
	f := newRunsTestFixture(t)
	now := time.Now().UTC()
	f.emitRunRowWithModel(t, "run_m", "claude-opus-4-8", now.Add(-2*time.Minute))
	f.emitRunRow(t, "run_plain", "COMPLETED", "USER", now.Add(-1*time.Minute))

	req := httptest.NewRequest("GET", "/api/v1/runs", nil)
	req = withWorkspaceUser(req, f.user, f.wsID, "OWNER")
	rr := httptest.NewRecorder()
	f.h.List(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var resp runListResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	byID := map[string]runResponse{}
	for _, r := range resp.Data {
		byID[r.ID] = r
	}
	rm, ok := byID["run_m"]
	if !ok {
		t.Fatalf("run_m missing from response: %+v", resp.Data)
	}
	if rm.Model == nil || *rm.Model != "claude-opus-4-8" {
		t.Errorf("run_m.model = %v, want claude-opus-4-8", rm.Model)
	}
	// A run with no recorded model omits the field (nil), never errors.
	if plain, ok := byID["run_plain"]; !ok || plain.Model != nil {
		t.Errorf("run_plain.model = %v, want nil", plain.Model)
	}
}
