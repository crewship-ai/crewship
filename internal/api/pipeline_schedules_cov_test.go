package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// pipeline_schedules_cov_test.go covers the remaining schedule-handler
// branches: nil-store 503s, wake-gate resolution edge cases, enabled
// pointer handling, store failures (500), and the small helpers.
// Reuses scheduleHandlerRig / seedPipelineRow / seedPipelineRowDef /
// withWorkspaceUser from the existing schedule tests. Prefix covPS2.

func TestCovPS2_WakeRefFromBody(t *testing.T) {
	if id, slug, set := wakeRefFromBody(&scheduleRequestBody{}); set || id != "" || slug != "" {
		t.Errorf("absent refs: got %q/%q/%v", id, slug, set)
	}
	wid, wslug := "pid", "pslug"
	id, slug, set := wakeRefFromBody(&scheduleRequestBody{WakePipelineID: &wid, WakePipelineSlug: &wslug})
	if !set || id != "pid" || slug != "pslug" {
		t.Errorf("got %q/%q/%v", id, slug, set)
	}
}

func TestCovPS2_IsUserScheduleError_Nil(t *testing.T) {
	if isUserScheduleError(nil) {
		t.Errorf("nil must not be a user error")
	}
}

func TestCovPS2_ResolveWakePipeline_Branches(t *testing.T) {
	h, db, _, wsID := scheduleHandlerRig(t)
	req := httptest.NewRequest("GET", "/", nil)

	// Neither id nor slug.
	if _, _, err := h.resolveWakePipeline(req, wsID, "tgt", "", ""); err == nil ||
		!strings.Contains(err.Error(), "wake_pipeline_slug or wake_pipeline_id required") {
		t.Errorf("default arm err = %v", err)
	}

	// By-ID lookup, but the pipeline lives in another workspace.
	execOrFatal(t, db, `INSERT INTO workspaces (id, name, slug) VALUES ('covps2-other-ws', 'Other', 'covps2-other')`)
	seedPipelineRowDef(t, db, "covps2-other-ws", "covps2-foreign", "covps2-foreign", agentlessProbeDef)
	if _, _, err := h.resolveWakePipeline(req, wsID, "tgt", "covps2-foreign", ""); err == nil ||
		!strings.Contains(err.Error(), "wake pipeline not found") {
		t.Errorf("cross-workspace err = %v", err)
	}

	// By-ID happy path.
	seedPipelineRowDef(t, db, wsID, "covps2-probe", "covps2-probe", agentlessProbeDef)
	id, slug, err := h.resolveWakePipeline(req, wsID, "tgt", "covps2-probe", "")
	if err != nil || id != "covps2-probe" || slug != "covps2-probe" {
		t.Errorf("by-id: %q/%q/%v", id, slug, err)
	}

	// Definition that does not parse.
	seedPipelineRowDef(t, db, wsID, "covps2-broken", "covps2-broken", `{not json`)
	if _, _, err := h.resolveWakePipeline(req, wsID, "tgt", "covps2-broken", ""); err == nil ||
		!strings.Contains(err.Error(), "does not parse") {
		t.Errorf("broken def err = %v", err)
	}
}

func TestCovPS2_ResolveSchedulePipelineID_NeitherGiven(t *testing.T) {
	h, _, _, wsID := scheduleHandlerRig(t)
	req := httptest.NewRequest("GET", "/", nil)
	if _, _, err := h.resolveSchedulePipelineID(req, wsID, &scheduleRequestBody{}); err == nil ||
		!strings.Contains(err.Error(), "target_pipeline_slug or target_pipeline_id required") {
		t.Errorf("err = %v", err)
	}
}

