package api

// #2496 — a plan's preset must satisfy the recipe it points at, checked when
// the plan is WRITTEN, not only when the recipe later changes.
//
// The run path is already guarded twice: the Run handler calls
// pipeline.ValidateFormInputs before dispatch, and the executor calls it
// again before the first step. The publish path is guarded by the
// schedule-preset gate, which re-validates every enabled plan whenever the
// recipe changes. Between those two sits the hole: creating or editing a
// PLAN stores whatever preset it is given. A plan can therefore be born with
// a preset its target routine already rejects, sit enabled in the calendar
// looking healthy, and fail for the first time at 02:30 — with no run to
// inspect, because the executor refuses before the run row exists.
//
// The fix reuses pipeline.ValidateFormInputs, the same function the run path
// and the decision forms use, rather than adding a second opinion about what
// a valid input is. In particular `hasInputForm` still decides what carries
// a contract at all, so a legacy untyped recipe stays exactly as permissive
// as it is today.

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

// presetValidationDef declares one required select and one optional boolean,
// both with a widget — i.e. both carrying the form contract hasInputForm
// recognises.
const presetValidationDef = `{"name":"planned","inputs":[` +
	`{"name":"region","type":"string","widget":"select","options":["eu","us"],"required":true},` +
	`{"name":"dry_run","type":"boolean","widget":"boolean","default":true}],` +
	`"steps":[{"id":"a","type":"transform","transform":{"input":"hi","expression":"."}}]}`

// legacyUntypedDef is the shape most existing routines have: a declared type
// but no widget. hasInputForm says it carries no form contract, so nothing in
// the product validates it — and this test file must not change that.
const legacyUntypedDef = `{"name":"legacy","inputs":[{"name":"topic","type":"string","required":true}],` +
	`"steps":[{"id":"a","type":"transform","transform":{"input":"hi","expression":"."}}]}`

// presetRig is newPipelineHandlerForCRUDTest plus the schedule store the
// plan endpoints need.
func presetRig(t *testing.T) (*PipelineHandler, string, string) {
	t.Helper()
	h, user, ws := newPipelineHandlerForCRUDTest(t)
	h.SetScheduleStore(pipeline.NewScheduleStore(h.db))
	return h, user, ws
}

func seedRoutineForPreset(t *testing.T, h *PipelineHandler, wsID, slug, def string) *pipeline.Pipeline {
	t.Helper()
	now := time.Now()
	p, err := h.store.Save(t.Context(), pipeline.SaveInput{
		WorkspaceID: wsID, Slug: slug, Name: slug,
		DefinitionJSON: def, LastTestRunAt: &now, LastTestRunPassed: true,
	})
	if err != nil {
		t.Fatalf("seed routine: %v", err)
	}
	return p
}

// createSchedule posts a plan and returns the recorder.
func createSchedule(t *testing.T, h *PipelineHandler, user, ws, pipelineID string, inputs map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"name": "Nightly", "target_pipeline_id": pipelineID,
		"cron_expr": "0 9 * * *", "timezone": "UTC", "enabled": true, "inputs": inputs,
	})
	req := withAuthCtx(withWorkspaceCtx(httptest.NewRequest("POST", "/pipeline-schedules", bytes.NewReader(body)), ws), user, "OWNER")
	rr := httptest.NewRecorder()
	h.CreateSchedule(rr, req)
	return rr
}

