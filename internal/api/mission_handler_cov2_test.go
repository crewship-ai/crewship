package api

// mission_handler.go + mission_handler_mutate.go coverage top-up #2 —
// the DB-failure forks (insert/update triggers, dropped tables, lax-
// schema scan errors), the Start CAS-conflict 409 (RAISE(IGNORE)
// trigger), and the ValidateDAG 400 using a real MissionEngine over a
// cyclic task graph.
//
// All tests are prefixed TestCov2Mis.

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/orchestrator"
)

// cov2MisRig: ws + crew + LEAD agent + handler (no hub, no engine).
func cov2MisRig(t *testing.T) (*MissionHandler, *sql.DB, string, string, string) {
	t.Helper()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedCrewRow(t, db, "c2mis_crew", wsID, "Crew", "c2mis-crew")
	leadID := seedAgentRow(t, db, "c2mis_lead", wsID, crewID, "Lead", "c2mis-lead", "LEAD")
	return NewMissionHandler(db, nil, nil, newTestLogger()), db, wsID, crewID, leadID
}

func cov2MisSeedMission(t *testing.T, db *sql.DB, id, wsID, crewID, leadID, status string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title, status)
		VALUES (?, ?, ?, ?, ?, 'Mission', ?)`, id, wsID, crewID, leadID, "trace-"+id, status); err != nil {
		t.Fatalf("seed mission: %v", err)
	}
}

func cov2MisReq(method, target, body, wsID, crewID, missionID string) *http.Request {
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, target, nil)
	} else {
		r = httptest.NewRequest(method, target, strings.NewReader(body))
	}
	r.SetPathValue("crewId", crewID)
	if missionID != "" {
		r.SetPathValue("missionId", missionID)
	}
	return r.WithContext(withWorkspace(r.Context(), wsID, "OWNER"))
}

func cov2MisTrigger(t *testing.T, db *sql.DB, name, body string) {
	t.Helper()
	if _, err := db.Exec(`CREATE TRIGGER ` + name + ` ` + body); err != nil {
		t.Fatalf("create trigger %s: %v", name, err)
	}
}

// --- Create: tx insert failures ---

func TestCov2MisCreate_InsertFailures500(t *testing.T) {
	for _, tc := range []struct{ name, table string }{
		{"missions insert blocked", "missions"},
		{"chats insert blocked", "chats"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, db, wsID, crewID, leadID := cov2MisRig(t)
			cov2MisTrigger(t, db, "c2mis_ins_"+tc.table,
				`BEFORE INSERT ON `+tc.table+` BEGIN SELECT RAISE(ABORT, 'blocked'); END`)
			body := `{"title":"M","lead_agent_id":"` + leadID + `"}`
			rec := httptest.NewRecorder()
			h.Create(rec, cov2MisReq("POST", "/x", body, wsID, crewID, ""))
			if rec.Code != http.StatusInternalServerError {
				t.Fatalf("status = %d, want 500, body=%s", rec.Code, rec.Body.String())
			}
			// Rollback: nothing persisted.
			var n int
			if err := db.QueryRow(`SELECT COUNT(*) FROM missions`).Scan(&n); err != nil || n != 0 {
				t.Errorf("missions rows = %d (err %v), want 0", n, err)
			}
		})
	}
}

// --- Update: lookup error, per-field update failures, stale read ---

func TestCov2MisUpdate_LookupError500(t *testing.T) {
	h, db, wsID, crewID, _ := cov2MisRig(t)
	if _, err := db.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		t.Fatalf("fk off: %v", err)
	}
	if _, err := db.Exec(`DROP TABLE missions`); err != nil {
		t.Fatalf("drop: %v", err)
	}
	rec := httptest.NewRecorder()
	h.Update(rec, cov2MisReq("PATCH", "/x", `{"title":"t"}`, wsID, crewID, "m1"))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500, body=%s", rec.Code, rec.Body.String())
	}
}

func TestCov2MisUpdate_FieldUpdateFailures500(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"status", `{"status":"CANCELLED"}`},
		{"title", `{"title":"new"}`},
		{"description", `{"description":"new"}`},
		{"plan", `{"plan":"new"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, db, wsID, crewID, leadID := cov2MisRig(t)
			cov2MisSeedMission(t, db, "m-upd", wsID, crewID, leadID, "PLANNING")
			cov2MisTrigger(t, db, "c2mis_upd_block",
				`BEFORE UPDATE ON missions BEGIN SELECT RAISE(ABORT, 'blocked'); END`)
			rec := httptest.NewRecorder()
			h.Update(rec, cov2MisReq("PATCH", "/x", tc.body, wsID, crewID, "m-upd"))
			if rec.Code != http.StatusInternalServerError {
				t.Fatalf("status = %d, want 500, body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}

// The post-commit re-read joins agents; a mission whose lead agent row
// vanished scans zero rows → 500 (after the update itself committed).
func TestCov2MisUpdate_ReadBackFails500(t *testing.T) {
	h, db, wsID, crewID, _ := cov2MisRig(t)
	if _, err := db.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		t.Fatalf("fk off: %v", err)
	}
	cov2MisSeedMission(t, db, "m-ghost", wsID, crewID, "ghost-agent", "PLANNING")
	rec := httptest.NewRecorder()
	h.Update(rec, cov2MisReq("PATCH", "/x", `{"title":"renamed"}`, wsID, crewID, "m-ghost"))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (read-back join missed), body=%s", rec.Code, rec.Body.String())
	}
	// The update itself committed before the failed read-back.
	var title string
	if err := db.QueryRow(`SELECT title FROM missions WHERE id = 'm-ghost'`).Scan(&title); err != nil {
		t.Fatalf("read title: %v", err)
	}
	if title != "renamed" {
		t.Errorf("title = %q, want renamed (commit happened before read-back)", title)
	}
}

// --- Start: CAS conflict, update failure, DAG validation ---

func TestCov2MisStart_UpdateBlocked500(t *testing.T) {
	h, db, wsID, crewID, leadID := cov2MisRig(t)
	cov2MisSeedMission(t, db, "m-st1", wsID, crewID, leadID, "PLANNING")
	cov2MisTrigger(t, db, "c2mis_start_block",
		`BEFORE UPDATE ON missions BEGIN SELECT RAISE(ABORT, 'blocked'); END`)
	rec := httptest.NewRecorder()
	h.Start(rec, cov2MisReq("POST", "/x", "", wsID, crewID, "m-st1"))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500, body=%s", rec.Code, rec.Body.String())
	}
}

