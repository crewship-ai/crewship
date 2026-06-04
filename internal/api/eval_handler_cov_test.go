package api

// Additional coverage for the quartermaster eval endpoints. The existing
// eval_handler_test.go covers replay happy-path, cross-tenant 404s, list
// scoping, and the replay 403. This file fills the remaining branches that
// don't require a live eval-runner / LLM / network:
//
//   - missing-workspace 401 on Replay / Regression / ListRuns
//   - invalid-JSON 400 and empty-field 400 on Replay / Regression
//   - Regression non-admin 403 and happy-path 202 (+ row insert)
//   - ListRuns limit clamping (valid applied, out-of-range ignored)
//   - SetJournal nil → noopEmitter, non-nil → stored
//   - safeStr / newEvalToken units
//
// SKIPPED: the success/failure of the background goroutine (quartermaster
// Replay / DetectRegression) and the updateRun terminal writes — those need
// a real journal-backed mission extraction. We only assert the synchronous
// 202 + inserted "queued"/"pending" row, matching the existing happy-path.

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/journal"
)

// covEvalDecodeResp pulls run_id/status out of a 202 body.
func covEvalDecodeResp(t *testing.T, rr *httptest.ResponseRecorder) (string, string) {
	t.Helper()
	var resp struct {
		RunID  string `json:"run_id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode resp: %v (body=%s)", err, rr.Body.String())
	}
	return resp.RunID, resp.Status
}

// ---------------------------------------------------------------------------
// Replay — auth / validation branches
// ---------------------------------------------------------------------------

func TestCovEvalReplay_MissingWorkspace_401(t *testing.T) {
	db := setupTestDB(t)
	h := NewEvalHandler(db, newTestLogger())

	body := bytes.NewBufferString(`{"mission_id":"mis-any"}`)
	req := httptest.NewRequest("POST", "/api/v1/eval/replay", body)
	// No workspace/user context attached.
	rr := httptest.NewRecorder()
	h.Replay(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

func TestCovEvalReplay_InvalidJSON_400(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewEvalHandler(db, newTestLogger())

	body := bytes.NewBufferString(`{not json`)
	req := httptest.NewRequest("POST", "/api/v1/eval/replay", body)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Replay(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusBadRequest)
	}
}

func TestCovEvalReplay_EmptyMissionID_400(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewEvalHandler(db, newTestLogger())

	body := bytes.NewBufferString(`{"mission_id":"","seed":1}`)
	req := httptest.NewRequest("POST", "/api/v1/eval/replay", body)
	req = withWorkspaceUser(req, userID, wsID, "ADMIN")
	rr := httptest.NewRecorder()
	h.Replay(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusBadRequest)
	}
	// No row should have been inserted.
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM eval_runs`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("eval_runs count = %d, want 0", n)
	}
}

// Replay happy-path under ADMIN (existing test uses OWNER) exercises the
// second arm of the role check and confirms created_by is recorded.
func TestCovEvalReplay_AdminHappyPath_RecordsCreatedBy(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedCrewRow(t, db, "crew-cov-r1", wsID, "Crew", "crew-cov-r1")
	missionID := seedMissionRow(t, db, "mis-cov-r1", wsID, crewID, "M")

	h := NewEvalHandler(db, newTestLogger())
	body := bytes.NewBufferString(`{"mission_id":"` + missionID + `","seed":7}`)
	req := httptest.NewRequest("POST", "/api/v1/eval/replay", body)
	req = withWorkspaceUser(req, userID, wsID, "ADMIN")
	rr := httptest.NewRecorder()
	h.Replay(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
	runID, status := covEvalDecodeResp(t, rr)
	if runID == "" || status != "queued" {
		t.Fatalf("run_id=%q status=%q", runID, status)
	}

	var createdBy string
	if err := db.QueryRow(`SELECT created_by FROM eval_runs WHERE id = ?`, runID).Scan(&createdBy); err != nil {
		t.Fatalf("query created_by: %v", err)
	}
	if createdBy != userID {
		t.Errorf("created_by = %q, want %q", createdBy, userID)
	}
}

// ---------------------------------------------------------------------------
// Regression — auth / validation branches
// ---------------------------------------------------------------------------

func TestCovEvalRegression_MissingWorkspace_401(t *testing.T) {
	db := setupTestDB(t)
	h := NewEvalHandler(db, newTestLogger())

	body := bytes.NewBufferString(`{"baseline_mission_id":"a","candidate_mission_id":"b"}`)
	req := httptest.NewRequest("POST", "/api/v1/eval/regression", body)
	rr := httptest.NewRecorder()
	h.Regression(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

func TestCovEvalRegression_NonAdmin_403(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewEvalHandler(db, newTestLogger())

	body := bytes.NewBufferString(`{"baseline_mission_id":"a","candidate_mission_id":"b"}`)
	req := httptest.NewRequest("POST", "/api/v1/eval/regression", body)
	req = withWorkspaceUser(req, userID, wsID, "MEMBER")
	rr := httptest.NewRecorder()
	h.Regression(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusForbidden)
	}
}

func TestCovEvalRegression_InvalidJSON_400(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewEvalHandler(db, newTestLogger())

	body := bytes.NewBufferString(`}{`)
	req := httptest.NewRequest("POST", "/api/v1/eval/regression", body)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Regression(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusBadRequest)
	}
}

func TestCovEvalRegression_MissingMissionIDs_400(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewEvalHandler(db, newTestLogger())

	// Baseline present, candidate empty → still 400 (both required).
	body := bytes.NewBufferString(`{"baseline_mission_id":"a","candidate_mission_id":""}`)
	req := httptest.NewRequest("POST", "/api/v1/eval/regression", body)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Regression(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusBadRequest)
	}
}

// Regression happy-path: both missions in the caller's workspace → 202 with
// a "regression"-kind row carrying both mission IDs.
func TestCovEvalRegression_HappyPath_InsertsRow(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedCrewRow(t, db, "crew-cov-reg", wsID, "Crew", "crew-cov-reg")
	baseline := seedMissionRow(t, db, "mis-cov-base", wsID, crewID, "Base")
	candidate := seedMissionRow(t, db, "mis-cov-cand", wsID, crewID, "Cand")

	h := NewEvalHandler(db, newTestLogger())
	body := bytes.NewBufferString(`{"baseline_mission_id":"` + baseline +
		`","candidate_mission_id":"` + candidate + `"}`)
	req := httptest.NewRequest("POST", "/api/v1/eval/regression", body)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Regression(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
	runID, status := covEvalDecodeResp(t, rr)
	if runID == "" || status != "queued" {
		t.Fatalf("run_id=%q status=%q", runID, status)
	}

	var ws, kind, base, cand string
	if err := db.QueryRow(
		`SELECT workspace_id, kind, baseline_mission_id, candidate_mission_id FROM eval_runs WHERE id = ?`,
		runID).Scan(&ws, &kind, &base, &cand); err != nil {
		t.Fatalf("query eval_runs: %v", err)
	}
	if ws != wsID || kind != "regression" || base != baseline || cand != candidate {
		t.Errorf("row mismatch: ws=%q kind=%q base=%q cand=%q", ws, kind, base, cand)
	}
}

// ---------------------------------------------------------------------------
// ListRuns — auth + limit branches
// ---------------------------------------------------------------------------

func TestCovEvalListRuns_MissingWorkspace_401(t *testing.T) {
	db := setupTestDB(t)
	h := NewEvalHandler(db, newTestLogger())

	req := httptest.NewRequest("GET", "/api/v1/eval/runs", nil)
	rr := httptest.NewRecorder()
	h.ListRuns(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

func TestCovEvalListRuns_LimitClamping(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewEvalHandler(db, newTestLogger())

	cases := []struct {
		name      string
		query     string
		wantLimit int
	}{
		{"valid-in-range", "?limit=10", 10},
		{"upper-bound", "?limit=200", 200},
		{"over-max-ignored", "?limit=999", 50},
		{"zero-ignored", "?limit=0", 50},
		{"negative-ignored", "?limit=-5", 50},
		{"non-numeric-ignored", "?limit=abc", 50},
		{"absent-default", "", 50},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/api/v1/eval/runs"+tc.query, nil)
			req = withWorkspaceUser(req, userID, wsID, "OWNER")
			rr := httptest.NewRecorder()
			h.ListRuns(rr, req)

			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
			}
			var resp struct {
				Limit int `json:"limit"`
			}
			if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if resp.Limit != tc.wantLimit {
				t.Errorf("limit = %d, want %d", resp.Limit, tc.wantLimit)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// SetJournal + unit helpers
// ---------------------------------------------------------------------------

func TestCovEvalSetJournal_NilFallsBackToNoop(t *testing.T) {
	db := setupTestDB(t)
	h := NewEvalHandler(db, newTestLogger())

	// nil collapses to noopEmitter, not a nil interface that would panic
	// when quartermaster calls Emit.
	h.SetJournal(nil)
	if h.journal == nil {
		t.Fatal("journal is nil after SetJournal(nil); want noopEmitter")
	}
	// noopEmitter only errors on "run.*" lifecycle types; a plain entry
	// must be swallowed without error.
	if _, err := h.journal.Emit(context.Background(), journal.Entry{Type: "eval.cov"}); err != nil {
		t.Errorf("noop Emit returned err: %v", err)
	}
}

func TestCovEvalSetJournal_NonNilStored(t *testing.T) {
	db := setupTestDB(t)
	h := NewEvalHandler(db, newTestLogger())

	em := noopEmitter{}
	h.SetJournal(em)
	if h.journal == nil {
		t.Fatal("journal nil after SetJournal(noopEmitter{})")
	}
}

func TestCovEvalSafeStr(t *testing.T) {
	if got := safeStr(nil); got != "" {
		t.Errorf("safeStr(nil) = %q, want empty", got)
	}
	if got := safeStr(errors.New("boom")); got != "boom" {
		t.Errorf("safeStr(err) = %q, want boom", got)
	}
}

func TestCovEvalNewEvalToken(t *testing.T) {
	tok, err := newEvalToken()
	if err != nil {
		t.Fatalf("newEvalToken err: %v", err)
	}
	// 8 random bytes hex-encoded → 16 hex chars that decode cleanly.
	if len(tok) != 16 {
		t.Fatalf("token len = %d, want 16", len(tok))
	}
	if _, err := hex.DecodeString(tok); err != nil {
		t.Errorf("token not valid hex: %v", err)
	}
	// Two draws should differ (entropy, not a constant).
	tok2, _ := newEvalToken()
	if tok == tok2 {
		t.Errorf("two tokens identical: %q", tok)
	}
}