func countSchedules(t *testing.T, h *PipelineHandler, ws string) int {
	t.Helper()
	var n int
	if err := h.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM pipeline_schedules WHERE workspace_id=? AND deleted_at IS NULL`, ws).Scan(&n); err != nil {
		t.Fatalf("count schedules: %v", err)
	}
	return n
}

// TestPresetValidation_CreateRejectsAnInvalidPreset is the defect: the plan
// must not be stored at all.
func TestPresetValidation_CreateRejectsAnInvalidPreset(t *testing.T) {
	for _, tc := range []struct {
		name   string
		inputs map[string]any
		want   string
	}{
		{"required input missing", map[string]any{}, "region"},
		{"required input explicitly null", map[string]any{"region": nil}, "region"},
		{"value outside the declared options", map[string]any{"region": "antarctica"}, "region"},
		{"wrong type for a boolean", map[string]any{"region": "eu", "dry_run": "yes"}, "dry_run"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, user, ws := presetRig(t)
			p := seedRoutineForPreset(t, h, ws, "planned", presetValidationDef)

			rr := createSchedule(t, h, user, ws, p.ID, tc.inputs)
			if rr.Code != http.StatusBadRequest && rr.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 400/422 — a plan whose preset the routine rejects must not be stored; body = %s",
					rr.Code, rr.Body.String())
			}
			if !bytes.Contains(rr.Body.Bytes(), []byte(tc.want)) {
				t.Errorf("refusal %s does not name the offending input %q", rr.Body.String(), tc.want)
			}
			if n := countSchedules(t, h, ws); n != 0 {
				t.Errorf("schedules stored = %d, want 0 — a refused plan must leave no row", n)
			}
		})
	}
}

// TestPresetValidation_CreateAcceptsTheValidCases pins everything that must
// keep working, including the three values a naive "is it empty" check gets
// wrong.
func TestPresetValidation_CreateAcceptsTheValidCases(t *testing.T) {
	for _, tc := range []struct {
		name   string
		inputs map[string]any
	}{
		{"required supplied, optional defaulted", map[string]any{"region": "eu"}},
		{"false is an answer, not an absence", map[string]any{"region": "eu", "dry_run": false}},
		{"an undeclared extra input is not rejected", map[string]any{"region": "us", "source": "manual"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, user, ws := presetRig(t)
			p := seedRoutineForPreset(t, h, ws, "planned", presetValidationDef)
			rr := createSchedule(t, h, user, ws, p.ID, tc.inputs)
			if rr.Code != http.StatusCreated {
				t.Fatalf("status = %d, want 201; body = %s", rr.Code, rr.Body.String())
			}
			if n := countSchedules(t, h, ws); n != 1 {
				t.Errorf("schedules stored = %d, want 1", n)
			}
		})
	}
}

// TestPresetValidation_ZeroIsAnAnswer is the numeric half of the same point,
// with its own routine because 0 only means something on a numeric input.
func TestPresetValidation_ZeroIsAnAnswer(t *testing.T) {
	const def = `{"name":"planned","inputs":[{"name":"retries","type":"integer","widget":"number","required":true,"min":0,"max":5}],` +
		`"steps":[{"id":"a","type":"transform","transform":{"input":"hi","expression":"."}}]}`
	h, user, ws := presetRig(t)
	p := seedRoutineForPreset(t, h, ws, "planned", def)

	if rr := createSchedule(t, h, user, ws, p.ID, map[string]any{"retries": 0}); rr.Code != http.StatusCreated {
		t.Fatalf("0 rejected as if it were absent: %d %s", rr.Code, rr.Body.String())
	}
	h2, user2, ws2 := presetRig(t)
	p2 := seedRoutineForPreset(t, h2, ws2, "planned", def)
	if rr := createSchedule(t, h2, user2, ws2, p2.ID, map[string]any{"retries": 9}); rr.Code == http.StatusCreated {
		t.Error("a value above the declared maximum was stored")
	}
}

// TestPresetValidation_LegacyUntypedRecipeStaysPermissive is the
// compatibility line. An input with no widget carries no form contract;
// nothing else in the product validates it, and neither may this.
func TestPresetValidation_LegacyUntypedRecipeStaysPermissive(t *testing.T) {
	h, user, ws := presetRig(t)
	p := seedRoutineForPreset(t, h, ws, "legacy", legacyUntypedDef)

	// Empty preset against a required-but-untyped input: permissive today,
	// permissive after. Tightening this belongs to a decision about the
	// legacy contract, not to a preset-validation fix.
	if rr := createSchedule(t, h, user, ws, p.ID, map[string]any{}); rr.Code != http.StatusCreated {
		t.Fatalf("a legacy untyped recipe must not become stricter: %d %s", rr.Code, rr.Body.String())
	}
}

// TestPresetValidation_UpdateIsGuardedToo — editing a plan onto an invalid
// preset is the same hole through the other door, and the stored row must be
// left as it was.
func TestPresetValidation_UpdateIsGuardedToo(t *testing.T) {
	h, user, ws := presetRig(t)
	p := seedRoutineForPreset(t, h, ws, "planned", presetValidationDef)
	rr := createSchedule(t, h, user, ws, p.ID, map[string]any{"region": "eu"})
	if rr.Code != http.StatusCreated {
		t.Fatalf("seed plan: %d %s", rr.Code, rr.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil || created.ID == "" {
		t.Fatalf("decode created plan: %v %s", err, rr.Body.String())
	}

	body, _ := json.Marshal(map[string]any{"inputs": map[string]any{"region": "antarctica"}})
	req := withAuthCtx(withWorkspaceCtx(httptest.NewRequest("PATCH", "/pipeline-schedules/"+created.ID, bytes.NewReader(body)), ws), user, "OWNER")
	req.SetPathValue("scheduleId", created.ID)
	up := httptest.NewRecorder()
	h.UpdateSchedule(up, req)

	if up.Code != http.StatusBadRequest && up.Code != http.StatusUnprocessableEntity {
		t.Fatalf("update status = %d, want 400/422; body = %s", up.Code, up.Body.String())
	}
	var stored string
	if err := h.db.QueryRowContext(t.Context(), `SELECT inputs_json FROM pipeline_schedules WHERE id=?`, created.ID).Scan(&stored); err != nil {
		t.Fatalf("read preset: %v", err)
	}
	if stored != `{"region":"eu"}` {
		t.Errorf("the stored preset changed despite the refusal: %s", stored)
	}
}

// TestPresetValidation_RunPathAlreadyRejects documents the half that was
// already right, so a future reader does not "fix" it twice. It is also the
// evidence for the correction to the acceptance protocol's finding N3: the
// server DOES validate a run's inputs — for every input that carries a form
// contract.
func TestPresetValidation_RunPathAlreadyRejects(t *testing.T) {
	dsl, err := pipeline.Parse([]byte(presetValidationDef))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := pipeline.ValidateFormInputs(dsl, map[string]any{}); err == nil {
		t.Error("a missing required form input was accepted")
	}
	if err := pipeline.ValidateFormInputs(dsl, map[string]any{"region": "eu"}); err != nil {
		t.Errorf("a valid input set was rejected: %v", err)
	}
	legacy, err := pipeline.Parse([]byte(legacyUntypedDef))
	if err != nil {
		t.Fatalf("parse legacy: %v", err)
	}
	if err := pipeline.ValidateFormInputs(legacy, map[string]any{}); err != nil {
		t.Errorf("a legacy untyped input must stay unvalidated: %v", err)
	}
}

// TestPresetValidation_TriggerPresetIsGatedAtSaveTime closes the third door.
// A routine save can carry a trigger, and the trigger carries a preset for
// the very definition being published — so the two can be born disagreeing
// in a single request unless the save checks them against each other.
// ErrInvalidTrigger keeps the 422 both save doors already map.
func TestPresetValidation_TriggerPresetIsGatedAtSaveTime(t *testing.T) {
	h, user, ws := presetRig(t)

	save := func(inputs map[string]any) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]any{
			"slug": "planned", "name": "Planned",
			"definition":     json.RawMessage(presetValidationDef),
			"skip_test_gate": true,
			"trigger": map[string]any{
				"kind": "schedule", "cron": "0 9 * * *", "timezone": "UTC", "inputs": inputs,
			},
		})
		req := withAuthCtx(withWorkspaceCtx(httptest.NewRequest("POST", "/save", bytes.NewReader(body)), ws), user, "OWNER")
		rr := httptest.NewRecorder()
		h.Save(rr, req)
		return rr
	}

	rr := save(map[string]any{"region": "antarctica"})
	if rr.Code != http.StatusUnprocessableEntity && rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 422/400 — a trigger preset the recipe rejects must not be stored; body = %s",
			rr.Code, rr.Body.String())
	}
	if !bytes.Contains(rr.Body.Bytes(), []byte("region")) {
		t.Errorf("refusal does not name the offending input: %s", rr.Body.String())
	}
	// The whole save rolls back — the routine is not created either, which is
	// the existing atomicity contract for a bad trigger.
	var pipes int
	if err := h.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM pipelines WHERE workspace_id=? AND slug='planned' AND deleted_at IS NULL`, ws).Scan(&pipes); err != nil {
		t.Fatalf("count pipelines: %v", err)
	}
	if pipes != 0 {
		t.Errorf("pipelines = %d, want 0 — a refused trigger must roll the save back", pipes)
	}

	if rr := save(map[string]any{"region": "eu"}); rr.Code != http.StatusOK && rr.Code != http.StatusCreated {
		t.Fatalf("a valid trigger preset was refused: %d %s", rr.Code, rr.Body.String())
	}
}

