package api

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Wake-gate fields on the schedule endpoints. The save handler is the
// enforcement point for the product guarantee "wake checks are free":
// wake_pipeline_slug/-id must reference an AGENTLESS routine in the
// same workspace, and a schedule can't gate on its own routine.
// ---------------------------------------------------------------------------

// seedPipelineRowDef mirrors seedPipelineRow but with a caller-supplied
// definition, so wake tests can seed agentless and non-agentless probes.
func seedPipelineRowDef(t *testing.T, db *sql.DB, wsID, id, slug, definitionJSON string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := db.Exec(`
		INSERT INTO pipelines (id, workspace_id, slug, name, definition_json, definition_hash, created_at, updated_at, last_test_run_at)
		VALUES (?, ?, ?, ?, ?, 'hash', ?, ?, ?)`,
		id, wsID, slug, slug, definitionJSON, now, now, now); err != nil {
		t.Fatalf("seed pipeline %s: %v", slug, err)
	}
}

const agentlessProbeDef = `{"dsl_version":"1.0","name":"cost-probe","agentless":true,"steps":[{"id":"t","type":"transform","transform":{"input":"true","expression":"."}}]}`
const agentfulProbeDef = `{"dsl_version":"1.0","name":"agent-probe","steps":[{"id":"a","type":"agent_run","agent_slug":"judge","prompt":"interesting?"}]}`

func wakeCreateBody(wakeSlug string) string {
	return `{
		"cron_expr": "*/10 * * * *",
		"target_pipeline_slug": "ping-hosts",
		"wake_pipeline_slug": "` + wakeSlug + `",
		"wake_inputs": {"threshold": "100"}
	}`
}

func TestPipelineSchedules_Create_WithWakeGate_Returns201(t *testing.T) {
	h, db, userID, wsID := scheduleHandlerRig(t)
	seedPipelineRow(t, db, wsID, "pln_main", "ping-hosts")
	seedPipelineRowDef(t, db, wsID, "pln_probe", "cost-probe", agentlessProbeDef)

	req := withWorkspaceUser(httptest.NewRequest("POST",
		"/api/v1/workspaces/"+wsID+"/pipeline-schedules",
		strings.NewReader(wakeCreateBody("cost-probe"))),
		userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.CreateSchedule(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body: %s)", rr.Code, rr.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["wake_pipeline_id"] != "pln_probe" {
		t.Errorf("wake_pipeline_id: got %v", resp["wake_pipeline_id"])
	}
	if resp["wake_pipeline_slug"] != "cost-probe" {
		t.Errorf("wake_pipeline_slug: got %v", resp["wake_pipeline_slug"])
	}
	inputs, _ := resp["wake_inputs"].(map[string]any)
	if inputs["threshold"] != "100" {
		t.Errorf("wake_inputs round-trip: got %v", resp["wake_inputs"])
	}
}

func TestPipelineSchedules_Create_WakeGateNonAgentless_Returns400(t *testing.T) {
	h, db, userID, wsID := scheduleHandlerRig(t)
	seedPipelineRow(t, db, wsID, "pln_main", "ping-hosts")
	seedPipelineRowDef(t, db, wsID, "pln_probe", "agent-probe", agentfulProbeDef)

	req := withWorkspaceUser(httptest.NewRequest("POST",
		"/api/v1/workspaces/"+wsID+"/pipeline-schedules",
		strings.NewReader(wakeCreateBody("agent-probe"))),
		userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.CreateSchedule(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body: %s)", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "agentless") {
		t.Errorf("error should explain the agentless requirement, got: %s", rr.Body.String())
	}
}

func TestPipelineSchedules_Create_WakeGateUnknownSlug_Returns400(t *testing.T) {
	h, db, userID, wsID := scheduleHandlerRig(t)
	seedPipelineRow(t, db, wsID, "pln_main", "ping-hosts")

	req := withWorkspaceUser(httptest.NewRequest("POST",
		"/api/v1/workspaces/"+wsID+"/pipeline-schedules",
		strings.NewReader(wakeCreateBody("ghost-probe"))),
		userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.CreateSchedule(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body: %s)", rr.Code, rr.Body.String())
	}
}

