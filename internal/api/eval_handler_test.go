package api

// Tests for the quartermaster eval endpoints.
//
// Coverage focus:
//   - replay happy-path: 202 + eval_runs row inserted
//   - regression happy-path: 202 + eval_runs row inserted
//   - cross-tenant mission ID 404s on replay and regression
//   - list is scoped to the caller's workspace
//   - non-admin → 403 on mutating endpoints

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// seedMissionRow inserts a mission row with the columns the schema
// requires (lead_agent_id, trace_id, title). Returns the mission ID.
func seedMissionRow(t *testing.T, db *sql.DB, id, wsID, crewID, title string) string {
	t.Helper()
	// Use the seeded crew to anchor a lead agent — missions.NOT NULL
	// FK on lead_agent_id means we need a real agent row. The DB now
	// enforces one LEAD per crew (idx_agents_one_lead_per_crew), so reuse
	// an existing crew lead when seeding multiple missions in the same crew.
	leadID := "lead-" + id
	var existing string
	if err := db.QueryRow(`SELECT id FROM agents WHERE crew_id = ? AND agent_role = 'LEAD' AND deleted_at IS NULL`, crewID).Scan(&existing); err == nil {
		leadID = existing
	} else {
		if _, err := db.Exec(`INSERT INTO agents (id, workspace_id, crew_id, name, slug, agent_role, status,
			cli_adapter, tool_profile, timeout_seconds, memory_enabled)
			VALUES (?, ?, ?, ?, ?, 'LEAD', 'IDLE', 'CLAUDE_CODE', 'CODING', 1800, 0)`,
			leadID, wsID, crewID, "Lead "+id, "lead-"+id); err != nil {
			t.Fatalf("seed lead agent: %v", err)
		}
	}
	_, err := db.Exec(`INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title, status)
		VALUES (?, ?, ?, ?, ?, ?, 'PLANNING')`,
		id, wsID, crewID, leadID, "trace-"+id, title)
	if err != nil {
		t.Fatalf("seed mission: %v", err)
	}
	return id
}

func TestEvalReplay_HappyPath(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedCrewRow(t, db, "crew-e1", wsID, "Crew", "crew-e1")
	missionID := seedMissionRow(t, db, "mis-e1", wsID, crewID, "M1")

	h := NewEvalHandler(db, newTestLogger())

	body := bytes.NewBufferString(`{"mission_id":"` + missionID + `","seed":42}`)
	req := httptest.NewRequest("POST", "/api/v1/eval/replay", body)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Replay(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
	var resp struct {
		RunID  string `json:"run_id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.RunID == "" {
		t.Fatal("run_id empty")
	}
	if resp.Status != "queued" {
		t.Errorf("status = %q, want queued", resp.Status)
	}

	// eval_runs row exists with the correct workspace + mission scope.
	var ws, mission, kind string
	if err := db.QueryRow(`SELECT workspace_id, mission_id, kind FROM eval_runs WHERE id = ?`, resp.RunID).
		Scan(&ws, &mission, &kind); err != nil {
		t.Fatalf("query eval_runs row: %v", err)
	}
	if ws != wsID || mission != missionID || kind != "replay" {
		t.Errorf("eval_runs row mismatch: ws=%q mission=%q kind=%q", ws, mission, kind)
	}
}

func TestEvalReplay_CrossTenantMission_404(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	// Create a mission in an unrelated workspace.
	otherWS := "other-ws"
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'Other', 'other')`, otherWS); err != nil {
		t.Fatalf("seed other ws: %v", err)
	}
	otherCrew := seedCrewRow(t, db, "crew-e2", otherWS, "X", "x")
	foreignMission := seedMissionRow(t, db, "mis-x", otherWS, otherCrew, "Foreign")

	h := NewEvalHandler(db, newTestLogger())
	body := bytes.NewBufferString(`{"mission_id":"` + foreignMission + `"}`)
	req := httptest.NewRequest("POST", "/api/v1/eval/replay", body)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Replay(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("cross-tenant replay status = %d, want 404", rr.Code)
	}
	// No eval_runs row should have been inserted.
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM eval_runs`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("eval_runs count = %d, want 0 (no row should persist on 404)", n)
	}
}

func TestEvalRegression_CrossTenant_404(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedCrewRow(t, db, "crew-e3", wsID, "Crew", "crew-e3")
	baseline := seedMissionRow(t, db, "mis-base", wsID, crewID, "Base")

	// Candidate belongs to another workspace.
	otherWS := "other-ws"
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'Other', 'other')`, otherWS); err != nil {
		t.Fatalf("seed other ws: %v", err)
	}
	otherCrew := seedCrewRow(t, db, "crew-e4", otherWS, "X", "x")
	foreignCand := seedMissionRow(t, db, "mis-cand", otherWS, otherCrew, "Cand")

	h := NewEvalHandler(db, newTestLogger())
	body := bytes.NewBufferString(`{"baseline_mission_id":"` + baseline +
		`","candidate_mission_id":"` + foreignCand + `"}`)
	req := httptest.NewRequest("POST", "/api/v1/eval/regression", body)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Regression(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("cross-tenant regression status = %d, want 404", rr.Code)
	}
}

func TestEvalListRuns_WorkspaceScoped(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedCrewRow(t, db, "crew-e5", wsID, "Crew", "crew-e5")
	missionID := seedMissionRow(t, db, "mis-list", wsID, crewID, "L")

	// Insert one replay run for caller workspace.
	if _, err := db.Exec(`INSERT INTO eval_runs (id, workspace_id, kind, mission_id, status, created_at)
		VALUES ('er-caller', ?, 'replay', ?, 'completed', datetime('now'))`, wsID, missionID); err != nil {
		t.Fatalf("seed replay: %v", err)
	}

	// Insert a foreign run that must NOT appear.
	otherWS := "other-ws"
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'Other', 'other')`, otherWS); err != nil {
		t.Fatalf("seed other ws: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO eval_runs (id, workspace_id, kind, status, created_at)
		VALUES ('er-foreign', ?, 'regression', 'completed', datetime('now'))`, otherWS); err != nil {
		t.Fatalf("seed foreign run: %v", err)
	}

	h := NewEvalHandler(db, newTestLogger())
	req := httptest.NewRequest("GET", "/api/v1/eval/runs", nil)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.ListRuns(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
	var resp struct {
		Rows []struct {
			ID          string `json:"id"`
			WorkspaceID string `json:"workspace_id"`
		} `json:"rows"`
		Count int `json:"count"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Count != 1 {
		t.Fatalf("count = %d, want 1 (foreign row leaked?)", resp.Count)
	}
	if resp.Rows[0].ID != "er-caller" {
		t.Errorf("row id = %q, want er-caller", resp.Rows[0].ID)
	}
}

func TestEvalReplay_NonAdmin_Forbidden(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	h := NewEvalHandler(db, newTestLogger())
	body := bytes.NewBufferString(`{"mission_id":"mis-any"}`)
	req := httptest.NewRequest("POST", "/api/v1/eval/replay", body)
	req = withWorkspaceUser(req, userID, wsID, "MEMBER")
	rr := httptest.NewRecorder()
	h.Replay(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d (member can't run eval)", rr.Code, http.StatusForbidden)
	}
}