// TestPresetValidation_UpdateJudgesOnlyWhatItWrites is the trap this gate
// must not set. A plan created before the gate existed can be carrying a
// preset its recipe already rejects. If a PATCH re-validated the stored
// preset it fell back to, the operator could not disable that plan, nor fix
// its cron, without first repairing a preset the request never mentions —
// and disabling it is the very first thing they would reach for.
func TestPresetValidation_UpdateJudgesOnlyWhatItWrites(t *testing.T) {
	h, user, ws := presetRig(t)
	p := seedRoutineForPreset(t, h, ws, "planned", presetValidationDef)
	// A legacy row: written straight to the table, the way one that predates
	// this gate exists in a real database.
	if _, err := h.db.ExecContext(t.Context(),
		`INSERT INTO pipeline_schedules(id,workspace_id,name,target_pipeline_id,cron_expr,timezone,inputs_json,enabled)
		 VALUES('legacy',?,'Legacy',?,'0 9 * * *','UTC','{"region":"antarctica"}',1)`, ws, p.ID); err != nil {
		t.Fatalf("seed legacy plan: %v", err)
	}

	patch := func(body string) *httptest.ResponseRecorder {
		req := withAuthCtx(withWorkspaceCtx(httptest.NewRequest("PATCH", "/pipeline-schedules/legacy", bytes.NewBufferString(body)), ws), user, "OWNER")
		req.SetPathValue("scheduleId", "legacy")
		rr := httptest.NewRecorder()
		h.UpdateSchedule(rr, req)
		return rr
	}

	if rr := patch(`{"enabled":false}`); rr.Code != http.StatusOK {
		t.Fatalf("disabling a plan with a stale preset: %d %s — the operator must be able to switch it off",
			rr.Code, rr.Body.String())
	}
	if rr := patch(`{"cron_expr":"0 10 * * *"}`); rr.Code != http.StatusOK {
		t.Fatalf("rescheduling a plan with a stale preset: %d %s", rr.Code, rr.Body.String())
	}
	// But writing a NEW invalid preset is still refused.
	if rr := patch(`{"inputs":{"region":"atlantis"}}`); rr.Code != http.StatusBadRequest {
		t.Errorf("writing a fresh invalid preset: %d %s, want 400", rr.Code, rr.Body.String())
	}
	// And repairing it works.
	if rr := patch(`{"inputs":{"region":"eu"}}`); rr.Code != http.StatusOK {
		t.Errorf("repairing the preset: %d %s", rr.Code, rr.Body.String())
	}
}

