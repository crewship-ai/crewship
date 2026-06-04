package api

// Additional branch-coverage tests for admin_gdpr.go, task_handler.go and
// consolidate_handler.go. These complement admin_gdpr_test.go,
// task_handler_test.go and consolidate_handler_test.go by hitting the
// auth/validation/404/happy/500 branches those files leave uncovered.
//
// Conventions (per the worktree task brief):
//   - package api; reuse existing helpers (setupTestDB, seedTestUser,
//     seedTestWorkspace, newTestLogger, gdprTestSetup, stubSummarizer,
//     newMissionHandlerForTasks, withWorkspaceUser, withUser, withWorkspace).
//   - new helpers prefixed covGTC; all test funcs prefixed TestCovGTC.
//
// Skipped (require LLM/Docker or live consolidation work): the actual
// rule-appending body of consolidate.Consolidator.Run (needs a summarizer
// LLM + journal entries to consolidate) and the on-disk peer-card delete
// path in DeleteUserData (needs a populated memory dir on a real FS layout).

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/consolidate"
	"github.com/crewship-ai/crewship/internal/journal"
)

// ---------------------------------------------------------------------------
// admin_gdpr.go
// ---------------------------------------------------------------------------

// covGTCNoUserReq builds a request with NO user / workspace / role context so
// adminContext's early-return branches can be exercised individually.
func covGTCNoUserReq(t *testing.T, body string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodDelete, "/", bytes.NewBufferString(body))
	req.SetPathValue("userId", "subj1")
	return req
}

func TestCovGTC_GDPRDelete_Unauthenticated(t *testing.T) {
	r := gdprTestSetup(t)
	rec := httptest.NewRecorder()
	// No context values at all → 401.
	r.h.DeleteUserData(rec, covGTCNoUserReq(t, `{"reason":"x"}`))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

func TestCovGTC_GDPRDelete_NoWorkspaceContext(t *testing.T) {
	r := gdprTestSetup(t)
	rec := httptest.NewRecorder()
	req := covGTCNoUserReq(t, `{"reason":"x"}`)
	// User present but no workspace id in context → 400.
	req = req.WithContext(withUser(req.Context(), &AuthUser{ID: r.adminID}))
	r.h.DeleteUserData(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (missing workspace)", rec.Code)
	}
}

func TestCovGTC_GDPRDelete_MissingUserIDPath(t *testing.T) {
	r := gdprTestSetup(t)
	rec := httptest.NewRecorder()
	// Empty userId path value (whitespace) → 400.
	req := r.adminReq(t, http.MethodDelete, `{"reason":"x"}`, "   ", "ADMIN")
	r.h.DeleteUserData(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (missing userId)", rec.Code)
	}
}

func TestCovGTC_GDPRDelete_InvalidJSON(t *testing.T) {
	r := gdprTestSetup(t)
	rec := httptest.NewRecorder()
	r.h.DeleteUserData(rec, r.adminReq(t, http.MethodDelete, `not-json`, r.targetID, "ADMIN"))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (invalid JSON)", rec.Code)
	}
}

func TestCovGTC_GDPRDelete_AuditInsertFails_500(t *testing.T) {
	r := gdprTestSetup(t)
	// Closing the DB makes the audit-row INSERT fail → 500 before any cascade.
	_ = r.db.Close()
	rec := httptest.NewRecorder()
	r.h.DeleteUserData(rec, r.adminReq(t, http.MethodDelete, `{"reason":"x"}`, r.targetID, "ADMIN"))
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500 (audit insert failed)", rec.Code)
	}
}