func TestCov2MisStart_CASConflict409(t *testing.T) {
	h, db, wsID, crewID, leadID := cov2MisRig(t)
	cov2MisSeedMission(t, db, "m-st2", wsID, crewID, leadID, "PLANNING")
	// RAISE(IGNORE) silently skips the row → RowsAffected 0 → the handler
	// reports the concurrent-start conflict.
	cov2MisTrigger(t, db, "c2mis_start_ignore",
		`BEFORE UPDATE ON missions BEGIN SELECT RAISE(IGNORE); END`)
	rec := httptest.NewRecorder()
	h.Start(rec, cov2MisReq("POST", "/x", "", wsID, crewID, "m-st2"))
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (CAS lost), body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "already started") {
		t.Errorf("body = %s, want already-started conflict", rec.Body.String())
	}
}

func TestCov2MisStart_InvalidDAG400(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedCrewRow(t, db, "c2mis_dag_crew", wsID, "Crew", "c2mis-dag")
	leadID := seedAgentRow(t, db, "c2mis_dag_lead", wsID, crewID, "Lead", "c2mis-dag-lead", "LEAD")
	engine := orchestrator.NewMissionEngine(db, nil, nil, newTestLogger())
	h := NewMissionHandler(db, nil, engine, newTestLogger())

	cov2MisSeedMission(t, db, "m-dag", wsID, crewID, leadID, "PLANNING")
	// Two tasks depending on each other → cycle.
	for _, row := range [][2]string{{"t1", `["t2"]`}, {"t2", `["t1"]`}} {
		if _, err := db.Exec(`INSERT INTO mission_tasks (id, mission_id, title, status, depends_on)
			VALUES (?, 'm-dag', 'T', 'PENDING', ?)`, row[0], row[1]); err != nil {
			t.Fatalf("seed task %s: %v", row[0], err)
		}
	}

	rec := httptest.NewRecorder()
	h.Start(rec, cov2MisReq("POST", "/x", "", wsID, crewID, "m-dag"))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (cyclic DAG), body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Invalid task DAG") {
		t.Errorf("body = %s, want DAG validation error", rec.Body.String())
	}
	// Status must stay PLANNING — validation failed before the CAS.
	var status string
	if err := db.QueryRow(`SELECT status FROM missions WHERE id = 'm-dag'`).Scan(&status); err != nil {
		t.Fatalf("read status: %v", err)
	}
	if status != "PLANNING" {
		t.Errorf("status = %q, want PLANNING", status)
	}
}

// --- List / ListAll: scan errors via lax schema, stats fallbacks ---

