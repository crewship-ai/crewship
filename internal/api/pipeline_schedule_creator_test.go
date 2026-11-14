package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/pipeline"
)

// #5339 owner decision A: a schedule records who created or last saved it.
// Only a person's own credential on the public API counts as that person;
// every internal path, and a propagated X-Caller-User-Id, is an agent.

// asPerson is a request authenticated the way the auth middleware does it
// for a person: their session plus the original credential it captured.
func asPerson(r *http.Request, userID, wsID, role string) *http.Request {
	original := personCredential(userID)
	ctx := withUser(r.Context(), &AuthUser{ID: userID, SessionID: original.ID, OriginalCredential: &original})
	ctx = auth.WithOriginalCredential(ctx, original)
	return r.WithContext(withWorkspace(ctx, wsID, role))
}

// personCredential is a well-formed original credential, as the auth
// middleware captures it for a session.
func personCredential(userID string) auth.OriginalCredential {
	return auth.OriginalCredential{
		UserID: userID, Kind: "session", ID: "sess-" + userID,
		ExpiresAt:             time.Now().Add(time.Hour).UTC().Truncate(time.Second).Format(time.RFC3339Nano),
		Binding:               "sha256:" + strings.Repeat("ab", 32),
		UserAuthorityRevision: "1",
	}
}

func TestScheduleCreator_OnlyAPersonsOwnCredentialIsAPerson(t *testing.T) {
	h, db, userID, wsID := scheduleHandlerRig(t)
	seedPipelineRow(t, db, wsID, "pln_cr", "creator-routine")
	body := `{"cron_expr":"0 * * * *","target_pipeline_slug":"creator-routine"}`
	create := func(r *http.Request) scheduleResponse {
		t.Helper()
		rr := httptest.NewRecorder()
		h.CreateSchedule(rr, r)
		if rr.Code != http.StatusCreated {
			t.Fatalf("create: %d %s", rr.Code, rr.Body)
		}
		var out scheduleResponse
		_ = json.Unmarshal(rr.Body.Bytes(), &out)
		return out
	}
	patch := func(r *http.Request, id string) scheduleResponse {
		t.Helper()
		r.SetPathValue("scheduleId", id)
		rr := httptest.NewRecorder()
		h.UpdateSchedule(rr, r)
		if rr.Code != http.StatusOK {
			t.Fatalf("update: %d %s", rr.Code, rr.Body)
		}
		var out scheduleResponse
		_ = json.Unmarshal(rr.Body.Bytes(), &out)
		return out
	}
	post := func() *http.Request {
		return httptest.NewRequest("POST", "/api/v1/workspaces/"+wsID+"/pipeline-schedules", strings.NewReader(body))
	}

	person := create(asPerson(post(), userID, wsID, "OWNER"))
	if person.CreatorKind != "human" || person.CreatorUserID != userID {
		t.Fatalf("a person's create: %+v", person)
	}

	for name, r := range map[string]*http.Request{
		"a user id with no captured credential": withWorkspaceUser(post(), userID, wsID, "OWNER"),
		"a credential naming someone else": func() *http.Request {
			r := asPerson(post(), userID, wsID, "OWNER")
			return r.WithContext(auth.WithOriginalCredential(r.Context(), personCredential("someone-else")))
		}(),
		"a person's credential next to a crew token": func() *http.Request {
			r := asPerson(post(), userID, wsID, "OWNER")
			return r.WithContext(context.WithValue(r.Context(), ctxInternalTokenCrew, "crew-x"))
		}(),
		"a person's credential next to a workspace-bound internal token": func() *http.Request {
			r := asPerson(post(), userID, wsID, "OWNER")
			return r.WithContext(context.WithValue(r.Context(), ctxInternalTokenWS, wsID))
		}(),
	} {
		if got := create(r); got.CreatorKind != "agent" || got.CreatorUserID != "" {
			t.Fatalf("%s recorded %q/%q, want agent", name, got.CreatorKind, got.CreatorUserID)
		}
	}

	// The internal route, with the person's id propagated, is an agent.
	adapter := NewRoutineInternalAdapter(h)
	r := httptest.NewRequest(http.MethodPost, "/?workspace_id="+wsID, strings.NewReader(body))
	r.Header.Set("X-Caller-User-Id", userID)
	rr := httptest.NewRecorder()
	adapter.CreateSchedule(rr, r)
	if rr.Code != http.StatusCreated && rr.Code != http.StatusAccepted {
		t.Fatalf("internal create: %d %s", rr.Code, rr.Body)
	}
	var internal scheduleResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &internal)
	if internal.CreatorKind != "agent" || internal.CreatorUserID != "" {
		t.Fatalf("internal create recorded %+v, want agent", internal)
	}

	// A save of what the schedule runs overwrites: a person changing it
	// takes it over, an agent changing a person's schedule takes the
	// person's vouching away.
	path := "/api/v1/workspaces/" + wsID + "/pipeline-schedules/" + internal.ID
	if got := patch(asPerson(httptest.NewRequest("PATCH", path, strings.NewReader(`{"cron_expr":"0 2 * * *"}`)), userID, wsID, "OWNER"), internal.ID); got.CreatorKind != "human" || got.CreatorUserID != userID {
		t.Fatalf("a person's re-save: %+v", got)
	}
	if got := patch(withWorkspaceUser(httptest.NewRequest("PATCH", path, strings.NewReader(`{"cron_expr":"0 3 * * *"}`)), userID, wsID, "OWNER"), internal.ID); got.CreatorKind != "agent" || got.CreatorUserID != "" {
		t.Fatalf("an unproven re-save kept the person: %+v", got)
	}
	var kind, user any
	if err := db.QueryRow(`SELECT creator_kind,creator_user_id FROM pipeline_schedules WHERE id=?`, internal.ID).Scan(&kind, &user); err != nil || kind != "agent" || user != nil {
		t.Fatalf("stored creator %v/%v %v", kind, user, err)
	}
}