// TestPresetValidation_PinnedPlanIsCheckedAgainstItsPinnedVersion — a plan
// that pins v1 runs v1, so its preset must be judged against v1 even after
// HEAD moves on. Checking it against HEAD would reject a preset that is
// perfectly correct for the recipe this plan actually executes.
func TestPresetValidation_PinnedPlanIsCheckedAgainstItsPinnedVersion(t *testing.T) {
	h, user, ws := presetRig(t)
	p := seedRoutineForPreset(t, h, ws, "planned", presetValidationDef)

	// v2 renames the input; v1 still wants `region`.
	now := time.Now()
	const v2 = `{"name":"planned","inputs":[{"name":"zone","type":"string","widget":"select","options":["eu","us"],"required":true}],` +
		`"steps":[{"id":"a","type":"transform","transform":{"input":"hi","expression":"."}}]}`
	if _, err := h.store.Save(t.Context(), pipeline.SaveInput{
		WorkspaceID: ws, Slug: "planned", Name: "Planned",
		DefinitionJSON: v2, LastTestRunAt: &now, LastTestRunPassed: true,
	}); err != nil {
		t.Fatalf("save v2: %v", err)
	}

	one := 1
	body, _ := json.Marshal(map[string]any{
		"name": "Pinned", "target_pipeline_id": p.ID, "target_pipeline_version": &one,
		"cron_expr": "0 9 * * *", "timezone": "UTC", "enabled": true,
		"inputs": map[string]any{"region": "eu"}, // correct for v1, wrong for HEAD
	})
	req := withAuthCtx(withWorkspaceCtx(httptest.NewRequest("POST", "/pipeline-schedules", bytes.NewReader(body)), ws), user, "OWNER")
	rr := httptest.NewRecorder()
	h.CreateSchedule(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("a preset correct for the pinned version was refused: %d %s", rr.Code, rr.Body.String())
	}
}

