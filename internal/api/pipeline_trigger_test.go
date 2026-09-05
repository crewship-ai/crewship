package api

// B8 (#2359): atomic routine authoring. These tests drive the PUBLIC HTTP
// surface (InternalSave — the sidecar's save_routine entry point — and
// ActivateSchedule), not the pipeline.Store internals directly, because the
// accept line is about what a real caller can observe: a bad trigger rolls
// the whole save back, a good one names a first fire time, and a draft
// raises exactly one approval item with a receipt pinning the version.

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

// triggerSaveRig wires a PipelineHandler with everything InternalSave and
// ActivateSchedule need: the full migrated schema, a save_token secret, and
// a seeded crew + agent so a save_token can be minted over a real
// definition. Mirrors scheduleHandlerRig (pipeline_schedules_test.go) plus
// the save-token bits TestPipelineInternalSave_HappyPath uses.
func triggerSaveRig(t *testing.T) (h *PipelineHandler, db *sql.DB, wsID, crewID string) {
	t.Helper()
	db = setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID = seedTestWorkspace(t, db, userID)
	crewID = seedCrewRow(t, db, "c-trigger", wsID, "Eng", "eng")
	seedAgentRow(t, db, "a-trigger", wsID, crewID, "Lead", "agent_lead", "LEAD")

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	h = NewPipelineHandler(db, logger, nil, nil)
	h.SetScheduleStore(pipeline.NewScheduleStore(db))
	h.SetSaveTokenSecret([]byte("trigger-test-secret"))
	return h, db, wsID, crewID
}

// internalSaveTriggerBody builds a valid InternalSave request body carrying
// a trigger block, with a save_token minted over the exact definition so
// the test-run gate clears.
func internalSaveTriggerBody(t *testing.T, h *PipelineHandler, wsID, crewID, slug string, trigger, activation string) []byte {
	t.Helper()
	def := `{"name":"` + slug + `","steps":[{"id":"a","type":"agent_run","agent_slug":"agent_lead","prompt":"hi"}]}`
	token := signSaveToken([]byte("trigger-test-secret"), wsID,
		definitionHashHex([]byte(def)), internalSaveTokenSubject(crewID), time.Now())
	body := map[string]any{
		"workspace_id":    wsID,
		"slug":            slug,
		"name":            slug,
		"author_crew_id":  crewID,
		"author_agent_id": "a-trigger",
		"save_token":      token,
		"definition":      json.RawMessage(def),
	}
	if trigger != "" {
		var t2 map[string]any
		if err := json.Unmarshal([]byte(trigger), &t2); err != nil {
			t.Fatalf("bad trigger literal: %v", err)
		}
		body["trigger"] = t2
	}
	if activation != "" {
		body["activation"] = activation
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	return raw
}

func TestPipelineInternalSave_Trigger_RollbackOnBadCron(t *testing.T) {
	h, tdb, wsID, crewID := triggerSaveRig(t)
	body := internalSaveTriggerBody(t, h, wsID, crewID, "rollback-routine",
		`{"kind":"schedule","cron":"not a cron expression","timezone":"UTC"}`, "")

	req := httptest.NewRequest("POST", "/api/v1/internal/pipelines/save", bytes.NewBuffer(body))
	rr := httptest.NewRecorder()
	h.InternalSave(rr, req)
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status=%d want 422; body=%s", rr.Code, rr.Body.String())
	}

	// B8's atomicity accept line, proven through the public API: the bad
	// trigger must not leave the routine behind either.
	var count int
	if err := tdb.QueryRow(
		`SELECT COUNT(*) FROM pipelines WHERE workspace_id = ? AND slug = 'rollback-routine'`, wsID,
	).Scan(&count); err != nil {
		t.Fatalf("count pipelines: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected 0 pipeline rows after a rolled-back trigger save, got %d", count)
	}
}