// The agent's save_routine path records its trigger's schedule as an
// agent's, whatever caller header it carries.
func TestScheduleCreator_AgentSaveRoutineTriggerIsAnAgent(t *testing.T) {
	h, db, wsID, crewID := triggerSaveRig(t)
	body := internalSaveTriggerBody(t, h, wsID, crewID, "agent-cron", `{"kind":"schedule","cron":"0 9 * * *","timezone":"UTC"}`, "")
	req := httptest.NewRequest("POST", "/api/v1/internal/pipelines/save", bytes.NewBuffer(body))
	req.Header.Set("X-Caller-User-Id", "someone")
	rr := httptest.NewRecorder()
	h.InternalSave(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("internal save: %d %s", rr.Code, rr.Body)
	}
	var kind, user any
	if err := db.QueryRow(`SELECT s.creator_kind,s.creator_user_id FROM pipeline_schedules s JOIN pipelines p ON p.id=s.target_pipeline_id WHERE p.slug='agent-cron'`).Scan(&kind, &user); err != nil || kind != "agent" || user != nil {
		t.Fatalf("agent trigger creator %v/%v %v", kind, user, err)
	}
}

// Legacy rows (saved before the creator existed) stay unknown and show no
// creator.
func TestScheduleCreator_LegacyRowShowsNoCreator(t *testing.T) {
	h, db, _, wsID := scheduleHandlerRig(t)
	seedPipelineRow(t, db, wsID, "pln_legacy", "legacy-routine")
	mustExec(t, db, `INSERT INTO pipeline_schedules(id,workspace_id,name,target_pipeline_id,cron_expr,timezone,inputs_json,enabled) VALUES('psch_legacy',?,'old','pln_legacy','0 8 * * *','UTC','{}',1)`, wsID)
	sched, err := h.schedules.GetByID(t.Context(), "psch_legacy")
	if err != nil {
		t.Fatal(err)
	}
	if out := h.toScheduleResponse(sched, "legacy-routine", ""); out.CreatorKind != "" || out.CreatorUserID != "" {
		t.Fatalf("legacy row shows a creator: %+v", out)
	}
	if _, err := db.Exec(`UPDATE pipeline_schedules SET creator_kind='human' WHERE id='psch_legacy'`); err == nil {
		t.Fatal("a human creator without a user id was stored")
	}
	if _, err := db.Exec(`UPDATE pipeline_schedules SET creator_kind='agent',creator_user_id='u' WHERE id='psch_legacy'`); err == nil {
		t.Fatal("an agent creator with a user id was stored")
	}
}

