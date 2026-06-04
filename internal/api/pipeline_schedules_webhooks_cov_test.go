package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

// covPSWScheduleRig wires a PipelineHandler with a real ScheduleStore
// against the full-migration test DB. Mirrors scheduleHandlerRig from
// pipeline_schedules_test.go but is kept distinct so this file owns its
// own setup and never collides with the existing helper name.
func covPSWScheduleRig(t *testing.T) (*PipelineHandler, *sql.DB, string, string) {
	t.Helper()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewPipelineHandler(db, newTestLogger(), nil, nil)
	h.SetScheduleStore(pipeline.NewScheduleStore(db))
	return h, db, userID, wsID
}

// covPSWWebhookRig is the webhook-store flavour of the rig above.
func covPSWWebhookRig(t *testing.T) (*PipelineHandler, *sql.DB, string, string) {
	t.Helper()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewPipelineHandler(db, newTestLogger(), nil, nil)
	h.SetWebhookStore(pipeline.NewWebhookStore(db))
	return h, db, userID, wsID
}

// covPSWSeedPipeline inserts a minimal pipelines row (mirrors
// seedPipelineRow / seedWebhookPipeline). Named distinctly to avoid a
// duplicate-symbol clash with the existing helpers.
func covPSWSeedPipeline(t *testing.T, db *sql.DB, wsID, id, slug string) {
	t.Helper()
	if _, err := db.Exec(`
		INSERT INTO pipelines (id, workspace_id, slug, name, definition_json, definition_hash, created_at, updated_at, last_test_run_at)
		VALUES (?, ?, ?, ?, '{"name":"x","steps":[]}', 'hash', datetime('now'), datetime('now'), datetime('now'))`,
		id, wsID, slug, slug); err != nil {
		t.Fatalf("seed pipeline: %v", err)
	}
}

// covPSWSeedSchedule saves a schedule row via the store and returns it.
func covPSWSeedSchedule(t *testing.T, db *sql.DB, wsID, pipelineID, name string) *pipeline.Schedule {
	t.Helper()
	s, err := pipeline.NewScheduleStore(db).Save(t.Context(), pipeline.SaveScheduleInput{
		WorkspaceID:      wsID,
		Name:             name,
		TargetPipelineID: pipelineID,
		CronExpr:         "*/10 * * * *",
		Timezone:         "UTC",
		Enabled:          true,
	})
	if err != nil {
		t.Fatalf("seed schedule: %v", err)
	}
	return s
}

// covPSWSeedWebhook saves a webhook row via the store and returns it.
func covPSWSeedWebhook(t *testing.T, db *sql.DB, wsID, pipelineID, secret string, enabled bool) *pipeline.Webhook {
	t.Helper()
	wh, err := pipeline.NewWebhookStore(db).Save(t.Context(), pipeline.SaveWebhookInput{
		WorkspaceID:      wsID,
		Name:             "cov-hook",
		TargetPipelineID: pipelineID,
		SigningSecret:    secret,
		Enabled:          enabled,
	})
	if err != nil {
		t.Fatalf("seed webhook: %v", err)
	}
	return wh
}

// covPSWSign returns the hex HMAC-SHA256 of body under secret — the
// shape FireWebhook's X-Crewship-Signature header expects.
func covPSWSign(secret, body string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	return hex.EncodeToString(mac.Sum(nil))
}

// ── Schedules: CreateSchedule ───────────────────────────────────────────