// TestPresetValidation_TriggerWithNoInputsIsStillJudged — the empty preset is
// the unsatisfiable one when the recipe has a required input, so skipping
// validation for "no inputs supplied" would let exactly the worst case
// through.
func TestPresetValidation_TriggerWithNoInputsIsStillJudged(t *testing.T) {
	h, user, ws := presetRig(t)

	body, _ := json.Marshal(map[string]any{
		"slug": "planned", "name": "Planned",
		"definition":     json.RawMessage(presetValidationDef),
		"skip_test_gate": true,
		"trigger":        map[string]any{"kind": "schedule", "cron": "0 9 * * *", "timezone": "UTC"},
	})
	req := withAuthCtx(withWorkspaceCtx(httptest.NewRequest("POST", "/save", bytes.NewReader(body)), ws), user, "OWNER")
	rr := httptest.NewRecorder()
	h.Save(rr, req)

	if rr.Code != http.StatusUnprocessableEntity && rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 422/400 — a plan supplying nothing for a required input cannot run; body = %s",
			rr.Code, rr.Body.String())
	}
	if !bytes.Contains(rr.Body.Bytes(), []byte("region")) {
		t.Errorf("refusal does not name the unanswered input: %s", rr.Body.String())
	}

	// A recipe with no declared inputs still takes a bare trigger.
	const noInputs = `{"name":"plain","steps":[{"id":"a","type":"transform","transform":{"input":"hi","expression":"."}}]}`
	body2, _ := json.Marshal(map[string]any{
		"slug": "plain", "name": "Plain",
		"definition":     json.RawMessage(noInputs),
		"skip_test_gate": true,
		"trigger":        map[string]any{"kind": "schedule", "cron": "0 9 * * *", "timezone": "UTC"},
	})
	req2 := withAuthCtx(withWorkspaceCtx(httptest.NewRequest("POST", "/save", bytes.NewReader(body2)), ws), user, "OWNER")
	rr2 := httptest.NewRecorder()
	h.Save(rr2, req2)
	if rr2.Code != http.StatusOK && rr2.Code != http.StatusCreated {
		t.Errorf("a bare trigger on a recipe with no inputs was refused: %d %s", rr2.Code, rr2.Body.String())
	}
}

// The two holes the opponent review of #2498 reproduced. Both are the same
// mistake from different sides: the gate judged the preset against the
// recipe the plan used to point at, not the one it will point at once the
// PATCH lands.

// presetV2Def is presetValidationDef with the required input renamed, so a
// preset that fits v1 does not fit v2.
const presetV2Def = `{"name":"planned","inputs":[` +
	`{"name":"zone","type":"string","widget":"select","options":["eu","us"],"required":true}],` +
	`"steps":[{"id":"a","type":"transform","transform":{"input":"hi","expression":"."}}]}`

