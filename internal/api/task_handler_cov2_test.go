package api

// task_handler.go coverage top-up #2 — the trigger-injected UPDATE
// failure forks of applyTaskEditableFields / applyTaskStatus /
// applyTaskMetadataFields, the dependency scan-error branches (lax
// schema), and UpdateTask's lookup-error 500. Reuses covMHRig and the
// covTH request builders. All tests are prefixed TestCov2TH.

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// --- CreateTask: dependency scan error (NULL status row) ---

func TestCov2THCreateTask_DepScanError500(t *testing.T) {
	r := newCovMHRig(t)
	covMMSeedMission(t, r.db, "m-c2", r.wsID, r.crewID, r.leadID, "PLANNING")
	// Replace mission_tasks with a lax copy and plant a NULL-status dep.
	if _, err := r.db.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		t.Fatalf("fk off: %v", err)
	}
	if _, err := r.db.Exec(`DROP TABLE mission_tasks`); err != nil {
		t.Fatalf("drop: %v", err)
	}
	if _, err := r.db.Exec(`CREATE TABLE mission_tasks (
		id TEXT PRIMARY KEY, mission_id TEXT, assigned_agent_id TEXT, title TEXT,
		description TEXT, status TEXT, task_order INTEGER DEFAULT 0,
		depends_on TEXT DEFAULT '[]', iteration INTEGER DEFAULT 1, max_iterations INTEGER,
		result_summary TEXT, output_path TEXT, error_message TEXT, assignment_id TEXT,
		token_count INTEGER, estimated_cost REAL, started_at TEXT, completed_at TEXT,
		duration_ms INTEGER, created_at TEXT DEFAULT (datetime('now')),
		updated_at TEXT DEFAULT (datetime('now')))`); err != nil {
		t.Fatalf("recreate: %v", err)
	}
	if _, err := r.db.Exec(`INSERT INTO mission_tasks (id, mission_id, title, status)
		VALUES ('dep-null', 'm-c2', 'Dep', NULL)`); err != nil {
		t.Fatalf("insert: %v", err)
	}

	rec := httptest.NewRecorder()
	r.h.CreateTask(rec, covTHCreateReq(r, "m-c2", `{"title":"T","depends_on":["dep-null"]}`))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (NULL status scan), body=%s", rec.Code, rec.Body.String())
	}
}

// --- UpdateTask: lookup error → 500 ---

func TestCov2THUpdateTask_LookupError500(t *testing.T) {
	r := newCovMHRig(t)
	covMMSeedMission(t, r.db, "m-c3", r.wsID, r.crewID, r.leadID, "PLANNING")
	if _, err := r.db.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		t.Fatalf("fk off: %v", err)
	}
	if _, err := r.db.Exec(`DROP TABLE mission_tasks`); err != nil {
		t.Fatalf("drop: %v", err)
	}
	rec := httptest.NewRecorder()
	r.h.UpdateTask(rec, covTHUpdateReq(r, "m-c3", "t-x", `{"title":"new"}`))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (lookup), body=%s", rec.Code, rec.Body.String())
	}
}

// --- UpdateTask: per-field UPDATE failures via trigger ---

func TestCov2THUpdateTask_FieldUpdateBlocked500(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"status", `{"status":"IN_PROGRESS"}`},
		{"title", `{"title":"new"}`},
		{"description", `{"description":"new"}`},
		{"depends_on", `{"depends_on":"[]"}`},
		{"metadata", `{"result_summary":"done"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newCovMHRig(t)
			covMMSeedMission(t, r.db, "m-c4", r.wsID, r.crewID, r.leadID, "IN_PROGRESS")
			r.seedTask(t, "t-blk", "m-c4", "PENDING", 1)
			if _, err := r.db.Exec(`CREATE TRIGGER cov2th_block BEFORE UPDATE ON mission_tasks
				BEGIN SELECT RAISE(ABORT, 'blocked'); END`); err != nil {
				t.Fatalf("trigger: %v", err)
			}
			rec := httptest.NewRecorder()
			r.h.UpdateTask(rec, covTHUpdateReq(r, "m-c4", "t-blk", tc.body))
			if rec.Code != http.StatusInternalServerError {
				t.Fatalf("status = %d, want 500 (update blocked), body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}

// --- UpdateTask: depends_on recomputes status (BLOCKED on open dep) ---

func TestCov2THUpdateTask_DependsOnRecomputesStatus(t *testing.T) {
	r := newCovMHRig(t)
	covMMSeedMission(t, r.db, "m-c5", r.wsID, r.crewID, r.leadID, "IN_PROGRESS")
	r.seedTask(t, "t-dep-open", "m-c5", "IN_PROGRESS", 1)
	r.seedTask(t, "t-edit", "m-c5", "PENDING", 2)

	rec := httptest.NewRecorder()
	r.h.UpdateTask(rec, covTHUpdateReq(r, "m-c5", "t-edit", `{"depends_on":"[\"t-dep-open\"]"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rec.Code, rec.Body.String())
	}
	var status string
	if err := r.db.QueryRow(`SELECT status FROM mission_tasks WHERE id = 't-edit'`).Scan(&status); err != nil {
		t.Fatalf("read: %v", err)
	}
	if status != "BLOCKED" {
		t.Errorf("status = %q, want BLOCKED (open dependency)", status)
	}
}

// --- UpdateTask: dependency on a missing task → 400 ---

func TestCov2THUpdateTask_DependsOnGhost400(t *testing.T) {
	r := newCovMHRig(t)
	covMMSeedMission(t, r.db, "m-c6", r.wsID, r.crewID, r.leadID, "IN_PROGRESS")
	r.seedTask(t, "t-e2", "m-c6", "PENDING", 1)

	rec := httptest.NewRecorder()
	r.h.UpdateTask(rec, covTHUpdateReq(r, "m-c6", "t-e2", `{"depends_on":"[\"ghost\"]"}`))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (ghost dep), body=%s", rec.Code, rec.Body.String())
	}
}
