package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// seedTestPipeline inserts a minimal pipeline row for the given workspace
// so the routine-binding tests can resolve a routine_id without booting
// the full PipelineHandler stack. Mirrors seedSmokePipeline but uses the
// workspace ID supplied by the issue fixtures rather than the hardcoded
// "ws_smoke".
func seedTestPipeline(t *testing.T, h *IssueHandler, wsID, slug string) string {
	t.Helper()
	id := "pln_routine_" + slug
	now := time.Now().UTC().Format(time.RFC3339Nano)
	def := `{"name":"` + slug + `","steps":[{"id":"a","type":"agent_run","agent_slug":"lead","prompt":"hi"}]}`
	_, err := h.db.ExecContext(context.Background(), `
		INSERT INTO pipelines (id, workspace_id, slug, name, definition_json,
			definition_hash, ephemeral, workspace_visible,
			author_crew_id, author_agent_id, authored_via,
			last_test_run_at, last_test_run_passed, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, 0, 1, NULL, NULL, 'user_api', ?, 1, ?, ?)`,
		id, wsID, slug, slug, def, "hash_"+slug, now, now, now)
	if err != nil {
		t.Fatalf("seed pipeline: %v", err)
	}
	return id
}

// TestIssue_Create_WithRoutineBinding verifies that supplying routine_id
// at create time persists the binding and surfaces it back in the
// response (slug + name JOINed from pipelines). This is the happy path
// for the IA refactor's Issue ↔ Routine integration.
func TestIssue_Create_WithRoutineBinding(t *testing.T) {
	h, userID, wsID, crewID, _, _ := newTestIssueHandler(t)
	routineID := seedTestPipeline(t, h, wsID, "triage")

	body := bytes.NewBufferString(`{
		"title":"Investigate alert",
		"priority":"high",
		"routine_id":"` + routineID + `",
		"routine_inputs":{"alert_id":"a-1"}
	}`)
	req := httptest.NewRequest("POST", "/", body)
	req.SetPathValue("crewId", crewID)
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, wsID, "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.Create(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}

	// Fetch via the workspace-scoped Get to confirm the binding is
	// persisted AND that the LEFT JOIN on pipelines populates
	// routine_slug + routine_name.
	getReq := httptest.NewRequest("GET", "/", nil)
	getReq.SetPathValue("crewId", crewID)
	getReq.SetPathValue("identifier", "ENG-1")
	getCtx := withUser(getReq.Context(), &AuthUser{ID: userID})
	getCtx = withWorkspace(getCtx, wsID, "OWNER")
	getReq = getReq.WithContext(getCtx)
	getRR := httptest.NewRecorder()
	h.Get(getRR, getReq)

	if getRR.Code != http.StatusOK {
		t.Fatalf("get status = %d, body=%s", getRR.Code, getRR.Body.String())
	}
	var got issueResponse
	if err := json.Unmarshal(getRR.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode get: %v", err)
	}
	if got.RoutineID == nil || *got.RoutineID != routineID {
		t.Errorf("routine_id = %v, want %s", got.RoutineID, routineID)
	}
	if got.RoutineSlug == nil || *got.RoutineSlug != "triage" {
		t.Errorf("routine_slug = %v, want triage", got.RoutineSlug)
	}
	if got.RoutineName == nil || *got.RoutineName != "triage" {
		t.Errorf("routine_name = %v, want triage", got.RoutineName)
	}
}

// TestIssue_Create_WithBadRoutineID rejects a routine_id that doesn't
// exist in the workspace — guards against stale or cross-workspace IDs
// the UI might supply by accident.
func TestIssue_Create_WithBadRoutineID(t *testing.T) {
	h, userID, wsID, crewID, _, _ := newTestIssueHandler(t)

	body := bytes.NewBufferString(`{
		"title":"Bad routine",
		"routine_id":"pln_does_not_exist"
	}`)
	req := httptest.NewRequest("POST", "/", body)
	req.SetPathValue("crewId", crewID)
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, wsID, "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.Create(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (body=%s)", rr.Code, rr.Body.String())
	}
}

// TestIssue_Update_RoutineBinding_SetAndClear verifies the PATCH
// surface: an empty routine_id clears the binding, a valid one sets
// it. Also exercises the routine_inputs replacement path.
func TestIssue_Update_RoutineBinding_SetAndClear(t *testing.T) {
	h, userID, wsID, crewID, leadID, _ := newTestIssueHandler(t)
	id := seedIssue(t, h.db, wsID, crewID, leadID, "ENG-1", "BACKLOG")
	_ = id
	routineID := seedTestPipeline(t, h, wsID, "auto-triage")

	// Set the binding via PATCH.
	patch := bytes.NewBufferString(`{
		"routine_id":"` + routineID + `",
		"routine_inputs":{"k":"v"}
	}`)
	req := httptest.NewRequest("PATCH", "/", patch)
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("identifier", "ENG-1")
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, wsID, "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.Update(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("set status = %d, body=%s", rr.Code, rr.Body.String())
	}
	var resp issueResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode set: %v", err)
	}
	if resp.RoutineID == nil || *resp.RoutineID != routineID {
		t.Errorf("after set: routine_id = %v, want %s", resp.RoutineID, routineID)
	}
	if resp.RoutineSlug == nil || *resp.RoutineSlug != "auto-triage" {
		t.Errorf("after set: routine_slug = %v, want auto-triage", resp.RoutineSlug)
	}

	// Clear the binding via PATCH with empty routine_id.
	clearReq := httptest.NewRequest("PATCH", "/", bytes.NewBufferString(`{"routine_id":""}`))
	clearReq.SetPathValue("crewId", crewID)
	clearReq.SetPathValue("identifier", "ENG-1")
	cctx := withUser(clearReq.Context(), &AuthUser{ID: userID})
	cctx = withWorkspace(cctx, wsID, "OWNER")
	clearReq = clearReq.WithContext(cctx)
	clearRR := httptest.NewRecorder()
	h.Update(clearRR, clearReq)

	if clearRR.Code != http.StatusOK {
		t.Fatalf("clear status = %d, body=%s", clearRR.Code, clearRR.Body.String())
	}
	var cleared issueResponse
	if err := json.Unmarshal(clearRR.Body.Bytes(), &cleared); err != nil {
		t.Fatalf("decode clear: %v", err)
	}
	if cleared.RoutineID != nil {
		t.Errorf("after clear: routine_id = %v, want nil", *cleared.RoutineID)
	}
	if cleared.RoutineSlug != nil {
		t.Errorf("after clear: routine_slug = %v, want nil", *cleared.RoutineSlug)
	}
}
