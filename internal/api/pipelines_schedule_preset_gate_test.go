package api

// #2495 at the HTTP layer: every door that changes an active recipe must
// refuse a change an enabled, unpinned plan's preset can no longer satisfy,
// and must refuse it ACTIONABLY — 409 carrying which plan and why, so the
// caller can open that plan and repair the preset.
//
// The store-level gate is covered in internal/pipeline; what these tests add
// is the mapping, which is the part that was missing per door. Before this
// change the plain save and the agent save reached the store gate not at all,
// and the import and agent doors had no branch for the conflict even once it
// could reach them — an entirely actionable refusal would have surfaced as
// "Failed to save pipeline", HTTP 500.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

// presetGateV1Def declares one required, typed input; the plan's preset
// satisfies it. presetGateV2Def renames that input, so the stored preset no
// longer does — the smallest change that genuinely breaks a plan.
const presetGateV1Def = `{"name":"planned","inputs":[{"name":"who","type":"string","widget":"text","required":true}],"steps":[{"id":"a","type":"transform","transform":{"input":"hi","expression":"."}}]}`
const presetGateV2Def = `{"name":"planned","inputs":[{"name":"recipient","type":"string","widget":"text","required":true}],"steps":[{"id":"a","type":"transform","transform":{"input":"hi","expression":"."}}]}`

// seedPlannedRoutine saves presetGateV1Def and attaches one enabled,
// unpinned plan whose preset fits it.
func seedPlannedRoutine(t *testing.T, h *PipelineHandler, wsID string) *pipeline.Pipeline {
	t.Helper()
	now := time.Now()
	p, err := h.store.Save(t.Context(), pipeline.SaveInput{
		WorkspaceID: wsID, Slug: "planned", Name: "Planned",
		DefinitionJSON: presetGateV1Def, LastTestRunAt: &now, LastTestRunPassed: true,
	})
	if err != nil {
		t.Fatalf("seed routine: %v", err)
	}
	if _, err := h.db.Exec(
		`INSERT INTO pipeline_schedules(id,workspace_id,name,target_pipeline_id,cron_expr,inputs_json,enabled)
		 VALUES('nightly',?,'Nightly report',?,'0 9 * * *','{"who":"alice"}',1)`, wsID, p.ID); err != nil {
		t.Fatalf("seed schedule: %v", err)
	}
	return p
}

