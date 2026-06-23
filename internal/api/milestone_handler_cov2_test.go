package api

// milestone_handler.go coverage top-up #2 — List scan error (lax
// schema), Update exec / read-back failures, and the Delete tx branches
// (unlink failure, delete failure, race-lost 404). Reuses covMSRig /
// covMSReq. All tests are prefixed TestCov2MS.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCov2MSList_ScanError500(t *testing.T) {
	h, db, userID, wsID, pid := covMSRig(t)
	if _, err := db.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		t.Fatalf("fk off: %v", err)
	}
	if _, err := db.Exec(`DROP TABLE milestones`); err != nil {
		t.Fatalf("drop: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE milestones (
		id TEXT PRIMARY KEY, project_id TEXT, name TEXT, description TEXT,
		target_date TEXT, status TEXT, position INTEGER DEFAULT 0,
		created_at TEXT DEFAULT (datetime('now')), updated_at TEXT DEFAULT (datetime('now')))`); err != nil {
		t.Fatalf("recreate: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO milestones (id, project_id, name, status)
		VALUES ('ms-null', ?, NULL, 'active')`, pid); err != nil {
		t.Fatalf("insert: %v", err)
	}

	rec := httptest.NewRecorder()
	h.List(rec, covMSReq(userID, wsID, "MEMBER", "GET", "", pid, ""))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (NULL name scan), body=%s", rec.Code, rec.Body.String())
	}
}

func TestCov2MSUpdate_ExecBlocked500(t *testing.T) {
	h, db, userID, wsID, pid := covMSRig(t)
	if _, err := db.Exec(`INSERT INTO milestones (id, project_id, name) VALUES ('ms-u', ?, 'M')`, pid); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := db.Exec(`CREATE TRIGGER cov2ms_upd BEFORE UPDATE ON milestones
		BEGIN SELECT RAISE(ABORT,'blocked'); END`); err != nil {
		t.Fatalf("trigger: %v", err)
	}
	rec := httptest.NewRecorder()
	h.Update(rec, covMSReq(userID, wsID, "MANAGER", "PATCH", `{"name":"new"}`, pid, "ms-u"))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500, body=%s", rec.Code, rec.Body.String())
	}
}

func TestCov2MSUpdate_ReadBackGone500(t *testing.T) {
	h, db, userID, wsID, pid := covMSRig(t)
	if _, err := db.Exec(`INSERT INTO milestones (id, project_id, name) VALUES ('ms-rb', ?, 'M')`, pid); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// AFTER UPDATE: vaporize the row so the read-back finds nothing.
	if _, err := db.Exec(`CREATE TRIGGER cov2ms_vanish AFTER UPDATE ON milestones
		BEGIN DELETE FROM milestones WHERE id = NEW.id; END`); err != nil {
		t.Fatalf("trigger: %v", err)
	}
	rec := httptest.NewRecorder()
	h.Update(rec, covMSReq(userID, wsID, "MANAGER", "PATCH", `{"name":"new"}`, pid, "ms-rb"))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (read-back gone), body=%s", rec.Code, rec.Body.String())
	}
}

func TestCov2MSDelete_TxBranches(t *testing.T) {
	t.Run("unlink missions blocked 500", func(t *testing.T) {
		h, db, userID, wsID, pid := covMSRig(t)
		if _, err := db.Exec(`INSERT INTO milestones (id, project_id, name) VALUES ('ms-d1', ?, 'M')`, pid); err != nil {
			t.Fatalf("seed: %v", err)
		}
		// A mission linked to the milestone so the unlink UPDATE visits a row.
		crewID := seedCrewRow(t, db, "ms-crew", wsID, "C", "ms-crew")
		leadID := seedAgentRow(t, db, "ms-lead", wsID, crewID, "L", "ms-lead", "LEAD")
		if _, err := db.Exec(`INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title, status, milestone_id)
			VALUES ('ms-m1', ?, ?, ?, 'tr-ms1', 'T', 'PLANNING', 'ms-d1')`, wsID, crewID, leadID); err != nil {
			t.Fatalf("seed mission: %v", err)
		}
		if _, err := db.Exec(`CREATE TRIGGER cov2ms_unlink BEFORE UPDATE ON missions
			BEGIN SELECT RAISE(ABORT,'blocked'); END`); err != nil {
			t.Fatalf("trigger: %v", err)
		}
		rec := httptest.NewRecorder()
		h.Delete(rec, covMSReq(userID, wsID, "OWNER", "DELETE", "", pid, "ms-d1"))
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500, body=%s", rec.Code, rec.Body.String())
		}
	})

	t.Run("delete blocked 500", func(t *testing.T) {
		h, db, userID, wsID, pid := covMSRig(t)
		if _, err := db.Exec(`INSERT INTO milestones (id, project_id, name) VALUES ('ms-d2', ?, 'M')`, pid); err != nil {
			t.Fatalf("seed: %v", err)
		}
		if _, err := db.Exec(`CREATE TRIGGER cov2ms_del BEFORE DELETE ON milestones
			BEGIN SELECT RAISE(ABORT,'blocked'); END`); err != nil {
			t.Fatalf("trigger: %v", err)
		}
		rec := httptest.NewRecorder()
		h.Delete(rec, covMSReq(userID, wsID, "OWNER", "DELETE", "", pid, "ms-d2"))
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500, body=%s", rec.Code, rec.Body.String())
		}
	})

	t.Run("race-lost delete 404", func(t *testing.T) {
		h, db, userID, wsID, pid := covMSRig(t)
		if _, err := db.Exec(`INSERT INTO milestones (id, project_id, name) VALUES ('ms-d3', ?, 'M')`, pid); err != nil {
			t.Fatalf("seed: %v", err)
		}
		if _, err := db.Exec(`CREATE TRIGGER cov2ms_race BEFORE DELETE ON milestones
			BEGIN SELECT RAISE(IGNORE); END`); err != nil {
			t.Fatalf("trigger: %v", err)
		}
		rec := httptest.NewRecorder()
		h.Delete(rec, covMSReq(userID, wsID, "OWNER", "DELETE", "", pid, "ms-d3"))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404 (0 rows deleted), body=%s", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "Milestone not found") {
			t.Errorf("body = %s, want not-found message", rec.Body.String())
		}
	})
}