// pinnedPlanOnV1 publishes v1 then v2 and returns a plan pinned to v1 whose
// preset fits v1 only.
func pinnedPlanOnV1(t *testing.T, h *PipelineHandler, user, ws string) (*pipeline.Pipeline, string) {
	t.Helper()
	p := seedRoutineForPreset(t, h, ws, "planned", presetValidationDef)
	now := time.Now()
	if _, err := h.store.Save(t.Context(), pipeline.SaveInput{
		WorkspaceID: ws, Slug: "planned", Name: "Planned",
		DefinitionJSON: presetV2Def, LastTestRunAt: &now, LastTestRunPassed: true,
	}); err != nil {
		t.Fatalf("publish v2: %v", err)
	}
	one := 1
	body, _ := json.Marshal(map[string]any{
		"name": "Pinned", "target_pipeline_id": p.ID, "target_pipeline_version": &one,
		"cron_expr": "0 9 * * *", "timezone": "UTC", "enabled": true,
		"inputs": map[string]any{"region": "eu"},
	})
	req := withAuthCtx(withWorkspaceCtx(httptest.NewRequest("POST", "/pipeline-schedules", bytes.NewReader(body)), ws), user, "OWNER")
	rr := httptest.NewRecorder()
	h.CreateSchedule(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("seed pinned plan: %d %s", rr.Code, rr.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil || created.ID == "" {
		t.Fatalf("decode plan: %v %s", err, rr.Body.String())
	}
	return p, created.ID
}

func patchPlan(t *testing.T, h *PipelineHandler, user, ws, id, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := withAuthCtx(withWorkspaceCtx(httptest.NewRequest("PATCH", "/pipeline-schedules/"+id, bytes.NewBufferString(body)), ws), user, "OWNER")
	req.SetPathValue("scheduleId", id)
	rr := httptest.NewRecorder()
	h.UpdateSchedule(rr, req)
	return rr
}

func storedPlan(t *testing.T, h *PipelineHandler, id string) (inputs string, pinned *int) {
	t.Helper()
	var pv sql.NullInt64
	if err := h.db.QueryRowContext(t.Context(), `SELECT inputs_json, target_pipeline_version FROM pipeline_schedules WHERE id=?`, id).Scan(&inputs, &pv); err != nil {
		t.Fatalf("read plan: %v", err)
	}
	if pv.Valid {
		v := int(pv.Int64)
		pinned = &v
	}
	return inputs, pinned
}

// TestPresetValidation_UnpinningIsJudgedAgainstHead — an explicit
// `target_pipeline_version: null` moves the plan onto HEAD. The handler
// already resolves that (absent keeps the pin, explicit null clears it); the
// gate then re-applied the OLD pin on top, validated a v1 preset against v1,
// and let a plan that will run v2 store a preset v2 rejects.
func TestPresetValidation_UnpinningIsJudgedAgainstHead(t *testing.T) {
	h, user, ws := presetRig(t)
	_, id := pinnedPlanOnV1(t, h, user, ws)

	rr := patchPlan(t, h, user, ws, id, `{"target_pipeline_version":null,"inputs":{"region":"eu"}}`)
	if rr.Code == http.StatusOK {
		t.Fatalf("unpinning onto HEAD with a preset HEAD rejects returned 200: %s", rr.Body.String())
	}
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
	inputs, pinned := storedPlan(t, h, id)
	if inputs != `{"region":"eu"}` || pinned == nil || *pinned != 1 {
		t.Errorf("the refused PATCH changed the row: inputs=%s pinned=%v", inputs, pinned)
	}

	// The same unpin with a preset that fits HEAD goes through.
	if rr := patchPlan(t, h, user, ws, id, `{"target_pipeline_version":null,"inputs":{"zone":"eu"}}`); rr.Code != http.StatusOK {
		t.Errorf("a correct unpin was refused: %d %s", rr.Code, rr.Body.String())
	}
}

// TestPresetValidation_RepinningWithoutInputsIsJudgedToo — moving the pin
// changes what the stored preset will be fed to. "Validate only what the
// request writes" was right about disabling a plan and wrong about this: a
// PATCH that changes the target has changed what the preset means.
func TestPresetValidation_RepinningWithoutInputsIsJudgedToo(t *testing.T) {
	h, user, ws := presetRig(t)
	_, id := pinnedPlanOnV1(t, h, user, ws)

	rr := patchPlan(t, h, user, ws, id, `{"target_pipeline_version":2}`)
	if rr.Code == http.StatusOK {
		t.Fatalf("repinning onto a version that rejects the stored preset returned 200: %s", rr.Body.String())
	}
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
	if !bytes.Contains(rr.Body.Bytes(), []byte("zone")) {
		t.Errorf("refusal does not name the unsatisfied input: %s", rr.Body.String())
	}
	_, pinned := storedPlan(t, h, id)
	if pinned == nil || *pinned != 1 {
		t.Errorf("the refused repin changed the stored pin: %v", pinned)
	}

	// Disabling that same plan is still free — it changes nothing the preset
	// is fed to.
	if rr := patchPlan(t, h, user, ws, id, `{"enabled":false}`); rr.Code != http.StatusOK {
		t.Errorf("disabling a plan must stay possible: %d %s", rr.Code, rr.Body.String())
	}
	// And repinning together with a matching preset goes through.
	if rr := patchPlan(t, h, user, ws, id, `{"target_pipeline_version":2,"inputs":{"zone":"us"}}`); rr.Code != http.StatusOK {
		t.Errorf("a repin with a matching preset was refused: %d %s", rr.Code, rr.Body.String())
	}
}