func TestCovGTC_GDPRExport_Unauthenticated(t *testing.T) {
	r := gdprTestSetup(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.SetPathValue("userId", r.targetID)
	r.h.ExportUserData(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

func TestCovGTC_GDPRExport_AuditInsertFails_500(t *testing.T) {
	r := gdprTestSetup(t)
	_ = r.db.Close()
	rec := httptest.NewRecorder()
	r.h.ExportUserData(rec, r.adminReq(t, http.MethodGet, "", r.targetID, "ADMIN"))
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500 (audit insert failed)", rec.Code)
	}
}

// covGTCgdprWithBrokenTable creates a fresh rig, seeds the audit row path
// (the gdpr_actions INSERT must still succeed) but then drops a cascade table
// so the SELECT/DELETE against it fails, exercising the firstErr → 207 / 500
// branches. We drop peer_cards which both endpoints query first.
func covGTCdropPeerCards(t *testing.T, r *gdprTestRig) {
	t.Helper()
	if _, err := r.db.Exec(`DROP TABLE peer_cards`); err != nil {
		t.Fatalf("drop peer_cards: %v", err)
	}
}

func TestCovGTC_GDPRDelete_CascadeError_207(t *testing.T) {
	r := gdprTestSetup(t)
	covGTCdropPeerCards(t, r)
	rec := httptest.NewRecorder()
	r.h.DeleteUserData(rec, r.adminReq(t, http.MethodDelete, `{"reason":"x"}`, r.targetID, "ADMIN"))
	// Audit row inserts fine; the peer_cards SELECT fails → firstErr set →
	// 207 Multi-Status (partial success, audit row tells the full story).
	if rec.Code != http.StatusMultiStatus {
		t.Errorf("status = %d, want 207 (cascade partial failure)", rec.Code)
	}
}

func TestCovGTC_GDPRExport_QueryError_500(t *testing.T) {
	r := gdprTestSetup(t)
	covGTCdropPeerCards(t, r)
	rec := httptest.NewRecorder()
	r.h.ExportUserData(rec, r.adminReq(t, http.MethodGet, "", r.targetID, "ADMIN"))
	// Any table query failure → 500 (never ship a partial Art.15 export).
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500 (export query failed)", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// task_handler.go
// ---------------------------------------------------------------------------

func TestCovGTC_CreateTask_MissionLookupDBError_500(t *testing.T) {
	h, userID, wsID, crewID, _, missionID := newMissionHandlerForTasks(t)
	h.db.Close() // mission status SELECT fails (not sql.ErrNoRows) → 500

	req := httptest.NewRequest("POST", "/", bytes.NewBufferString(`{"title":"x"}`))
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("missionId", missionID)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.CreateTask(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rr.Code)
	}
}

func TestCovGTC_CreateTask_DependencyCompleted_Pending(t *testing.T) {
	h, userID, wsID, crewID, _, missionID := newMissionHandlerForTasks(t)
	// A COMPLETED dependency means the new task is unblocked → stays PENDING
	// (exercises the allCompleted==true branch, distinct from the BLOCKED case
	// already covered).
	depID := generateCUID()
	h.db.Exec(`INSERT INTO mission_tasks(id,mission_id,title,status,task_order,depends_on,created_at,updated_at)
		VALUES (?,?,?,?,?,?,datetime('now'),datetime('now'))`, depID, missionID, "Dep", "COMPLETED", 1, "[]")

	body := bytes.NewBufferString(`{"title":"after","depends_on":["` + depID + `"]}`)
	req := httptest.NewRequest("POST", "/", body)
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("missionId", missionID)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.CreateTask(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var resp missionTaskResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp.Status != "PENDING" {
		t.Errorf("status = %q, want PENDING (all deps COMPLETED)", resp.Status)
	}
}

func TestCovGTC_UpdateTask_BeginTxError_500(t *testing.T) {
	h, userID, wsID, crewID, _, missionID := newMissionHandlerForTasks(t)
	h.db.Close() // BeginTx fails → 500

	req := httptest.NewRequest("PATCH", "/", bytes.NewBufferString(`{"status":"IN_PROGRESS"}`))
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("missionId", missionID)
	req.SetPathValue("taskId", "x")
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.UpdateTask(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rr.Code)
	}
}

func TestCovGTC_UpdateTask_MetadataFields_OK(t *testing.T) {
	h, userID, wsID, crewID, _, missionID := newMissionHandlerForTasks(t)
	tID := generateCUID()
	h.db.Exec(`INSERT INTO mission_tasks(id,mission_id,title,status,task_order,depends_on,created_at,updated_at)
		VALUES (?,?,?,?,?,?,datetime('now'),datetime('now'))`, tID, missionID, "Task", "PENDING", 1, "[]")

	// Only metadata fields (no status / editable-field gate) — exercises
	// applyTaskMetadataFields' non-empty update builder path.
	body := bytes.NewBufferString(`{"result_summary":"done","output_path":"/out","token_count":42,"estimated_cost":0.5,"error_message":"none","assigned_agent_id":"agent-worker"}`)
	req := httptest.NewRequest("PATCH", "/", body)
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("missionId", missionID)
	req.SetPathValue("taskId", tID)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.UpdateTask(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var got string
	if err := h.db.QueryRow(`SELECT COALESCE(result_summary,'') FROM mission_tasks WHERE id=?`, tID).Scan(&got); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got != "done" {
		t.Errorf("result_summary = %q, want done", got)
	}
}

func TestCovGTC_UpdateTask_EditableTitleAndDeps_OK(t *testing.T) {
	h, userID, wsID, crewID, _, missionID := newMissionHandlerForTasks(t)
	tID := generateCUID()
	h.db.Exec(`INSERT INTO mission_tasks(id,mission_id,title,status,task_order,depends_on,created_at,updated_at)
		VALUES (?,?,?,?,?,?,datetime('now'),datetime('now'))`, tID, missionID, "Task", "PENDING", 1, "[]")

	// Editable fields on a PENDING task: title + description + empty depends_on.
	// Exercises applyTaskEditableFields success branches (title/description
	// UPDATEs and the len(depIDs)==0 → PENDING depends_on path).
	body := bytes.NewBufferString(`{"title":"renamed","description":"new desc","depends_on":"[]"}`)
	req := httptest.NewRequest("PATCH", "/", body)
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("missionId", missionID)
	req.SetPathValue("taskId", tID)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.UpdateTask(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var title string
	if err := h.db.QueryRow(`SELECT title FROM mission_tasks WHERE id=?`, tID).Scan(&title); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if title != "renamed" {
		t.Errorf("title = %q, want renamed", title)
	}
}

func TestCovGTC_UpdateTask_CompletedUnblocksDependents(t *testing.T) {
	h, userID, wsID, crewID, _, missionID := newMissionHandlerForTasks(t)
	// in-progress task that we transition to COMPLETED → unblockNeeded path +
	// broadcast-on-status branch + the dependent gets unblocked.
	doneID := generateCUID()
	h.db.Exec(`INSERT INTO mission_tasks(id,mission_id,title,status,task_order,depends_on,created_at,updated_at)
		VALUES (?,?,?,?,?,?,datetime('now'),datetime('now'))`, doneID, missionID, "Lead", "IN_PROGRESS", 1, "[]")
	depID := generateCUID()
	h.db.Exec(`INSERT INTO mission_tasks(id,mission_id,title,status,task_order,depends_on,created_at,updated_at)
		VALUES (?,?,?,?,?,?,datetime('now'),datetime('now'))`, depID, missionID, "Dependent", "BLOCKED", 2, `["`+doneID+`"]`)

	body := bytes.NewBufferString(`{"status":"COMPLETED"}`)
	req := httptest.NewRequest("PATCH", "/", body)
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("missionId", missionID)
	req.SetPathValue("taskId", doneID)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.UpdateTask(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var depStatus string
	if err := h.db.QueryRow(`SELECT status FROM mission_tasks WHERE id=?`, depID).Scan(&depStatus); err != nil {
		t.Fatalf("read dependent: %v", err)
	}
	if depStatus != "PENDING" {
		t.Errorf("dependent status = %q, want PENDING (should have unblocked)", depStatus)
	}
}

// ---------------------------------------------------------------------------
// consolidate_handler.go
// ---------------------------------------------------------------------------

func TestCovGTC_ConsolidateRun_NoWorkspace_401(t *testing.T) {
	db := setupTestDB(t)
	h := NewConsolidateHandler(db, newTestLogger())
	h.SetConsolidator(&consolidate.Consolidator{DB: db, Journal: noopEmitter{}, Summarizer: &stubSummarizer{}})

	// No workspace in context → 401.
	req := httptest.NewRequest("POST", "/api/v1/consolidate/run", bytes.NewBufferString(`{}`))
	rr := httptest.NewRecorder()
	h.Run(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rr.Code)
	}
}

func TestCovGTC_ConsolidateRun_InvalidJSONBody_400(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewConsolidateHandler(db, newTestLogger())
	h.SetConsolidator(&consolidate.Consolidator{DB: db, Journal: noopEmitter{}, Summarizer: &stubSummarizer{}})

	req := httptest.NewRequest("POST", "/api/v1/consolidate/run", bytes.NewBufferString(`{bad`))
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Run(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (invalid JSON body)", rr.Code)
	}
}

func TestCovGTC_ConsolidateRun_CrewCheckDBError_500(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewConsolidateHandler(db, newTestLogger())
	h.SetConsolidator(&consolidate.Consolidator{DB: db, Journal: noopEmitter{}, Summarizer: &stubSummarizer{}})

	db.Close() // crewLiveInWorkspace query fails (not ErrNoRows) → 500

	req := httptest.NewRequest("POST", "/api/v1/consolidate/run", bytes.NewBufferString(`{"crew_id":"some-crew"}`))
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Run(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500 (crew existence check failed)", rr.Code)
	}
}

func TestCovGTC_ConsolidateRun_ValidCrew_HappyPath_202(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedCrewRow(t, db, "crew-cov", wsID, "Cov", "cov")

	h := NewConsolidateHandler(db, newTestLogger())
	h.SetConsolidator(&consolidate.Consolidator{
		DB:         db,
		Journal:    noopEmitter{},
		Summarizer: &stubSummarizer{},
	})
	h.SetMemoryRoot(t.TempDir())

	body := bytes.NewBufferString(`{"crew_id":"` + crewID + `","since":"6h"}`)
	req := httptest.NewRequest("POST", "/api/v1/consolidate/run", body)
	req = withWorkspaceUser(req, userID, wsID, "ADMIN")
	rr := httptest.NewRecorder()
	h.Run(rr, req)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovGTC_ConsolidateRun_InFlight_409(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	h := NewConsolidateHandler(db, newTestLogger())
	h.SetConsolidator(&consolidate.Consolidator{DB: db, Journal: noopEmitter{}, Summarizer: &stubSummarizer{}})
	// Pre-mark the workspace as running so the in-flight guard fires.
	h.running[wsID] = struct{}{}

	req := httptest.NewRequest("POST", "/api/v1/consolidate/run", bytes.NewBufferString(`{}`))
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Run(rr, req)
	if rr.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409 (already running)", rr.Code)
	}
}

// covGTCcountJournal is a journal.Emitter that records how many entries were
// emitted so runOnce's status-path branches can be asserted synchronously.
type covGTCcapture struct {
	mu       sync.Mutex
	statuses []string
}

func (c *covGTCcapture) Emit(_ context.Context, e journal.Entry) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if s, ok := e.Payload["status"].(string); ok {
		c.statuses = append(c.statuses, s)
	}
	return "", nil
}

func (c *covGTCcapture) Flush(_ context.Context) error { return nil }

func TestCovGTC_ConsolidateRunOnce_CrewScoped_OK(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedCrewRow(t, db, "crew-once", wsID, "Once", "once")

	capJ := &covGTCcapture{}
	h := NewConsolidateHandler(db, newTestLogger())
	h.SetJournal(capJ)
	h.SetConsolidator(&consolidate.Consolidator{DB: db, Journal: noopEmitter{}, Summarizer: &stubSummarizer{}})
	h.SetMemoryRoot(t.TempDir())

	// Drive runOnce synchronously: crew-scoped success path → emits "ok".
	h.runOnce(context.Background(), wsID, crewID, time.Hour, "worker-cov")

	capJ.mu.Lock()
	defer capJ.mu.Unlock()
	if len(capJ.statuses) != 1 || capJ.statuses[0] != "ok" {
		t.Errorf("emitted statuses = %v, want [ok]", capJ.statuses)
	}
}

func TestCovGTC_ConsolidateRunOnce_CrewNotFound(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	capJ := &covGTCcapture{}
	h := NewConsolidateHandler(db, newTestLogger())
	h.SetJournal(capJ)
	h.SetConsolidator(&consolidate.Consolidator{DB: db, Journal: noopEmitter{}, Summarizer: &stubSummarizer{}})

	// A crew id that does not exist → "crew-not-found" status emitted.
	h.runOnce(context.Background(), wsID, "ghost-crew", time.Hour, "worker-cov")

	capJ.mu.Lock()
	defer capJ.mu.Unlock()
	if len(capJ.statuses) != 1 || capJ.statuses[0] != "crew-not-found" {
		t.Errorf("emitted statuses = %v, want [crew-not-found]", capJ.statuses)
	}
}

func TestCovGTC_ConsolidateRunOnce_Workspacewide_OK(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedCrewRow(t, db, "crew-a", wsID, "A", "a")
	seedCrewRow(t, db, "crew-b", wsID, "B", "b")

	capJ := &covGTCcapture{}
	h := NewConsolidateHandler(db, newTestLogger())
	h.SetJournal(capJ)
	h.SetConsolidator(&consolidate.Consolidator{DB: db, Journal: noopEmitter{}, Summarizer: &stubSummarizer{}})
	h.SetMemoryRoot(t.TempDir())

	// Empty crew id → enumerate all crews in the workspace, success path.
	h.runOnce(context.Background(), wsID, "", time.Hour, "worker-cov")

	capJ.mu.Lock()
	defer capJ.mu.Unlock()
	if len(capJ.statuses) != 1 || capJ.statuses[0] != "ok" {
		t.Errorf("emitted statuses = %v, want [ok]", capJ.statuses)
	}
}
