package api

// Coverage tests for mission_handler_mutate.go — exercising the Create /
// Update / Delete mutation handlers' error branches and happy paths that
// the existing missions_test.go does not reach.
//
// Skipped: orchestrator/Docker callback branches are out of scope —
// these handlers do not spawn the MissionEngine (it is passed as nil),
// and the F4.5 mission-outcome lesson hook (emitMissionOutcomeLessonAsync)
// is a detached goroutine with storagePath unset, so it is a no-op that we
// only confirm does not break the synchronous response path.

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func covMMLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
}

// covMMSeedMission inserts a mission row with the given id/status anchored to
// the provided crew + lead agent. Mirrors the inline INSERTs in missions_test.go.
func covMMSeedMission(t *testing.T, db *sql.DB, id, wsID, crewID, leadID, status string) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, 'Cov Mission', ?, datetime('now'), datetime('now'))`,
		id, wsID, crewID, leadID, "trace-"+id, status)
	if err != nil {
		t.Fatalf("seed mission %s: %v", id, err)
	}
}

// ---- Create branches ----

func TestCovMMCreate_InvalidJSON(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedMissionCrew(t, db, wsID)

	handler := NewMissionHandler(db, nil, nil, covMMLogger())

	body := bytes.NewBufferString(`{not valid json`)
	req := httptest.NewRequest("POST", "/api/v1/crews/"+crewID+"/missions", body)
	req.SetPathValue("crewId", crewID)
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, wsID, "MANAGER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.Create(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body: %s", rr.Code, http.StatusBadRequest, rr.Body.String())
	}
}

func TestCovMMCreate_LeadNotInCrew(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedMissionCrew(t, db, wsID)

	handler := NewMissionHandler(db, nil, nil, covMMLogger())

	// lead_agent_id references an agent that does not exist in the crew.
	body := bytes.NewBufferString(`{"title":"X","lead_agent_id":"ghost-agent"}`)
	req := httptest.NewRequest("POST", "/api/v1/crews/"+crewID+"/missions", body)
	req.SetPathValue("crewId", crewID)
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, wsID, "MANAGER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.Create(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body: %s", rr.Code, http.StatusBadRequest, rr.Body.String())
	}
}

func TestCovMMCreate_WithDescriptionAndTemplate(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedMissionCrew(t, db, wsID)
	leadID := seedMissionAgent(t, db, wsID, crewID, "cov-lead", "LEAD")

	handler := NewMissionHandler(db, nil, nil, covMMLogger())

	body := bytes.NewBufferString(`{"title":"Cov Build","description":"desc","workflow_template":"plan-execute","lead_agent_id":"` + leadID + `"}`)
	req := httptest.NewRequest("POST", "/api/v1/crews/"+crewID+"/missions", body)
	req.SetPathValue("crewId", crewID)
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, wsID, "MANAGER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.Create(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d; body: %s", rr.Code, http.StatusCreated, rr.Body.String())
	}

	var result map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if result["description"] != "desc" {
		t.Errorf("description = %v, want 'desc'", result["description"])
	}
	if result["workflow_template"] != "plan-execute" {
		t.Errorf("workflow_template = %v, want 'plan-execute'", result["workflow_template"])
	}

	// Confirm the mission row AND the synthetic chat (FK target) both persisted.
	missionID, _ := result["id"].(string)
	var chatCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM chats WHERE id = ?", missionID).Scan(&chatCount); err != nil {
		t.Fatalf("query chat: %v", err)
	}
	if chatCount != 1 {
		t.Errorf("synthetic chat count = %d, want 1", chatCount)
	}
}

// ---- Update branches ----

func TestCovMMUpdate_Forbidden(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedMissionCrew(t, db, wsID)
	leadID := seedMissionAgent(t, db, wsID, crewID, "cov-lead", "LEAD")
	covMMSeedMission(t, db, "cm-fb", wsID, crewID, leadID, "PLANNING")

	handler := NewMissionHandler(db, nil, nil, covMMLogger())

	body := bytes.NewBufferString(`{"status":"IN_PROGRESS"}`)
	req := httptest.NewRequest("PATCH", "/api/v1/crews/"+crewID+"/missions/cm-fb", body)
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("missionId", "cm-fb")
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, wsID, "VIEWER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.Update(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusForbidden)
	}
}

func TestCovMMUpdate_InvalidJSON(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedMissionCrew(t, db, wsID)
	leadID := seedMissionAgent(t, db, wsID, crewID, "cov-lead", "LEAD")
	covMMSeedMission(t, db, "cm-bad", wsID, crewID, leadID, "PLANNING")

	handler := NewMissionHandler(db, nil, nil, covMMLogger())

	body := bytes.NewBufferString(`{bad`)
	req := httptest.NewRequest("PATCH", "/api/v1/crews/"+crewID+"/missions/cm-bad", body)
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("missionId", "cm-bad")
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, wsID, "MANAGER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.Update(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusBadRequest)
	}
}

func TestCovMMUpdate_NotFound(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedMissionCrew(t, db, wsID)

	handler := NewMissionHandler(db, nil, nil, covMMLogger())

	body := bytes.NewBufferString(`{"status":"IN_PROGRESS"}`)
	req := httptest.NewRequest("PATCH", "/api/v1/crews/"+crewID+"/missions/ghost", body)
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("missionId", "ghost")
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, wsID, "MANAGER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.Update(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusNotFound)
	}
}

func TestCovMMUpdate_FieldsOnly(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedMissionCrew(t, db, wsID)
	leadID := seedMissionAgent(t, db, wsID, crewID, "cov-lead", "LEAD")
	covMMSeedMission(t, db, "cm-fields", wsID, crewID, leadID, "PLANNING")

	handler := NewMissionHandler(db, nil, nil, covMMLogger())

	// No status change — exercises the title/description/plan UPDATE branches.
	body := bytes.NewBufferString(`{"title":"New Title","description":"New Desc","plan":"step 1\nstep 2"}`)
	req := httptest.NewRequest("PATCH", "/api/v1/crews/"+crewID+"/missions/cm-fields", body)
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("missionId", "cm-fields")
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, wsID, "MANAGER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.Update(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", rr.Code, http.StatusOK, rr.Body.String())
	}

	var title, desc, plan, status string
	err := db.QueryRow("SELECT title, description, plan, status FROM missions WHERE id = 'cm-fields'").
		Scan(&title, &desc, &plan, &status)
	if err != nil {
		t.Fatalf("query mission: %v", err)
	}
	if title != "New Title" {
		t.Errorf("title = %q, want 'New Title'", title)
	}
	if desc != "New Desc" {
		t.Errorf("description = %q, want 'New Desc'", desc)
	}
	if plan != "step 1\nstep 2" {
		t.Errorf("plan = %q, want updated", plan)
	}
	// Status untouched (no status in body).
	if status != "PLANNING" {
		t.Errorf("status = %q, want PLANNING (unchanged)", status)
	}
}

func TestCovMMUpdate_TerminalTransitionSetsCompletedAt(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedMissionCrew(t, db, wsID)
	leadID := seedMissionAgent(t, db, wsID, crewID, "cov-lead", "LEAD")
	// REVIEW → COMPLETED is a valid terminal transition; storagePath is unset
	// so the F4.5 lesson goroutine is a no-op (skipped branch noted at top).
	covMMSeedMission(t, db, "cm-done", wsID, crewID, leadID, "REVIEW")

	handler := NewMissionHandler(db, nil, nil, covMMLogger())

	body := bytes.NewBufferString(`{"status":"COMPLETED"}`)
	req := httptest.NewRequest("PATCH", "/api/v1/crews/"+crewID+"/missions/cm-done", body)
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("missionId", "cm-done")
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, wsID, "MANAGER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.Update(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", rr.Code, http.StatusOK, rr.Body.String())
	}

	var status string
	var completedAt sql.NullString
	if err := db.QueryRow("SELECT status, completed_at FROM missions WHERE id = 'cm-done'").
		Scan(&status, &completedAt); err != nil {
		t.Fatalf("query mission: %v", err)
	}
	if status != "COMPLETED" {
		t.Errorf("status = %q, want COMPLETED", status)
	}
	if !completedAt.Valid || completedAt.String == "" {
		t.Errorf("completed_at = %v, want a timestamp on terminal transition", completedAt)
	}
}

// ---- Delete branches ----

func TestCovMMDelete_Forbidden(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedMissionCrew(t, db, wsID)
	leadID := seedMissionAgent(t, db, wsID, crewID, "cov-lead", "LEAD")
	covMMSeedMission(t, db, "cm-delfb", wsID, crewID, leadID, "PLANNING")

	handler := NewMissionHandler(db, nil, nil, covMMLogger())

	req := httptest.NewRequest("DELETE", "/api/v1/crews/"+crewID+"/missions/cm-delfb", nil)
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("missionId", "cm-delfb")
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, wsID, "VIEWER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.Delete(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusForbidden)
	}
}

func TestCovMMDelete_NotFound(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedMissionCrew(t, db, wsID)

	handler := NewMissionHandler(db, nil, nil, covMMLogger())

	req := httptest.NewRequest("DELETE", "/api/v1/crews/"+crewID+"/missions/ghost", nil)
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("missionId", "ghost")
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, wsID, "MANAGER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.Delete(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusNotFound)
	}
}

func TestCovMMDelete_WrongStatus(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedMissionCrew(t, db, wsID)
	leadID := seedMissionAgent(t, db, wsID, crewID, "cov-lead", "LEAD")
	// IN_PROGRESS is not deletable — only PLANNING / CANCELLED are.
	covMMSeedMission(t, db, "cm-running", wsID, crewID, leadID, "IN_PROGRESS")

	handler := NewMissionHandler(db, nil, nil, covMMLogger())

	req := httptest.NewRequest("DELETE", "/api/v1/crews/"+crewID+"/missions/cm-running", nil)
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("missionId", "cm-running")
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, wsID, "MANAGER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.Delete(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body: %s", rr.Code, http.StatusBadRequest, rr.Body.String())
	}

	// Row must survive a rejected delete.
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM missions WHERE id = 'cm-running'").Scan(&count); err != nil {
		t.Fatalf("query count: %v", err)
	}
	if count != 1 {
		t.Errorf("mission count = %d, want 1 (delete should be rejected)", count)
	}
}

func TestCovMMDelete_CancelledDeletable(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedMissionCrew(t, db, wsID)
	leadID := seedMissionAgent(t, db, wsID, crewID, "cov-lead", "LEAD")
	covMMSeedMission(t, db, "cm-cancelled", wsID, crewID, leadID, "CANCELLED")

	handler := NewMissionHandler(db, nil, nil, covMMLogger())

	req := httptest.NewRequest("DELETE", "/api/v1/crews/"+crewID+"/missions/cm-cancelled", nil)
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("missionId", "cm-cancelled")
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, wsID, "MANAGER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.Delete(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d; body: %s", rr.Code, http.StatusNoContent, rr.Body.String())
	}

	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM missions WHERE id = 'cm-cancelled'").Scan(&count); err != nil {
		t.Fatalf("query count: %v", err)
	}
	if count != 0 {
		t.Errorf("mission count = %d, want 0 (CANCELLED is deletable)", count)
	}
}