func TestCovPS2_CreateSchedule_EnabledFalseAndNoInputs(t *testing.T) {
	h, db, userID, wsID := scheduleHandlerRig(t)
	seedPipelineRow(t, db, wsID, "covps2-main", "covps2-main")

	req := withWorkspaceUser(httptest.NewRequest("POST", "/x",
		strings.NewReader(`{"cron_expr":"*/5 * * * *","target_pipeline_slug":"covps2-main","enabled":false}`)),
		userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.CreateSchedule(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["enabled"] != false {
		t.Errorf("enabled = %v, want false", resp["enabled"])
	}
	if _, ok := resp["inputs"].(map[string]any); !ok {
		t.Errorf("inputs = %v, want empty object (nil -> {})", resp["inputs"])
	}
}

func TestCovPS2_CreateSchedule_StoreFailure500(t *testing.T) {
	h, db, userID, wsID := scheduleHandlerRig(t)
	seedPipelineRow(t, db, wsID, "covps2-fail", "covps2-fail")
	execOrFatal(t, db, `CREATE TRIGGER covps2_fail_ins BEFORE INSERT ON pipeline_schedules BEGIN SELECT RAISE(ABORT, 'covps2 boom'); END`)

	req := withWorkspaceUser(httptest.NewRequest("POST", "/x",
		strings.NewReader(`{"cron_expr":"*/5 * * * *","target_pipeline_slug":"covps2-fail"}`)),
		userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.CreateSchedule(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovPS2_NilStore503(t *testing.T) {
	db := setupTestDB(t)
	h := NewPipelineHandler(db, newTestLogger(), nil, nil) // no SetScheduleStore
	for name, call := range map[string]func(http.ResponseWriter, *http.Request){
		"update": h.UpdateSchedule,
		"delete": h.DeleteSchedule,
	} {
		req := withWorkspaceUser(httptest.NewRequest("POST", "/x", strings.NewReader(`{}`)), "u", "ws", "OWNER")
		rr := httptest.NewRecorder()
		call(rr, req)
		if rr.Code != http.StatusServiceUnavailable {
			t.Errorf("%s: status = %d, want 503", name, rr.Code)
		}
	}
}

func TestCovPS2_UpdateSchedule_TargetResolveError(t *testing.T) {
	h, db, userID, wsID := scheduleHandlerRig(t)
	seedPipelineRow(t, db, wsID, "covps2-u1", "covps2-u1")
	s := covPS2Seed(t, h, db, userID, wsID, "covps2-u1")

	req := withWorkspaceUser(httptest.NewRequest("PATCH", "/x",
		strings.NewReader(`{"cron_expr":"*/5 * * * *","target_pipeline_slug":"covps2-ghost"}`)),
		userID, wsID, "OWNER")
	req.SetPathValue("scheduleId", s)
	rr := httptest.NewRecorder()
	h.UpdateSchedule(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
}

// covPS2Seed creates a schedule via the API and returns its id.
func covPS2Seed(t *testing.T, h *PipelineHandler, db interface{}, userID, wsID, targetSlug string) string {
	t.Helper()
	req := withWorkspaceUser(httptest.NewRequest("POST", "/x",
		strings.NewReader(`{"cron_expr":"*/5 * * * *","target_pipeline_slug":"`+targetSlug+`"}`)),
		userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.CreateSchedule(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("seed schedule: status = %d, body=%s", rr.Code, rr.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	id, _ := resp["id"].(string)
	if id == "" {
		t.Fatalf("no id in %v", resp)
	}
	return id
}

func TestCovPS2_UpdateSchedule_WakeResolveErrorAndRetargetOntoProbe(t *testing.T) {
	h, db, userID, wsID := scheduleHandlerRig(t)
	seedPipelineRow(t, db, wsID, "covps2-u2", "covps2-u2")
	seedPipelineRowDef(t, db, wsID, "covps2-u2-probe", "covps2-u2-probe", agentlessProbeDef)
	s := covPS2Seed(t, h, db, userID, wsID, "covps2-u2")

	// PATCH with an unknown wake slug -> 400.
	req := withWorkspaceUser(httptest.NewRequest("PATCH", "/x",
		strings.NewReader(`{"cron_expr":"*/5 * * * *","wake_pipeline_slug":"covps2-nope"}`)),
		userID, wsID, "OWNER")
	req.SetPathValue("scheduleId", s)
	rr := httptest.NewRecorder()
	h.UpdateSchedule(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("unknown wake: status = %d, body=%s", rr.Code, rr.Body.String())
	}

	// Attach a valid gate.
	req = withWorkspaceUser(httptest.NewRequest("PATCH", "/x",
		strings.NewReader(`{"cron_expr":"*/5 * * * *","wake_pipeline_slug":"covps2-u2-probe"}`)),
		userID, wsID, "OWNER")
	req.SetPathValue("scheduleId", s)
	rr = httptest.NewRecorder()
	h.UpdateSchedule(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("attach gate: status = %d, body=%s", rr.Code, rr.Body.String())
	}

	// Retarget the schedule onto its own probe without mentioning the
	// gate: the unchanged-gate re-check must reject it.
	req = withWorkspaceUser(httptest.NewRequest("PATCH", "/x",
		strings.NewReader(`{"cron_expr":"*/5 * * * *","target_pipeline_id":"covps2-u2-probe"}`)),
		userID, wsID, "OWNER")
	req.SetPathValue("scheduleId", s)
	rr = httptest.NewRecorder()
	h.UpdateSchedule(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("retarget onto probe: status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "own routine") {
		t.Errorf("body = %s", rr.Body.String())
	}
}

func TestCovPS2_UpdateSchedule_ExplicitGateClear(t *testing.T) {
	h, db, userID, wsID := scheduleHandlerRig(t)
	seedPipelineRow(t, db, wsID, "covps2-u3", "covps2-u3")
	seedPipelineRowDef(t, db, wsID, "covps2-u3-probe", "covps2-u3-probe", agentlessProbeDef)
	s := covPS2Seed(t, h, db, userID, wsID, "covps2-u3")

	// Attach, then clear with explicit "".
	for _, body := range []string{
		`{"cron_expr":"*/5 * * * *","wake_pipeline_slug":"covps2-u3-probe"}`,
		`{"cron_expr":"*/5 * * * *","wake_pipeline_slug":""}`,
	} {
		req := withWorkspaceUser(httptest.NewRequest("PATCH", "/x", strings.NewReader(body)), userID, wsID, "OWNER")
		req.SetPathValue("scheduleId", s)
		rr := httptest.NewRecorder()
		h.UpdateSchedule(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("body %s: status = %d, resp=%s", body, rr.Code, rr.Body.String())
		}
	}
	// Gate gone from the response of a final GET via ListSchedules.
	req := withWorkspaceUser(httptest.NewRequest("GET", "/x", nil), userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.ListSchedules(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("list: %d", rr.Code)
	}
	if strings.Contains(rr.Body.String(), "covps2-u3-probe") {
		t.Errorf("gate still present after clear: %s", rr.Body.String())
	}
}

func TestCovPS2_DeleteSchedule_StoreFailure500(t *testing.T) {
	h, db, userID, wsID := scheduleHandlerRig(t)
	seedPipelineRow(t, db, wsID, "covps2-d", "covps2-d")
	s := covPS2Seed(t, h, db, userID, wsID, "covps2-d")
	execOrFatal(t, db, `CREATE TRIGGER covps2_fail_upd BEFORE UPDATE ON pipeline_schedules BEGIN SELECT RAISE(ABORT, 'covps2 boom'); END`)

	req := withWorkspaceUser(httptest.NewRequest("DELETE", "/x", nil), userID, wsID, "OWNER")
	req.SetPathValue("scheduleId", s)
	rr := httptest.NewRecorder()
	h.DeleteSchedule(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovPS2_ListSchedules_CorruptWakeInputsTolerated(t *testing.T) {
	h, db, userID, wsID := scheduleHandlerRig(t)
	seedPipelineRow(t, db, wsID, "covps2-l", "covps2-l")
	seedPipelineRowDef(t, db, wsID, "covps2-l-probe", "covps2-l-probe", agentlessProbeDef)
	s := covPS2Seed(t, h, db, userID, wsID, "covps2-l")
	// Attach the gate, then corrupt the stored wake inputs.
	req := withWorkspaceUser(httptest.NewRequest("PATCH", "/x",
		strings.NewReader(`{"cron_expr":"*/5 * * * *","wake_pipeline_slug":"covps2-l-probe"}`)),
		userID, wsID, "OWNER")
	req.SetPathValue("scheduleId", s)
	rr := httptest.NewRecorder()
	h.UpdateSchedule(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("attach gate: %d %s", rr.Code, rr.Body.String())
	}
	execOrFatal(t, db, `UPDATE pipeline_schedules SET wake_inputs_json = '{corrupt' WHERE id = ?`, s)

	req = withWorkspaceUser(httptest.NewRequest("GET", "/x", nil), userID, wsID, "OWNER")
	rr = httptest.NewRecorder()
	h.ListSchedules(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("list with corrupt wake inputs: status = %d, body=%s", rr.Code, rr.Body.String())
	}
}