// assertActionableConflict is the whole point: a 409 whose body names the
// plan to repair.
func assertActionableConflict(t *testing.T, rr *httptest.ResponseRecorder, door string) {
	t.Helper()
	if rr.Code != http.StatusConflict {
		t.Fatalf("%s: status = %d, want 409; body = %s", door, rr.Code, rr.Body.String())
	}
	var resp struct {
		Error    string                         `json:"error"`
		Conflict pipeline.ScheduleDraftConflict `json:"schedule_conflict"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("%s: decode: %v (%s)", door, err, rr.Body.String())
	}
	if resp.Conflict.ScheduleID != "nightly" || resp.Conflict.Name != "Nightly report" || resp.Conflict.Reason == "" {
		t.Errorf("%s: conflict %+v does not name the plan and the reason", door, resp.Conflict)
	}
	if resp.Error == "" {
		t.Errorf("%s: no human-readable error alongside the structured conflict", door)
	}
}

// assertRecipeUnchanged: a refusal must leave the live recipe and the plan
// exactly as they were. The gate runs inside the save transaction, so a
// partial write here would mean the rollback is not doing its job.
func assertRecipeUnchanged(t *testing.T, h *PipelineHandler, wsID, door string) {
	t.Helper()
	var def, preset string
	if err := h.db.QueryRow(`SELECT definition_json FROM pipelines WHERE workspace_id=? AND slug='planned'`, wsID).Scan(&def); err != nil {
		t.Fatalf("%s: read recipe: %v", door, err)
	}
	if def != presetGateV1Def {
		t.Errorf("%s: the live recipe changed despite the refusal: %s", door, def)
	}
	if err := h.db.QueryRow(`SELECT inputs_json FROM pipeline_schedules WHERE id='nightly'`).Scan(&preset); err != nil {
		t.Fatalf("%s: read preset: %v", door, err)
	}
	if preset != `{"who":"alice"}` {
		t.Errorf("%s: the plan's preset was mutated: %s", door, preset)
	}
}

// TestSchedulePresetGate_UserSaveDoor — POST .../pipelines/save, the door
// `crewship routine save` drives.
func TestSchedulePresetGate_UserSaveDoor(t *testing.T) {
	h, user, ws := newPipelineHandlerForCRUDTest(t)
	seedPlannedRoutine(t, h, ws)

	body, _ := json.Marshal(map[string]any{
		"slug": "planned", "name": "Planned",
		"definition": json.RawMessage(presetGateV2Def), "skip_test_gate": true,
	})
	req := withAuthCtx(withWorkspaceCtx(httptest.NewRequest("POST", "/save", bytes.NewReader(body)), ws), user, "OWNER")
	rr := httptest.NewRecorder()
	h.Save(rr, req)

	assertActionableConflict(t, rr, "user save")
	assertRecipeUnchanged(t, h, ws, "user save")
}

// TestSchedulePresetGate_AgentSaveDoor — POST /internal/pipelines/save, the
// door an agent authoring a routine uses. It carries no session, so a 500
// here would leave the agent with nothing to act on.
func TestSchedulePresetGate_AgentSaveDoor(t *testing.T) {
	h, wsID, crewID := buildInternalSaveHandler1371(t)
	seedPlannedRoutine(t, h, wsID)

	token := signSaveToken([]byte(testSaveTokenSecret1371), wsID,
		pipeline.DefinitionHash([]byte(presetGateV2Def)), internalSaveTokenSubject(crewID), time.Now())
	body := `{"workspace_id":"` + wsID + `","slug":"planned","name":"Planned",` +
		`"author_crew_id":"` + crewID + `","save_token":"` + token + `",` +
		`"definition":` + presetGateV2Def + `}`
	req := httptest.NewRequest("POST", "/api/v1/internal/pipelines/save", bytes.NewBufferString(body))
	rr := httptest.NewRecorder()
	h.InternalSave(rr, req)

	assertActionableConflict(t, rr, "agent save")
	assertRecipeUnchanged(t, h, wsID, "agent save")
}

// TestSchedulePresetGate_RepairThenSave is the way out. Without it the gate
// would be a wall rather than a prompt: repair the named plan's preset, and
// the same save goes through.
func TestSchedulePresetGate_RepairThenSave(t *testing.T) {
	h, user, ws := newPipelineHandlerForCRUDTest(t)
	seedPlannedRoutine(t, h, ws)

	save := func() *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]any{
			"slug": "planned", "name": "Planned",
			"definition": json.RawMessage(presetGateV2Def), "skip_test_gate": true,
		})
		req := withAuthCtx(withWorkspaceCtx(httptest.NewRequest("POST", "/save", bytes.NewReader(body)), ws), user, "OWNER")
		rr := httptest.NewRecorder()
		h.Save(rr, req)
		return rr
	}
	if rr := save(); rr.Code != http.StatusConflict {
		t.Fatalf("first attempt: status = %d, want 409", rr.Code)
	}
	if _, err := h.db.Exec(`UPDATE pipeline_schedules SET inputs_json='{"recipient":"alice"}' WHERE id='nightly'`); err != nil {
		t.Fatalf("repair preset: %v", err)
	}
	rr := save()
	if rr.Code != http.StatusOK && rr.Code != http.StatusCreated {
		t.Fatalf("after repairing the preset: status = %d, want 2xx; body = %s", rr.Code, rr.Body.String())
	}
	var def string
	if err := h.db.QueryRow(`SELECT definition_json FROM pipelines WHERE workspace_id=? AND slug='planned'`, ws).Scan(&def); err != nil {
		t.Fatalf("read recipe: %v", err)
	}
	if def != presetGateV2Def {
		t.Errorf("the recipe did not advance after the repair: %s", def)
	}
}

// TestSchedulePresetGate_DisabledPlanDoesNotBlock keeps the exclusion honest
// at the HTTP layer too: a plan that is switched off is not firing, so it has
// no claim on the schema.
func TestSchedulePresetGate_DisabledPlanDoesNotBlock(t *testing.T) {
	h, user, ws := newPipelineHandlerForCRUDTest(t)
	seedPlannedRoutine(t, h, ws)
	if _, err := h.db.Exec(`UPDATE pipeline_schedules SET enabled=0 WHERE id='nightly'`); err != nil {
		t.Fatalf("disable plan: %v", err)
	}
	body, _ := json.Marshal(map[string]any{
		"slug": "planned", "name": "Planned",
		"definition": json.RawMessage(presetGateV2Def), "skip_test_gate": true,
	})
	req := withAuthCtx(withWorkspaceCtx(httptest.NewRequest("POST", "/save", bytes.NewReader(body)), ws), user, "OWNER")
	rr := httptest.NewRecorder()
	h.Save(rr, req)
	if rr.Code == http.StatusConflict {
		t.Fatalf("a disabled plan blocked the save: %s", rr.Body.String())
	}
}