func TestPipelineSchedules_Create_WakeGateSelfReference_Returns400(t *testing.T) {
	h, db, userID, wsID := scheduleHandlerRig(t)
	// The target itself is agentless — still can't gate on itself.
	seedPipelineRowDef(t, db, wsID, "pln_main", "ping-hosts", agentlessProbeDef)

	req := withWorkspaceUser(httptest.NewRequest("POST",
		"/api/v1/workspaces/"+wsID+"/pipeline-schedules",
		strings.NewReader(wakeCreateBody("ping-hosts"))),
		userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.CreateSchedule(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body: %s)", rr.Code, rr.Body.String())
	}
}

func createWakeGatedSchedule(t *testing.T, h *PipelineHandler, userID, wsID string) string {
	t.Helper()
	req := withWorkspaceUser(httptest.NewRequest("POST",
		"/api/v1/workspaces/"+wsID+"/pipeline-schedules",
		strings.NewReader(wakeCreateBody("cost-probe"))),
		userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.CreateSchedule(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create: status = %d (body: %s)", rr.Code, rr.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	id, _ := resp["id"].(string)
	if id == "" {
		t.Fatal("create: no schedule id in response")
	}
	return id
}

func TestPipelineSchedules_Update_ClearsWakeGate(t *testing.T) {
	h, db, userID, wsID := scheduleHandlerRig(t)
	seedPipelineRow(t, db, wsID, "pln_main", "ping-hosts")
	seedPipelineRowDef(t, db, wsID, "pln_probe", "cost-probe", agentlessProbeDef)
	id := createWakeGatedSchedule(t, h, userID, wsID)

	req := withWorkspaceUser(httptest.NewRequest("PATCH",
		"/api/v1/workspaces/"+wsID+"/pipeline-schedules/"+id,
		strings.NewReader(`{"wake_pipeline_slug": ""}`)),
		userID, wsID, "OWNER")
	req.SetPathValue("scheduleId", id)
	rr := httptest.NewRecorder()
	h.UpdateSchedule(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rr.Code, rr.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if got, _ := resp["wake_pipeline_id"].(string); got != "" {
		t.Errorf("expected gate cleared, got wake_pipeline_id=%q", got)
	}
}

func TestPipelineSchedules_List_ResolvesWakeSlug(t *testing.T) {
	h, db, userID, wsID := scheduleHandlerRig(t)
	seedPipelineRow(t, db, wsID, "pln_main", "ping-hosts")
	seedPipelineRowDef(t, db, wsID, "pln_probe", "cost-probe", agentlessProbeDef)
	createWakeGatedSchedule(t, h, userID, wsID)

	req := withWorkspaceUser(httptest.NewRequest("GET",
		"/api/v1/workspaces/"+wsID+"/pipeline-schedules", nil),
		userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.ListSchedules(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d (body: %s)", rr.Code, rr.Body.String())
	}
	var rows []map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &rows); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 schedule, got %d", len(rows))
	}
	if rows[0]["wake_pipeline_slug"] != "cost-probe" {
		t.Errorf("list should resolve wake_pipeline_slug, got %v", rows[0]["wake_pipeline_slug"])
	}
	if rows[0]["target_pipeline_slug"] != "ping-hosts" {
		t.Errorf("target slug resolution regressed: %v", rows[0]["target_pipeline_slug"])
	}
}

func TestPipelineSchedules_Update_WithoutWakeFields_KeepsGate(t *testing.T) {
	h, db, userID, wsID := scheduleHandlerRig(t)
	seedPipelineRow(t, db, wsID, "pln_main", "ping-hosts")
	seedPipelineRowDef(t, db, wsID, "pln_probe", "cost-probe", agentlessProbeDef)
	id := createWakeGatedSchedule(t, h, userID, wsID)

	req := withWorkspaceUser(httptest.NewRequest("PATCH",
		"/api/v1/workspaces/"+wsID+"/pipeline-schedules/"+id,
		strings.NewReader(`{"cron_expr": "*/30 * * * *"}`)),
		userID, wsID, "OWNER")
	req.SetPathValue("scheduleId", id)
	rr := httptest.NewRecorder()
	h.UpdateSchedule(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rr.Code, rr.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp["wake_pipeline_id"] != "pln_probe" {
		t.Errorf("gate must survive an unrelated PATCH, got wake_pipeline_id=%v", resp["wake_pipeline_id"])
	}
	inputs, _ := resp["wake_inputs"].(map[string]any)
	if inputs["threshold"] != "100" {
		t.Errorf("wake_inputs must survive an unrelated PATCH, got %v", resp["wake_inputs"])
	}
}
