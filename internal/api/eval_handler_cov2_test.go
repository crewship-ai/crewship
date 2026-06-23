package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// eval_handler_cov2_test.go — remaining Replay/Regression branches:
// mission-lookup DB error, eval_runs insert failures, the async
// worker's failed path (broken journal_entries makes Extract fail),
// and the regression completed path (empty missions compare clean).
// Async outcomes are observed by polling the eval_runs row with a
// bounded deadline. Helpers prefixed covEv2.

func covEv2Fixture(t *testing.T) (*EvalHandler, string, string, string) {
	t.Helper()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedCrewRow(t, db, "covev2-crew", wsID, "Crew", "covev2-crew")
	seedAgentRow(t, db, "covev2-lead", wsID, crewID, "Lead", "covev2-lead", "LEAD")
	h := NewEvalHandler(db, newTestLogger())
	return h, userID, wsID, crewID
}

func covEv2SeedMission(t *testing.T, h *EvalHandler, id, wsID, crewID string) {
	t.Helper()
	execOrFatal(t, h.db, `INSERT INTO missions
		(id, workspace_id, crew_id, lead_agent_id, trace_id, title, status, created_at, updated_at)
		VALUES (?, ?, ?, 'covev2-lead', ?, 'M', 'PLANNING', datetime('now'), datetime('now'))`,
		id, wsID, crewID, "covev2-tr-"+id)
}

// covEv2WaitStatus polls eval_runs until the run reaches a terminal
// status or the deadline passes; returns (status, result).
func covEv2WaitStatus(t *testing.T, h *EvalHandler, runID string) (string, string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var status, result string
		err := h.db.QueryRow(`SELECT status, COALESCE(result,'') FROM eval_runs WHERE id = ?`, runID).
			Scan(&status, &result)
		if err == nil && status != "queued" && status != "running" {
			return status, result
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("eval run %s did not reach a terminal status in time", runID)
	return "", ""
}

func covEv2RunID(t *testing.T, rr *httptest.ResponseRecorder) string {
	t.Helper()
	body := rr.Body.String()
	idx := strings.Index(body, `"run_id":"`)
	if idx < 0 {
		t.Fatalf("no run_id in body: %s", body)
	}
	rest := body[idx+len(`"run_id":"`):]
	return rest[:strings.Index(rest, `"`)]
}

func TestCovEv2_Replay_MissionLookupDBError_500(t *testing.T) {
	h, userID, wsID, _ := covEv2Fixture(t)
	h.db.Close()
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/eval/replay",
			jsonBody(map[string]any{"mission_id": "m-x"})),
		userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Replay(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovEv2_Replay_InsertFailure_500(t *testing.T) {
	h, userID, wsID, crewID := covEv2Fixture(t)
	covEv2SeedMission(t, h, "covev2-m1", wsID, crewID)
	execOrFatal(t, h.db, `CREATE TRIGGER covev2_block_ins BEFORE INSERT ON eval_runs
		BEGIN SELECT RAISE(ABORT, 'covev2 forced'); END`)
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/eval/replay",
			jsonBody(map[string]any{"mission_id": "covev2-m1"})),
		userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Replay(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

// TestCovEv2_Replay_WorkerExtractFails_RecordsFailed — journal_entries
// is renamed AFTER the insert path is set up, so the async Extract
// fails and the run lands in status=failed.
func TestCovEv2_Replay_WorkerExtractFails_RecordsFailed(t *testing.T) {
	h, userID, wsID, crewID := covEv2Fixture(t)
	covEv2SeedMission(t, h, "covev2-m2", wsID, crewID)
	execOrFatal(t, h.db, `ALTER TABLE journal_entries RENAME TO je_broken`)

	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/eval/replay",
			jsonBody(map[string]any{"mission_id": "covev2-m2", "seed": 7})),
		userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Replay(rr, req)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body=%s", rr.Code, rr.Body.String())
	}
	status, result := covEv2WaitStatus(t, h, covEv2RunID(t, rr))
	if status != "failed" {
		t.Fatalf("run status = %q (%q), want failed", status, result)
	}
}

func TestCovEv2_Regression_LookupDBError_500(t *testing.T) {
	h, userID, wsID, _ := covEv2Fixture(t)
	h.db.Close()
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/eval/regression",
			jsonBody(map[string]any{"baseline_mission_id": "a", "candidate_mission_id": "b"})),
		userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Regression(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovEv2_Regression_InsertFailure_500(t *testing.T) {
	h, userID, wsID, crewID := covEv2Fixture(t)
	covEv2SeedMission(t, h, "covev2-m3", wsID, crewID)
	covEv2SeedMission(t, h, "covev2-m4", wsID, crewID)
	execOrFatal(t, h.db, `CREATE TRIGGER covev2_block_ins2 BEFORE INSERT ON eval_runs
		BEGIN SELECT RAISE(ABORT, 'covev2 forced'); END`)
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/eval/regression",
			jsonBody(map[string]any{
				"baseline_mission_id":  "covev2-m3",
				"candidate_mission_id": "covev2-m4",
			})),
		userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Regression(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

// TestCovEv2_Regression_EmptyMissionsCompareClean — two journal-less
// missions produce identical zero metrics: completed, no_regression.
func TestCovEv2_Regression_EmptyMissionsCompareClean(t *testing.T) {
	h, userID, wsID, crewID := covEv2Fixture(t)
	covEv2SeedMission(t, h, "covev2-m5", wsID, crewID)
	covEv2SeedMission(t, h, "covev2-m6", wsID, crewID)
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/eval/regression",
			jsonBody(map[string]any{
				"baseline_mission_id":  "covev2-m5",
				"candidate_mission_id": "covev2-m6",
			})),
		userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Regression(rr, req)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body=%s", rr.Code, rr.Body.String())
	}
	status, result := covEv2WaitStatus(t, h, covEv2RunID(t, rr))
	if status != "completed" || result != "no_regression" {
		t.Fatalf("run = (%q, %q), want (completed, no_regression)", status, result)
	}
}