// Enabling or disabling a schedule, renaming it or changing its failure
// limit does not change what it runs, so it does not change who vouches for
// it (review round 3): an admin re-enabling an agent's schedule after a
// circuit-breaker trip lends it nothing. Changing the cron, inputs, target,
// pin or wake check is a person adopting it.
func TestScheduleCreator_OnlyAContentChangeTakesTheScheduleOver(t *testing.T) {
	h, db, userID, wsID := scheduleHandlerRig(t)
	seedPipelineRow(t, db, wsID, "pln_en", "enable-routine")
	adapter := NewRoutineInternalAdapter(h)
	rr := httptest.NewRecorder()
	adapter.CreateSchedule(rr, httptest.NewRequest(http.MethodPost, "/?workspace_id="+wsID, strings.NewReader(`{"cron_expr":"0 * * * *","target_pipeline_slug":"enable-routine"}`)))
	if rr.Code != http.StatusCreated && rr.Code != http.StatusAccepted {
		t.Fatalf("internal create: %d %s", rr.Code, rr.Body)
	}
	var agent scheduleResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &agent)
	stored := func() (kind, user, setAt any) {
		t.Helper()
		if err := db.QueryRow(`SELECT creator_kind,creator_user_id,creator_set_at FROM pipeline_schedules WHERE id=?`, agent.ID).Scan(&kind, &user, &setAt); err != nil {
			t.Fatal(err)
		}
		return
	}
	_, _, agentSetAt := stored()
	path := "/api/v1/workspaces/" + wsID + "/pipeline-schedules/" + agent.ID
	patch := func(body string) {
		t.Helper()
		r := asPerson(httptest.NewRequest("PATCH", path, strings.NewReader(body)), userID, wsID, "OWNER")
		r.SetPathValue("scheduleId", agent.ID)
		rr := httptest.NewRecorder()
		h.UpdateSchedule(rr, r)
		if rr.Code != http.StatusOK {
			t.Fatalf("patch %s: %d %s", body, rr.Code, rr.Body)
		}
	}
	for _, body := range []string{`{"enabled":false}`, `{"enabled":true}`, `{"name":"renamed"}`, `{"max_consecutive_failures":3}`} {
		patch(body)
		if kind, user, setAt := stored(); kind != "agent" || user != nil || setAt != agentSetAt {
			t.Fatalf("admin %s changed the creator to %v/%v at %v", body, kind, user, setAt)
		}
	}
	patch(`{"cron_expr":"0 9 * * *"}`)
	if kind, user, _ := stored(); kind != "human" || user != userID {
		t.Fatalf("an admin's cron edit recorded %v/%v, want human/%s", kind, user, userID)
	}
	var member any
	if err := db.QueryRow(`SELECT creator_member_id FROM pipeline_schedules WHERE id=?`, agent.ID).Scan(&member); err != nil || member == nil {
		t.Fatalf("a person's adoption recorded no membership: %v %v", member, err)
	}
}

// markerRunner records the host-only fields each step reached the runner
// with.
type markerRunner struct{ reqs []pipeline.AgentStepRequest }

func (m *markerRunner) RunStep(_ context.Context, req pipeline.AgentStepRequest) (pipeline.AgentStepResult, error) {
	m.reqs = append(m.reqs, req)
	return pipeline.AgentStepResult{Output: "ok"}, nil
}

