package api

import (
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

// scheduleHandlerRig wires a PipelineHandler against the full-migrated
// test DB (setupTestDB) so the production pipeline_schedules schema
// runs, not the truncated one openSmokeDB uses. Also wires
// SetScheduleStore so endpoints don't bail with 503.
func scheduleHandlerRig(t *testing.T) (*PipelineHandler, *sql.DB, string, string) {
	t.Helper()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	h := NewPipelineHandler(db, logger, nil, nil)
	h.SetScheduleStore(pipeline.NewScheduleStore(db))
	return h, db, userID, wsID
}

// seedPipelineRow inserts a minimal pipelines row so a schedule can
// bind to it. The schedule API only consults id/slug/workspace_id —
// definition_json can be a stub, definition_hash a placeholder.
func seedPipelineRow(t *testing.T, db *sql.DB, wsID, id, slug string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := db.Exec(`
		INSERT INTO pipelines (id, workspace_id, slug, name, definition_json, definition_hash, created_at, updated_at, last_test_run_at)
		VALUES (?, ?, ?, ?, '{"name":"x","steps":[]}', 'hash', ?, ?, ?)`,
		id, wsID, slug, slug, now, now, now); err != nil {
		t.Fatalf("seed pipeline: %v", err)
	}
}

// ── CreateSchedule ──────────────────────────────────────────────────────

func TestPipelineSchedules_Create_NoBackend_Returns503(t *testing.T) {
	// Construct a handler without wiring SetScheduleStore — the endpoint
	// must announce its dependency-missing state via 503 instead of
	// nil-deref'ing.
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	h := NewPipelineHandler(db, logger, nil, nil) // no SetScheduleStore

	req := withWorkspaceUser(httptest.NewRequest("POST",
		"/api/v1/workspaces/"+wsID+"/pipeline-schedules",
		strings.NewReader(`{"cron_expr":"*/5 * * * *","target_pipeline_slug":"x"}`)),
		userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.CreateSchedule(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rr.Code)
	}
}

func TestPipelineSchedules_Create_BadJSON_Returns400(t *testing.T) {
	h, _, userID, wsID := scheduleHandlerRig(t)
	req := withWorkspaceUser(httptest.NewRequest("POST",
		"/api/v1/workspaces/"+wsID+"/pipeline-schedules",
		strings.NewReader(`{NOT_JSON`)),
		userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.CreateSchedule(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

func TestPipelineSchedules_Create_MissingCron_Returns400(t *testing.T) {
	h, _, userID, wsID := scheduleHandlerRig(t)
	req := withWorkspaceUser(httptest.NewRequest("POST",
		"/api/v1/workspaces/"+wsID+"/pipeline-schedules",
		strings.NewReader(`{"target_pipeline_slug":"x"}`)),
		userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.CreateSchedule(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

func TestPipelineSchedules_Create_UnknownPipelineSlug_Returns400(t *testing.T) {
	h, _, userID, wsID := scheduleHandlerRig(t)
	req := withWorkspaceUser(httptest.NewRequest("POST",
		"/api/v1/workspaces/"+wsID+"/pipeline-schedules",
		strings.NewReader(`{"cron_expr":"*/5 * * * *","target_pipeline_slug":"does-not-exist"}`)),
		userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.CreateSchedule(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

func TestPipelineSchedules_Create_InvalidCron_Returns400(t *testing.T) {
	h, db, userID, wsID := scheduleHandlerRig(t)
	seedPipelineRow(t, db, wsID, "pln_x", "ping-hosts")

	// "every 5 seconds" is not a valid cron; the store flags it with the
	// "invalid cron expression" prefix that isUserScheduleError catches.
	req := withWorkspaceUser(httptest.NewRequest("POST",
		"/api/v1/workspaces/"+wsID+"/pipeline-schedules",
		strings.NewReader(`{"cron_expr":"every 5 seconds","target_pipeline_slug":"ping-hosts"}`)),
		userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.CreateSchedule(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

func TestPipelineSchedules_Create_HappyPath_Returns201(t *testing.T) {
	h, db, userID, wsID := scheduleHandlerRig(t)
	seedPipelineRow(t, db, wsID, "pln_x", "ping-hosts")

	body := `{
		"cron_expr": "*/10 * * * *",
		"target_pipeline_slug": "ping-hosts",
		"timezone": "UTC",
		"inputs": {"hosts": ["1.1.1.1","8.8.8.8"]}
	}`
	req := withWorkspaceUser(httptest.NewRequest("POST",
		"/api/v1/workspaces/"+wsID+"/pipeline-schedules",
		strings.NewReader(body)),
		userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.CreateSchedule(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}

	var resp scheduleResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.WorkspaceID != wsID {
		t.Errorf("workspace echo = %q, want %q", resp.WorkspaceID, wsID)
	}
	if resp.TargetPipelineSlug != "ping-hosts" {
		t.Errorf("slug echo = %q, want ping-hosts", resp.TargetPipelineSlug)
	}
	if resp.CronExpr != "*/10 * * * *" {
		t.Errorf("cron echo = %q", resp.CronExpr)
	}
	if !resp.Enabled {
		t.Errorf("enabled default should be true")
	}
}

// ── ListSchedules ───────────────────────────────────────────────────────

func TestPipelineSchedules_List_NoBackend_Returns503(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	h := NewPipelineHandler(db, logger, nil, nil) // no SetScheduleStore

	req := withWorkspaceUser(httptest.NewRequest("GET",
		"/api/v1/workspaces/"+wsID+"/pipeline-schedules", nil),
		userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.ListSchedules(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rr.Code)
	}
}

func TestPipelineSchedules_List_Empty_Returns200WithEmptyArray(t *testing.T) {
	h, _, userID, wsID := scheduleHandlerRig(t)
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
	if len(out) != 0 {
		t.Errorf("len(out) = %d, want 0", len(out))
	}
	// Returning a JSON literal "null" instead of "[]" would break
	// every UI list-rendering hook. Make sure the encoder picked the
	// array form by inspecting the raw bytes.
	body := strings.TrimSpace(rr.Body.String())
	if body == "null" {
		t.Errorf("empty schedule list serialised as null — UI expects []")
	}
}

func TestPipelineSchedules_List_HidesOtherWorkspaces(t *testing.T) {
	h, db, userID, wsID := scheduleHandlerRig(t)
	seedPipelineRow(t, db, wsID, "pln_a", "ours")

	// Drop a foreign workspace + its own schedule directly.
	otherWS := "ws_other"
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'Other', 'other')`, otherWS); err != nil {
		t.Fatalf("seed other ws: %v", err)
	}
	seedPipelineRow(t, db, otherWS, "pln_b", "theirs")
	if _, err := pipeline.NewScheduleStore(db).Save(t.Context(), pipeline.SaveScheduleInput{
		WorkspaceID:      otherWS,
		Name:             "Theirs",
		TargetPipelineID: "pln_b",
		CronExpr:         "*/15 * * * *",
		Timezone:         "UTC",
		Enabled:          true,
	}); err != nil {
		t.Fatalf("seed foreign schedule: %v", err)
	}
	// And one for our workspace.
	if _, err := pipeline.NewScheduleStore(db).Save(t.Context(), pipeline.SaveScheduleInput{
		WorkspaceID:      wsID,
		Name:             "Ours",
		TargetPipelineID: "pln_a",
		CronExpr:         "*/10 * * * *",
		Timezone:         "UTC",
		Enabled:          true,
	}); err != nil {
		t.Fatalf("seed own schedule: %v", err)
	}

	req := withWorkspaceUser(httptest.NewRequest("GET",
		"/api/v1/workspaces/"+wsID+"/pipeline-schedules", nil),
		userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.ListSchedules(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var out []scheduleResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &out)
	if len(out) != 1 {
		t.Fatalf("len = %d, want exactly 1 (own ws only); got=%v", len(out), out)
	}
	if out[0].Name != "Ours" {
		t.Errorf("tenant leak: got %q, want Ours", out[0].Name)
	}
}

// ── UpdateSchedule ──────────────────────────────────────────────────────

func TestPipelineSchedules_Update_UnknownID_Returns404(t *testing.T) {
	h, _, userID, wsID := scheduleHandlerRig(t)
	req := withWorkspaceUser(httptest.NewRequest("PATCH",
		"/api/v1/workspaces/"+wsID+"/pipeline-schedules/psched_nope",
		strings.NewReader(`{"cron_expr":"*/30 * * * *"}`)),
		userID, wsID, "OWNER")
	req.SetPathValue("scheduleId", "psched_nope")
	rr := httptest.NewRecorder()
	h.UpdateSchedule(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rr.Code)
	}
}

func TestPipelineSchedules_Update_ToggleEnabled(t *testing.T) {
	h, db, userID, wsID := scheduleHandlerRig(t)
	seedPipelineRow(t, db, wsID, "pln_x", "ping-hosts")
	saved, err := pipeline.NewScheduleStore(db).Save(t.Context(), pipeline.SaveScheduleInput{
		WorkspaceID:      wsID,
		Name:             "OG",
		TargetPipelineID: "pln_x",
		CronExpr:         "*/10 * * * *",
		Timezone:         "UTC",
		Enabled:          true,
	})
	if err != nil {
		t.Fatalf("seed schedule: %v", err)
	}

	// Disable it. enabled is *bool so omitting it should preserve the
	// existing value; passing false should flip it.
	req := withWorkspaceUser(httptest.NewRequest("PATCH",
		"/api/v1/workspaces/"+wsID+"/pipeline-schedules/"+saved.ID,
		strings.NewReader(`{"enabled":false}`)),
		userID, wsID, "OWNER")
	req.SetPathValue("scheduleId", saved.ID)
	rr := httptest.NewRecorder()
	h.UpdateSchedule(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var resp scheduleResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp.Enabled {
		t.Errorf("Enabled = true, want false after disable")
	}
}

func TestPipelineSchedules_Update_OtherWorkspaceSchedule_Returns404(t *testing.T) {
	h, db, userID, wsID := scheduleHandlerRig(t)
	otherWS := "ws_other"
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'Other', 'other')`, otherWS); err != nil {
		t.Fatalf("seed other ws: %v", err)
	}
	seedPipelineRow(t, db, otherWS, "pln_b", "theirs")
	saved, err := pipeline.NewScheduleStore(db).Save(t.Context(), pipeline.SaveScheduleInput{
		WorkspaceID:      otherWS,
		Name:             "Theirs",
		TargetPipelineID: "pln_b",
		CronExpr:         "*/15 * * * *",
		Timezone:         "UTC",
		Enabled:          true,
	})
	if err != nil {
		t.Fatalf("seed foreign schedule: %v", err)
	}

	req := withWorkspaceUser(httptest.NewRequest("PATCH",
		"/api/v1/workspaces/"+wsID+"/pipeline-schedules/"+saved.ID,
		strings.NewReader(`{"enabled":false}`)),
		userID, wsID, "OWNER")
	req.SetPathValue("scheduleId", saved.ID)
	rr := httptest.NewRecorder()
	h.UpdateSchedule(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("cross-workspace UPDATE leaked: status = %d, want 404", rr.Code)
	}
}

// ── DeleteSchedule ──────────────────────────────────────────────────────

func TestPipelineSchedules_Delete_UnknownID_Returns404(t *testing.T) {
	h, _, userID, wsID := scheduleHandlerRig(t)
	req := withWorkspaceUser(httptest.NewRequest("DELETE",
		"/api/v1/workspaces/"+wsID+"/pipeline-schedules/psched_nope", nil),
		userID, wsID, "OWNER")
	req.SetPathValue("scheduleId", "psched_nope")
	rr := httptest.NewRecorder()
	h.DeleteSchedule(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rr.Code)
	}
}

func TestPipelineSchedules_Delete_HappyPath_Returns204(t *testing.T) {
	h, db, userID, wsID := scheduleHandlerRig(t)
	seedPipelineRow(t, db, wsID, "pln_x", "ping-hosts")
	saved, err := pipeline.NewScheduleStore(db).Save(t.Context(), pipeline.SaveScheduleInput{
		WorkspaceID:      wsID,
		Name:             "Doomed",
		TargetPipelineID: "pln_x",
		CronExpr:         "*/10 * * * *",
		Timezone:         "UTC",
		Enabled:          true,
	})
	if err != nil {
		t.Fatalf("seed schedule: %v", err)
	}

	req := withWorkspaceUser(httptest.NewRequest("DELETE",
		"/api/v1/workspaces/"+wsID+"/pipeline-schedules/"+saved.ID, nil),
		userID, wsID, "OWNER")
	req.SetPathValue("scheduleId", saved.ID)
	rr := httptest.NewRecorder()
	h.DeleteSchedule(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body=%s", rr.Code, rr.Body.String())
	}

	// Verify soft-delete: row stays in the table but the next ListSchedules
	// should hide it.
	listReq := withWorkspaceUser(httptest.NewRequest("GET",
		"/api/v1/workspaces/"+wsID+"/pipeline-schedules", nil),
		userID, wsID, "OWNER")
	listRR := httptest.NewRecorder()
	h.ListSchedules(listRR, listReq)
	var out []scheduleResponse
	_ = json.Unmarshal(listRR.Body.Bytes(), &out)
	for _, s := range out {
		if s.ID == saved.ID {
			t.Errorf("soft-deleted schedule %s still surfaces in List", saved.ID)
		}
	}
}

func TestPipelineSchedules_Delete_OtherWorkspaceSchedule_Returns404(t *testing.T) {
	h, db, userID, wsID := scheduleHandlerRig(t)
	otherWS := "ws_other"
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'Other', 'other')`, otherWS); err != nil {
		t.Fatalf("seed other ws: %v", err)
	}
	seedPipelineRow(t, db, otherWS, "pln_b", "theirs")
	saved, err := pipeline.NewScheduleStore(db).Save(t.Context(), pipeline.SaveScheduleInput{
		WorkspaceID:      otherWS,
		Name:             "Theirs",
		TargetPipelineID: "pln_b",
		CronExpr:         "*/15 * * * *",
		Timezone:         "UTC",
		Enabled:          true,
	})
	if err != nil {
		t.Fatalf("seed foreign: %v", err)
	}

	req := withWorkspaceUser(httptest.NewRequest("DELETE",
		"/api/v1/workspaces/"+wsID+"/pipeline-schedules/"+saved.ID, nil),
		userID, wsID, "OWNER")
	req.SetPathValue("scheduleId", saved.ID)
	rr := httptest.NewRecorder()
	h.DeleteSchedule(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("cross-workspace DELETE leaked: status = %d, want 404", rr.Code)
	}
}