// cov2MisLaxMissions replaces missions with a constraint-free copy and
// inserts one row with NULL title so scanMission fails.
func cov2MisLaxMissions(t *testing.T, db *sql.DB, wsID, crewID, leadID string) {
	t.Helper()
	if _, err := db.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		t.Fatalf("fk off: %v", err)
	}
	if _, err := db.Exec(`DROP TABLE missions`); err != nil {
		t.Fatalf("drop: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE missions (
		id TEXT PRIMARY KEY, workspace_id TEXT, crew_id TEXT, lead_agent_id TEXT,
		trace_id TEXT, title TEXT, description TEXT, status TEXT, plan TEXT,
		workflow_template TEXT, total_token_count INTEGER, total_estimated_cost REAL,
		created_at TEXT DEFAULT (datetime('now')), updated_at TEXT DEFAULT (datetime('now')),
		completed_at TEXT)`); err != nil {
		t.Fatalf("recreate: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title, status)
		VALUES ('m-null', ?, ?, ?, 'tr-null', NULL, 'PLANNING')`, wsID, crewID, leadID); err != nil {
		t.Fatalf("insert null-title row: %v", err)
	}
}

func TestCov2MisList_ScanError500(t *testing.T) {
	h, db, wsID, crewID, leadID := cov2MisRig(t)
	cov2MisLaxMissions(t, db, wsID, crewID, leadID)
	rec := httptest.NewRecorder()
	h.List(rec, cov2MisReq("GET", "/x", "", wsID, crewID, ""))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (NULL title scan), body=%s", rec.Code, rec.Body.String())
	}
}

func TestCov2MisListAll_ScanError500(t *testing.T) {
	h, db, wsID, crewID, leadID := cov2MisRig(t)
	cov2MisLaxMissions(t, db, wsID, crewID, leadID)
	rec := httptest.NewRecorder()
	h.ListAll(rec, cov2MisReq("GET", "/x", "", wsID, crewID, ""))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (NULL title scan), body=%s", rec.Code, rec.Body.String())
	}
}

func TestCov2MisList_BatchStatsFailureIsSoft(t *testing.T) {
	h, db, wsID, crewID, leadID := cov2MisRig(t)
	cov2MisSeedMission(t, db, "m-l1", wsID, crewID, leadID, "PLANNING")
	if _, err := db.Exec(`DROP TABLE mission_tasks`); err != nil {
		t.Fatalf("drop: %v", err)
	}
	rec := httptest.NewRecorder()
	h.List(rec, cov2MisReq("GET", "/x", "", wsID, crewID, ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (stats failure is soft), body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "m-l1") {
		t.Errorf("body = %s, want mission row", rec.Body.String())
	}
}

func TestCov2MisListAll_IncludeTasksFailureYieldsEmptyTasks(t *testing.T) {
	h, db, wsID, crewID, leadID := cov2MisRig(t)
	cov2MisSeedMission(t, db, "m-la1", wsID, crewID, leadID, "PLANNING")
	if _, err := db.Exec(`DROP TABLE mission_tasks`); err != nil {
		t.Fatalf("drop: %v", err)
	}
	rec := httptest.NewRecorder()
	h.ListAll(rec, cov2MisReq("GET", "/x?include_tasks=true", "", wsID, crewID, ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rec.Code, rec.Body.String())
	}
	// The tasks-load failure falls back to an empty slice (omitted by
	// omitempty in JSON) — the mission row itself must still be returned.
	if !strings.Contains(rec.Body.String(), "m-la1") {
		t.Errorf("body = %s, want mission row despite tasks-load failure", rec.Body.String())
	}
}

// --- Get: tasks load failure → 500 ---

func TestCov2MisGet_TasksLoadFailure500(t *testing.T) {
	h, db, wsID, crewID, leadID := cov2MisRig(t)
	cov2MisSeedMission(t, db, "m-g1", wsID, crewID, leadID, "PLANNING")
	if _, err := db.Exec(`DROP TABLE mission_tasks`); err != nil {
		t.Fatalf("drop: %v", err)
	}
	rec := httptest.NewRecorder()
	h.Get(rec, cov2MisReq("GET", "/x", "", wsID, crewID, "m-g1"))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (tasks load), body=%s", rec.Code, rec.Body.String())
	}
}

// --- Metrics: task-table failures are warn-only ---

func TestCov2MisMetrics_TaskQueriesFailSoft(t *testing.T) {
	h, db, wsID, crewID, leadID := cov2MisRig(t)
	cov2MisSeedMission(t, db, "m-mx", wsID, crewID, leadID, "PLANNING")
	if _, err := db.Exec(`DROP TABLE mission_tasks`); err != nil {
		t.Fatalf("drop: %v", err)
	}
	rec := httptest.NewRecorder()
	h.Metrics(rec, cov2MisReq("GET", "/x", "", wsID, crewID, ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (token/task metrics warn only), body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"total_missions":1`) {
		t.Errorf("body = %s, want total_missions:1", rec.Body.String())
	}
}