func TestPipelineInternalSave_Trigger_NamesFirstFireTime(t *testing.T) {
	h, _, wsID, crewID := triggerSaveRig(t)
	body := internalSaveTriggerBody(t, h, wsID, crewID, "scheduled-routine",
		`{"kind":"schedule","cron":"0 9 * * 1-5","timezone":"Europe/Prague"}`, "")

	req := httptest.NewRequest("POST", "/api/v1/internal/pipelines/save", bytes.NewBuffer(body))
	rr := httptest.NewRecorder()
	h.InternalSave(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status=%d want 201; body=%s", rr.Code, rr.Body.String())
	}
	var resp struct {
		Trigger struct {
			Kind             string `json:"kind"`
			FirstFireAt      string `json:"first_fire_at"`
			Enabled          bool   `json:"enabled"`
			ApprovalRequired bool   `json:"approval_required"`
		} `json:"trigger"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Trigger.Kind != "schedule" {
		t.Fatalf("trigger.kind=%q want schedule", resp.Trigger.Kind)
	}
	if resp.Trigger.FirstFireAt == "" {
		t.Fatalf("expected trigger.first_fire_at to be populated — this is the first fire time")
	}
	if !resp.Trigger.Enabled || resp.Trigger.ApprovalRequired {
		t.Fatalf("expected an immediately-active trigger, got enabled=%v approval_required=%v",
			resp.Trigger.Enabled, resp.Trigger.ApprovalRequired)
	}
}

func TestPipelineInternalSave_Trigger_Draft_RaisesExactlyOneApprovalItem(t *testing.T) {
	h, tdb, wsID, crewID := triggerSaveRig(t)
	body := internalSaveTriggerBody(t, h, wsID, crewID, "draft-routine",
		`{"kind":"schedule","cron":"0 9 * * 1-5","timezone":"UTC"}`, "draft")

	req := httptest.NewRequest("POST", "/api/v1/internal/pipelines/save", bytes.NewBuffer(body))
	rr := httptest.NewRecorder()
	h.InternalSave(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status=%d want 201; body=%s", rr.Code, rr.Body.String())
	}
	var resp struct {
		Trigger struct {
			ScheduleID       string `json:"schedule_id"`
			ApprovalRequired bool   `json:"approval_required"`
			Enabled          bool   `json:"enabled"`
		} `json:"trigger"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.Trigger.ApprovalRequired {
		t.Fatalf("expected approval_required=true for a draft trigger")
	}
	if resp.Trigger.Enabled {
		t.Fatalf("expected a draft trigger to be created DISABLED")
	}

	var count int
	var payload string
	if err := tdb.QueryRow(
		`SELECT COUNT(*) FROM inbox_items WHERE workspace_id = ? AND kind = 'escalation' AND source_id LIKE 'routinetrigger:%'`,
		wsID,
	).Scan(&count); err != nil {
		t.Fatalf("count inbox items: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected exactly ONE approval item for the draft trigger, got %d", count)
	}
	if err := tdb.QueryRow(
		`SELECT payload_json FROM inbox_items WHERE workspace_id = ? AND source_id LIKE 'routinetrigger:%'`, wsID,
	).Scan(&payload); err != nil {
		t.Fatalf("read payload: %v", err)
	}
	var decoded struct {
		RoutineVersion int    `json:"routine_version"`
		ScheduleID     string `json:"schedule_id"`
	}
	if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if decoded.RoutineVersion != 1 {
		t.Fatalf("expected the receipt to pin routine_version=1 (the version just created), got %d", decoded.RoutineVersion)
	}
	if decoded.ScheduleID != resp.Trigger.ScheduleID {
		t.Fatalf("payload schedule_id=%q want %q", decoded.ScheduleID, resp.Trigger.ScheduleID)
	}
}

func TestPipelineInternalSave_Trigger_Manual_NoApprovalNoSchedule(t *testing.T) {
	h, tdb, wsID, crewID := triggerSaveRig(t)
	body := internalSaveTriggerBody(t, h, wsID, crewID, "manual-routine", `{"kind":"manual"}`, "")

	req := httptest.NewRequest("POST", "/api/v1/internal/pipelines/save", bytes.NewBuffer(body))
	rr := httptest.NewRecorder()
	h.InternalSave(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status=%d want 201; body=%s", rr.Code, rr.Body.String())
	}
	var resp struct {
		Trigger struct {
			Kind string `json:"kind"`
		} `json:"trigger"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Trigger.Kind != "manual" {
		t.Fatalf("trigger.kind=%q want manual", resp.Trigger.Kind)
	}
	var count int
	if err := tdb.QueryRow(`SELECT COUNT(*) FROM pipeline_schedules WHERE workspace_id = ?`, wsID).Scan(&count); err != nil {
		t.Fatalf("count schedules: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected no schedule row for a manual trigger, got %d", count)
	}
}

func TestPipelineHandler_ActivateSchedule_TurnsDraftOn(t *testing.T) {
	h, tdb, wsID, crewID := triggerSaveRig(t)
	body := internalSaveTriggerBody(t, h, wsID, crewID, "activate-me",
		`{"kind":"schedule","cron":"0 9 * * 1-5","timezone":"UTC"}`, "draft")
	req := httptest.NewRequest("POST", "/api/v1/internal/pipelines/save", bytes.NewBuffer(body))
	rr := httptest.NewRecorder()
	h.InternalSave(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("save status=%d want 201; body=%s", rr.Code, rr.Body.String())
	}
	var resp struct {
		Trigger struct {
			ScheduleID string `json:"schedule_id"`
		} `json:"trigger"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	actReq := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/workspaces/"+wsID+"/pipeline-schedules/"+resp.Trigger.ScheduleID+"/activate", nil),
		"user_test_actor", wsID, "MANAGER")
	actReq.SetPathValue("scheduleId", resp.Trigger.ScheduleID)
	actRR := httptest.NewRecorder()
	h.ActivateSchedule(actRR, actReq)
	if actRR.Code != http.StatusOK {
		t.Fatalf("activate status=%d want 200; body=%s", actRR.Code, actRR.Body.String())
	}
	var activated struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.Unmarshal(actRR.Body.Bytes(), &activated); err != nil {
		t.Fatalf("decode activate response: %v", err)
	}
	if !activated.Enabled {
		t.Fatalf("expected the schedule to be enabled after activation")
	}

	// Activating a second time must refuse — nothing left to activate.
	act2 := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/workspaces/"+wsID+"/pipeline-schedules/"+resp.Trigger.ScheduleID+"/activate", nil),
		"user_test_actor", wsID, "MANAGER")
	act2.SetPathValue("scheduleId", resp.Trigger.ScheduleID)
	act2RR := httptest.NewRecorder()
	h.ActivateSchedule(act2RR, act2)
	if act2RR.Code != http.StatusConflict {
		t.Fatalf("second activate status=%d want 409; body=%s", act2RR.Code, act2RR.Body.String())
	}

	var inboxState string
	if err := tdb.QueryRow(
		`SELECT state FROM inbox_items WHERE workspace_id = ? AND source_id LIKE 'routinetrigger:%'`, wsID,
	).Scan(&inboxState); err != nil {
		t.Fatalf("read inbox state: %v", err)
	}
	if inboxState != "resolved" {
		t.Fatalf("expected the approval item resolved after activation, got state=%q", inboxState)
	}
}

func TestPipelineHandler_ActivateSchedule_RequiresManager(t *testing.T) {
	h, _, wsID, crewID := triggerSaveRig(t)
	body := internalSaveTriggerBody(t, h, wsID, crewID, "activate-role-check",
		`{"kind":"schedule","cron":"0 9 * * 1-5","timezone":"UTC"}`, "draft")
	req := httptest.NewRequest("POST", "/api/v1/internal/pipelines/save", bytes.NewBuffer(body))
	rr := httptest.NewRecorder()
	h.InternalSave(rr, req)
	var resp struct {
		Trigger struct {
			ScheduleID string `json:"schedule_id"`
		} `json:"trigger"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)

	actReq := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/workspaces/"+wsID+"/pipeline-schedules/"+resp.Trigger.ScheduleID+"/activate", nil),
		"user_test_actor", wsID, "MEMBER")
	actReq.SetPathValue("scheduleId", resp.Trigger.ScheduleID)
	actRR := httptest.NewRecorder()
	h.ActivateSchedule(actRR, actReq)
	if actRR.Code != http.StatusForbidden {
		t.Fatalf("status=%d want 403; body=%s", actRR.Code, actRR.Body.String())
	}
}