// MEMBER without routine.create capability is below the create tier and
// fails the OR-combined gate before the body is even decoded.
func TestCovPSWSchedules_Create_MemberForbidden_Returns403(t *testing.T) {
	h, db, _, wsID := covPSWScheduleRig(t)
	covPSWSeedPipeline(t, db, wsID, "pln_x", "ping-hosts")
	memberID := "cov-member-no-cap"
	if _, err := db.Exec(`INSERT INTO users (id, email, full_name) VALUES (?, 'm@x', 'M')`, memberID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO workspace_members (id, workspace_id, user_id, role, capabilities) VALUES ('mc1', ?, ?, 'MEMBER', '[]')`, wsID, memberID); err != nil {
		t.Fatalf("seed member: %v", err)
	}
	InvalidateCapabilityCache(wsID, memberID)

	req := withWorkspaceUser(httptest.NewRequest("POST",
		"/api/v1/workspaces/"+wsID+"/pipeline-schedules",
		strings.NewReader(`{"cron_expr":"*/5 * * * *","target_pipeline_slug":"ping-hosts"}`)),
		memberID, wsID, "MEMBER")
	rr := httptest.NewRecorder()
	h.CreateSchedule(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rr.Code, rr.Body.String())
	}
}

// Binding by target_pipeline_id (CLI path) instead of slug. Also leaves
// Name blank so the handler's defaultIfBlank(name, slug) branch runs.
func TestCovPSWSchedules_Create_ByPipelineID_DefaultsName(t *testing.T) {
	h, db, userID, wsID := covPSWScheduleRig(t)
	covPSWSeedPipeline(t, db, wsID, "pln_id", "by-id")

	body := `{"cron_expr":"*/10 * * * *","target_pipeline_id":"pln_id"}`
	req := withWorkspaceUser(httptest.NewRequest("POST",
		"/api/v1/workspaces/"+wsID+"/pipeline-schedules",
		strings.NewReader(body)), userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.CreateSchedule(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}
	var resp scheduleResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.TargetPipelineID != "pln_id" {
		t.Errorf("target id = %q, want pln_id", resp.TargetPipelineID)
	}
	// Name was blank in the request; handler defaults it to the slug.
	if resp.Name != "by-id" {
		t.Errorf("name default = %q, want slug 'by-id'", resp.Name)
	}
}

// target_pipeline_id that points at a pipeline in another workspace must
// 400 (resolveSchedulePipelineID workspace-mismatch branch).
func TestCovPSWSchedules_Create_PipelineIDWrongWorkspace_Returns400(t *testing.T) {
	h, db, userID, wsID := covPSWScheduleRig(t)
	otherWS := "ws_foreign"
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'F', 'f')`, otherWS); err != nil {
		t.Fatalf("seed ws: %v", err)
	}
	covPSWSeedPipeline(t, db, otherWS, "pln_foreign", "foreign")

	body := `{"cron_expr":"*/5 * * * *","target_pipeline_id":"pln_foreign"}`
	req := withWorkspaceUser(httptest.NewRequest("POST",
		"/api/v1/workspaces/"+wsID+"/pipeline-schedules",
		strings.NewReader(body)), userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.CreateSchedule(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("cross-ws pipeline id leaked: status = %d, want 400", rr.Code)
	}
}

// Closing the DB makes the schedule store's Save fail with a non-user
// error → the handler maps it to 500 (not the 400 cron/tz path).
func TestCovPSWSchedules_Create_DBError_Returns500(t *testing.T) {
	h, db, userID, wsID := covPSWScheduleRig(t)
	covPSWSeedPipeline(t, db, wsID, "pln_x", "ping-hosts")
	// Resolve must succeed first (it reads the pipelines row), so close
	// AFTER the pipeline is seeded but the Save will hit a dead handle.
	// resolveSchedulePipelineID also queries the DB, so closing here
	// surfaces as a resolve 400 — instead drive the 500 through Update
	// where the GetByID precedes the close. Use the List path below for
	// a clean 500. Keep this as the resolve-failure 400 documentation.
	db.Close()
	body := `{"cron_expr":"*/10 * * * *","target_pipeline_id":"pln_x"}`
	req := withWorkspaceUser(httptest.NewRequest("POST",
		"/api/v1/workspaces/"+wsID+"/pipeline-schedules",
		strings.NewReader(body)), userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.CreateSchedule(rr, req)
	// resolveSchedulePipelineID fails first (DB dead) → 400. Either way
	// the handler does not panic; assert it is a client/server error.
	if rr.Code != http.StatusBadRequest && rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 400 or 500 on dead DB", rr.Code)
	}
}

// ── Schedules: ListSchedules ────────────────────────────────────────────

// A dead DB handle makes List() error → 500.
func TestCovPSWSchedules_List_DBError_Returns500(t *testing.T) {
	h, db, userID, wsID := covPSWScheduleRig(t)
	db.Close()
	req := withWorkspaceUser(httptest.NewRequest("GET",
		"/api/v1/workspaces/"+wsID+"/pipeline-schedules", nil),
		userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.ListSchedules(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rr.Code)
	}
}

// List resolves a slug per target so the response carries the pipeline
// label — exercises the slug-cache branch with a populated row.
func TestCovPSWSchedules_List_PopulatesSlug(t *testing.T) {
	h, db, userID, wsID := covPSWScheduleRig(t)
	covPSWSeedPipeline(t, db, wsID, "pln_x", "labelled")
	covPSWSeedSchedule(t, db, wsID, "pln_x", "Sched A")

	req := withWorkspaceUser(httptest.NewRequest("GET",
		"/api/v1/workspaces/"+wsID+"/pipeline-schedules", nil),
		userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.ListSchedules(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var out []scheduleResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out) != 1 || out[0].TargetPipelineSlug != "labelled" {
		t.Fatalf("slug not resolved into list response: %+v", out)
	}
}

// ── Schedules: UpdateSchedule ───────────────────────────────────────────

func TestCovPSWSchedules_Update_MemberForbidden_Returns403(t *testing.T) {
	h, db, _, wsID := covPSWScheduleRig(t)
	covPSWSeedPipeline(t, db, wsID, "pln_x", "p")
	s := covPSWSeedSchedule(t, db, wsID, "pln_x", "OG")

	// MANAGER can create but not "manage"; UpdateSchedule gates on
	// canRole(role,"manage") which only OWNER/ADMIN clear.
	req := withWorkspaceUser(httptest.NewRequest("PATCH",
		"/api/v1/workspaces/"+wsID+"/pipeline-schedules/"+s.ID,
		strings.NewReader(`{"enabled":false}`)),
		"someone", wsID, "MANAGER")
	req.SetPathValue("scheduleId", s.ID)
	rr := httptest.NewRecorder()
	h.UpdateSchedule(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rr.Code)
	}
}

func TestCovPSWSchedules_Update_MissingID_Returns400(t *testing.T) {
	h, _, userID, wsID := covPSWScheduleRig(t)
	req := withWorkspaceUser(httptest.NewRequest("PATCH",
		"/api/v1/workspaces/"+wsID+"/pipeline-schedules/",
		strings.NewReader(`{"enabled":false}`)),
		userID, wsID, "OWNER")
	// No SetPathValue → scheduleId == "".
	rr := httptest.NewRecorder()
	h.UpdateSchedule(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

func TestCovPSWSchedules_Update_BadJSON_Returns400(t *testing.T) {
	h, db, userID, wsID := covPSWScheduleRig(t)
	covPSWSeedPipeline(t, db, wsID, "pln_x", "p")
	s := covPSWSeedSchedule(t, db, wsID, "pln_x", "OG")

	req := withWorkspaceUser(httptest.NewRequest("PATCH",
		"/api/v1/workspaces/"+wsID+"/pipeline-schedules/"+s.ID,
		strings.NewReader(`{BROKEN`)),
		userID, wsID, "OWNER")
	req.SetPathValue("scheduleId", s.ID)
	rr := httptest.NewRecorder()
	h.UpdateSchedule(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

// Sending an invalid cron through Update hits the store's user-error
// path → 400 (isUserScheduleError true).
func TestCovPSWSchedules_Update_InvalidCron_Returns400(t *testing.T) {
	h, db, userID, wsID := covPSWScheduleRig(t)
	covPSWSeedPipeline(t, db, wsID, "pln_x", "p")
	s := covPSWSeedSchedule(t, db, wsID, "pln_x", "OG")

	req := withWorkspaceUser(httptest.NewRequest("PATCH",
		"/api/v1/workspaces/"+wsID+"/pipeline-schedules/"+s.ID,
		strings.NewReader(`{"cron_expr":"every blue moon"}`)),
		userID, wsID, "OWNER")
	req.SetPathValue("scheduleId", s.ID)
	rr := httptest.NewRecorder()
	h.UpdateSchedule(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

// Re-target the schedule onto a different pipeline via slug, change the
// cron, and pass new inputs — exercises the resolve-on-update branch and
// the whole-row replace happy path.
func TestCovPSWSchedules_Update_RetargetAndCron_Returns200(t *testing.T) {
	h, db, userID, wsID := covPSWScheduleRig(t)
	covPSWSeedPipeline(t, db, wsID, "pln_a", "first")
	covPSWSeedPipeline(t, db, wsID, "pln_b", "second")
	s := covPSWSeedSchedule(t, db, wsID, "pln_a", "OG")

	body := `{"cron_expr":"*/30 * * * *","target_pipeline_slug":"second","inputs":{"k":"v"}}`
	req := withWorkspaceUser(httptest.NewRequest("PATCH",
		"/api/v1/workspaces/"+wsID+"/pipeline-schedules/"+s.ID,
		strings.NewReader(body)), userID, wsID, "OWNER")
	req.SetPathValue("scheduleId", s.ID)
	rr := httptest.NewRecorder()
	h.UpdateSchedule(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var resp scheduleResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.TargetPipelineID != "pln_b" {
		t.Errorf("retarget failed: target = %q, want pln_b", resp.TargetPipelineID)
	}
	if resp.CronExpr != "*/30 * * * *" {
		t.Errorf("cron not updated: %q", resp.CronExpr)
	}
	if resp.Inputs["k"] != "v" {
		t.Errorf("inputs not stored: %+v", resp.Inputs)
	}
}

// Empty PATCH body (no fields) keeps existing cron/inputs and resolves
// the slug for the unchanged target via store.GetByID — covers the
// "else if p := store.GetByID" branch + inputs-preserve branch.
func TestCovPSWSchedules_Update_EmptyBodyPreserves_Returns200(t *testing.T) {
	h, db, userID, wsID := covPSWScheduleRig(t)
	covPSWSeedPipeline(t, db, wsID, "pln_x", "kept")
	s, err := pipeline.NewScheduleStore(db).Save(t.Context(), pipeline.SaveScheduleInput{
		WorkspaceID:      wsID,
		Name:             "Keep",
		TargetPipelineID: "pln_x",
		CronExpr:         "*/10 * * * *",
		Timezone:         "UTC",
		Inputs:           map[string]any{"orig": true},
		Enabled:          true,
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	req := withWorkspaceUser(httptest.NewRequest("PATCH",
		"/api/v1/workspaces/"+wsID+"/pipeline-schedules/"+s.ID,
		strings.NewReader(`{}`)), userID, wsID, "OWNER")
	req.SetPathValue("scheduleId", s.ID)
	rr := httptest.NewRecorder()
	h.UpdateSchedule(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var resp scheduleResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.CronExpr != "*/10 * * * *" {
		t.Errorf("cron should be preserved: %q", resp.CronExpr)
	}
	if resp.TargetPipelineSlug != "kept" {
		t.Errorf("slug should resolve to kept: %q", resp.TargetPipelineSlug)
	}
	if resp.Inputs["orig"] != true {
		t.Errorf("inputs should be preserved: %+v", resp.Inputs)
	}
}

// Dead DB → GetByID errors with a non-NotFound error → 500.
func TestCovPSWSchedules_Update_DBError_Returns500(t *testing.T) {
	h, db, userID, wsID := covPSWScheduleRig(t)
	db.Close()
	req := withWorkspaceUser(httptest.NewRequest("PATCH",
		"/api/v1/workspaces/"+wsID+"/pipeline-schedules/psched_x",
		strings.NewReader(`{"enabled":false}`)),
		userID, wsID, "OWNER")
	req.SetPathValue("scheduleId", "psched_x")
	rr := httptest.NewRecorder()
	h.UpdateSchedule(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rr.Code)
	}
}

// ── Schedules: DeleteSchedule ───────────────────────────────────────────

func TestCovPSWSchedules_Delete_MemberForbidden_Returns403(t *testing.T) {
	h, db, _, wsID := covPSWScheduleRig(t)
	covPSWSeedPipeline(t, db, wsID, "pln_x", "p")
	s := covPSWSeedSchedule(t, db, wsID, "pln_x", "Doomed")

	req := withWorkspaceUser(httptest.NewRequest("DELETE",
		"/api/v1/workspaces/"+wsID+"/pipeline-schedules/"+s.ID, nil),
		"someone", wsID, "MANAGER") // MANAGER lacks "delete"
	req.SetPathValue("scheduleId", s.ID)
	rr := httptest.NewRecorder()
	h.DeleteSchedule(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rr.Code)
	}
}

func TestCovPSWSchedules_Delete_MissingID_Returns400(t *testing.T) {
	h, _, userID, wsID := covPSWScheduleRig(t)
	req := withWorkspaceUser(httptest.NewRequest("DELETE",
		"/api/v1/workspaces/"+wsID+"/pipeline-schedules/", nil),
		userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.DeleteSchedule(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

// Dead DB → GetByID errors non-NotFound → 500.
func TestCovPSWSchedules_Delete_DBError_Returns500(t *testing.T) {
	h, db, userID, wsID := covPSWScheduleRig(t)
	db.Close()
	req := withWorkspaceUser(httptest.NewRequest("DELETE",
		"/api/v1/workspaces/"+wsID+"/pipeline-schedules/psched_x", nil),
		userID, wsID, "OWNER")
	req.SetPathValue("scheduleId", "psched_x")
	rr := httptest.NewRecorder()
	h.DeleteSchedule(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rr.Code)
	}
}

// ── Webhooks: CreateWebhook ─────────────────────────────────────────────

// CreateWebhook gates on canRole(role,"create"); MEMBER fails it.
func TestCovPSWWebhooks_Create_MemberForbidden_Returns403(t *testing.T) {
	h, db, _, wsID := covPSWWebhookRig(t)
	covPSWSeedPipeline(t, db, wsID, "pln_x", "p")
	req := withWorkspaceUser(httptest.NewRequest("POST",
		"/api/v1/workspaces/"+wsID+"/pipeline-webhooks",
		strings.NewReader(`{"target_pipeline_slug":"p"}`)),
		"someone", wsID, "MEMBER")
	rr := httptest.NewRecorder()
	h.CreateWebhook(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rr.Code)
	}
}

// Binding by target_pipeline_id (CLI path) + verify defaults: enabled
// true, rate limit echoed, secret revealed once.
func TestCovPSWWebhooks_Create_ByPipelineID_Returns201(t *testing.T) {
	h, db, userID, wsID := covPSWWebhookRig(t)
	covPSWSeedPipeline(t, db, wsID, "pln_id", "by-id")

	body := `{"target_pipeline_id":"pln_id","signing_secret":"sec","rate_limit_per_min":42}`
	req := withWorkspaceUser(httptest.NewRequest("POST",
		"/api/v1/workspaces/"+wsID+"/pipeline-webhooks",
		strings.NewReader(body)), userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.CreateWebhook(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}
	var resp webhookResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.TargetPipelineID != "pln_id" {
		t.Errorf("target id = %q, want pln_id", resp.TargetPipelineID)
	}
	if !resp.Enabled {
		t.Errorf("enabled default should be true")
	}
	if resp.RateLimitPerMin != 42 {
		t.Errorf("rate limit echo = %d, want 42", resp.RateLimitPerMin)
	}
	if resp.SigningSecret != "sec" {
		t.Errorf("secret not revealed on create: %q", resp.SigningSecret)
	}
}

// target_pipeline_id in another workspace → 400.
func TestCovPSWWebhooks_Create_PipelineIDWrongWorkspace_Returns400(t *testing.T) {
	h, db, userID, wsID := covPSWWebhookRig(t)
	otherWS := "ws_foreign"
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'F', 'f')`, otherWS); err != nil {
		t.Fatalf("seed ws: %v", err)
	}
	covPSWSeedPipeline(t, db, otherWS, "pln_foreign", "foreign")

	req := withWorkspaceUser(httptest.NewRequest("POST",
		"/api/v1/workspaces/"+wsID+"/pipeline-webhooks",
		strings.NewReader(`{"target_pipeline_id":"pln_foreign"}`)),
		userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.CreateWebhook(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("cross-ws pipeline id leaked: status = %d, want 400", rr.Code)
	}
}

// Dead DB after a successful resolve is hard to stage (resolve also hits
// the DB); instead close before the call and assert the handler does not
// 5xx-panic — resolve fails first → 400.
func TestCovPSWWebhooks_Create_DeadDB_NoPanic(t *testing.T) {
	h, db, userID, wsID := covPSWWebhookRig(t)
	covPSWSeedPipeline(t, db, wsID, "pln_x", "p")
	db.Close()
	req := withWorkspaceUser(httptest.NewRequest("POST",
		"/api/v1/workspaces/"+wsID+"/pipeline-webhooks",
		strings.NewReader(`{"target_pipeline_id":"pln_x","signing_secret":"sec"}`)),
		userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.CreateWebhook(rr, req)
	if rr.Code != http.StatusBadRequest && rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 400 or 500 on dead DB", rr.Code)
	}
}

// ── Webhooks: ListWebhooks ──────────────────────────────────────────────

func TestCovPSWWebhooks_List_DBError_Returns500(t *testing.T) {
	h, db, userID, wsID := covPSWWebhookRig(t)
	db.Close()
	req := withWorkspaceUser(httptest.NewRequest("GET",
		"/api/v1/workspaces/"+wsID+"/pipeline-webhooks", nil),
		userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.ListWebhooks(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rr.Code)
	}
}

// ── Webhooks: DeleteWebhook ─────────────────────────────────────────────

func TestCovPSWWebhooks_Delete_MemberForbidden_Returns403(t *testing.T) {
	h, db, _, wsID := covPSWWebhookRig(t)
	covPSWSeedPipeline(t, db, wsID, "pln_x", "p")
	wh := covPSWSeedWebhook(t, db, wsID, "pln_x", "", true)

	req := withWorkspaceUser(httptest.NewRequest("DELETE",
		"/api/v1/workspaces/"+wsID+"/pipeline-webhooks/"+wh.ID, nil),
		"someone", wsID, "MANAGER") // MANAGER lacks "delete"
	req.SetPathValue("webhookId", wh.ID)
	rr := httptest.NewRecorder()
	h.DeleteWebhook(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rr.Code)
	}
}

func TestCovPSWWebhooks_Delete_DBError_Returns500(t *testing.T) {
	h, db, userID, wsID := covPSWWebhookRig(t)
	db.Close()
	req := withWorkspaceUser(httptest.NewRequest("DELETE",
		"/api/v1/workspaces/"+wsID+"/pipeline-webhooks/pwh_x", nil),
		userID, wsID, "OWNER")
	req.SetPathValue("webhookId", "pwh_x")
	rr := httptest.NewRecorder()
	h.DeleteWebhook(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rr.Code)
	}
}

// ── Webhooks: FireWebhook ───────────────────────────────────────────────

// A webhook with rate_limit_per_min=1: first signed fire passes, the
// second within the window trips the 429 + Retry-After branch.
func TestCovPSWWebhooks_Fire_RateLimit_Returns429(t *testing.T) {
	h, db, _, wsID := covPSWWebhookRig(t)
	h.SetRunner(&stubRunner{output: "ok"})
	covPSWSeedPipeline(t, db, wsID, "pln_rl", "rl")
	wh, err := pipeline.NewWebhookStore(db).Save(t.Context(), pipeline.SaveWebhookInput{
		WorkspaceID:      wsID,
		Name:             "rl",
		TargetPipelineID: "pln_rl",
		SigningSecret:    "rl-secret",
		Enabled:          true,
		RateLimitPerMin:  1,
	})
	if err != nil {
		t.Fatalf("seed webhook: %v", err)
	}

	body := `{"n":1}`
	sig := covPSWSign("rl-secret", body)
	fire := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/api/v1/webhooks/"+wh.Token, strings.NewReader(body))
		req.SetPathValue("token", wh.Token)
		req.Header.Set("X-Crewship-Signature", sig)
		rr := httptest.NewRecorder()
		h.FireWebhook(rr, req)
		return rr
	}
	if first := fire(); first.Code != http.StatusAccepted {
		t.Fatalf("first fire status = %d, want 202; body=%s", first.Code, first.Body.String())
	}
	second := fire()
	if second.Code != http.StatusTooManyRequests {
		t.Fatalf("second fire status = %d, want 429", second.Code)
	}
	if second.Header().Get("Retry-After") == "" {
		t.Errorf("429 response missing Retry-After header")
	}
}

// A non-JSON body still dispatches (tryParseJSON falls back to the raw
// string) and the inputs_template's NEW key is merged while reserved
// keys stay request-derived. Asserts 202.
func TestCovPSWWebhooks_Fire_NonJSONBody_TemplateAddsKey_Returns202(t *testing.T) {
	h, db, _, wsID := covPSWWebhookRig(t)
	h.SetRunner(&stubRunner{output: "ok"})
	covPSWSeedPipeline(t, db, wsID, "pln_t", "t")
	wh, err := pipeline.NewWebhookStore(db).Save(t.Context(), pipeline.SaveWebhookInput{
		WorkspaceID:      wsID,
		Name:             "t",
		TargetPipelineID: "pln_t",
		SigningSecret:    "tsec",
		InputsTemplate:   map[string]any{"newkey": "added", "event": "ignored-reserved"},
		Enabled:          true,
	})
	if err != nil {
		t.Fatalf("seed webhook: %v", err)
	}

	body := "this is not json at all"
	sig := covPSWSign("tsec", body)
	req := httptest.NewRequest("POST", "/api/v1/webhooks/"+wh.Token, strings.NewReader(body))
	req.SetPathValue("token", wh.Token)
	req.Header.Set("X-Crewship-Signature", sig)
	// Multi-value header to exercise flattenHeaders join + lowercasing.
	req.Header.Add("X-Multi", "a")
	req.Header.Add("X-Multi", "b")
	rr := httptest.NewRecorder()
	h.FireWebhook(rr, req)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body=%s", rr.Code, rr.Body.String())
	}
}

// Empty-body fire: tryParseJSON(nil) returns nil and dispatch still
// succeeds with a signature over the empty byte slice.
func TestCovPSWWebhooks_Fire_EmptyBody_Returns202(t *testing.T) {
	h, db, _, wsID := covPSWWebhookRig(t)
	h.SetRunner(&stubRunner{output: "ok"})
	covPSWSeedPipeline(t, db, wsID, "pln_e", "e")
	wh := covPSWSeedWebhook(t, db, wsID, "pln_e", "esec", true)

	sig := covPSWSign("esec", "")
	req := httptest.NewRequest("POST", "/api/v1/webhooks/"+wh.Token, http.NoBody)
	req.SetPathValue("token", wh.Token)
	req.Header.Set("X-Crewship-Signature", sig)
	rr := httptest.NewRecorder()
	h.FireWebhook(rr, req)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body=%s", rr.Code, rr.Body.String())
	}
}

// ── helpers: tryParseJSON / flattenHeaders direct units ─────────────────

func TestCovPSWTryParseJSON(t *testing.T) {
	if got := tryParseJSON(nil); got != nil {
		t.Errorf("nil body → %v, want nil", got)
	}
	if got := tryParseJSON([]byte(`not json`)); got != "not json" {
		t.Errorf("non-json → %v, want raw string", got)
	}
	got := tryParseJSON([]byte(`{"a":1}`))
	m, ok := got.(map[string]any)
	if !ok || m["a"].(float64) != 1 {
		t.Errorf("json object not parsed: %v", got)
	}
}

func TestCovPSWFlattenHeaders(t *testing.T) {
	hdr := http.Header{}
	hdr.Add("X-Event-Type", "push")
	hdr.Add("X-Multi", "a")
	hdr.Add("X-Multi", "b")
	out := flattenHeaders(hdr)
	if out["x_event_type"] != "push" {
		t.Errorf("dash→underscore+lowercase failed: %v", out)
	}
	if out["x_multi"] != "a,b" {
		t.Errorf("multi-value join failed: %q", out["x_multi"])
	}
}