// "Run now" is the host's own fire of the schedule: its steps carry the
// schedule as the vouched marker, so the schedule's creator (never the
// person pressing the button) decides.
func TestScheduleCreator_RunNowIsTheHostsFire(t *testing.T) {
	h, db, user, workspace := scheduleHandlerRig(t)
	runner := &markerRunner{}
	h.SetRunner(runner)
	h.SetRunStore(pipeline.NewRunStore(db))
	const def = `{"dsl_version":"1.0","name":"run-now","steps":[{"id":"a","type":"agent_run","agent_slug":"agent_lead","prompt":"hi"}]}`
	now := time.Now().UTC().Format(time.RFC3339)
	mustExec(t, db, `INSERT INTO pipelines (id, workspace_id, slug, name, definition_json, definition_hash, head_version, created_at, updated_at, last_test_run_at)
		VALUES ('run-now-pipeline', ?, 'run-now', 'run-now', ?, 'h1', 1, ?, ?, ?)`, workspace, def, now, now, now)
	mustExec(t, db, `INSERT INTO pipeline_versions (id, pipeline_id, version, definition_json, definition_hash, author_type, author_id, created_at)
		VALUES ('plnv_run_now_v1', 'run-now-pipeline', 1, ?, 'h1', 'user', 'u_test', ?)`, def, now)
	saved, err := pipeline.NewScheduleStore(db).Save(t.Context(), pipeline.SaveScheduleInput{WorkspaceID: workspace, Name: "run now", TargetPipelineID: "run-now-pipeline", CronExpr: "0 8 * * *", Timezone: "UTC", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	req := withWorkspaceUser(httptest.NewRequest("POST", "/schedule/run", nil), user, workspace, "OWNER")
	req.SetPathValue("scheduleId", saved.ID)
	rr := httptest.NewRecorder()
	h.RunSchedule(rr, req)
	if rr.Code != http.StatusOK || len(runner.reqs) == 0 {
		t.Fatalf("run now: %d %s, %d steps", rr.Code, rr.Body, len(runner.reqs))
	}
	for i, c := range runner.reqs {
		if c.TriggeredVia != pipeline.TriggeredViaSchedule || c.TriggeredByID != saved.ID || c.ArtifactScheduleID != saved.ID {
			t.Fatalf("step %d reached the runner as via=%q by=%q marker=%q", i, c.TriggeredVia, c.TriggeredByID, c.ArtifactScheduleID)
		}
	}
}

// A person's public routine save with a schedule trigger records that
// person as the schedule's creator.
func TestScheduleCreator_PersonsRoutineSaveTriggerIsThePerson(t *testing.T) {
	h, user, ws := presetRig(t)
	body, _ := json.Marshal(map[string]any{
		"slug": "person-cron", "name": "Person cron",
		"definition":     json.RawMessage(presetValidationDef),
		"skip_test_gate": true,
		"trigger":        map[string]any{"kind": "schedule", "cron": "0 9 * * *", "timezone": "UTC", "inputs": map[string]any{"region": "eu"}},
	})
	rr := httptest.NewRecorder()
	h.Save(rr, asPerson(httptest.NewRequest("POST", "/save", bytes.NewReader(body)), user, ws, "OWNER"))
	if rr.Code != http.StatusOK && rr.Code != http.StatusCreated {
		t.Fatalf("save: %d %s", rr.Code, rr.Body)
	}
	var kind, who, member any
	if err := h.db.QueryRow(`SELECT s.creator_kind,s.creator_user_id,s.creator_member_id FROM pipeline_schedules s JOIN pipelines p ON p.id=s.target_pipeline_id WHERE p.slug='person-cron'`).Scan(&kind, &who, &member); err != nil || kind != "human" || who != user || member == nil {
		t.Fatalf("a person's trigger creator %v/%v member %v %v", kind, who, member, err)
	}
}
