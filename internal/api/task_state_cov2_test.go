package api

// Second coverage pass for task_state.go. Adds the branches the first pass
// skipped:
//
//   - Resume with a wired MissionEngine: ValidateDAG rejection (invalid
//     dependency) including the deferred FAILED rollback, and the
//     StartMission failure rollback (forced by renaming `crews` so the
//     engine's mission-load JOIN errors after the SQL phase committed).
//   - Restart / Resume task-reset write failures (SQLite RAISE triggers).
//   - Clone insert failures for the synthetic chat, the mission row and the
//     task rows (RAISE triggers scoped via WHEN clauses).
//
// Triggers let a specific mid-handler statement fail while everything
// before it succeeds — no closed-DB sledgehammer, no production changes.

import (
	"net/http"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/orchestrator"
)

func covTS2Engine(h *MissionHandler) {
	h.missionEngine = orchestrator.NewMissionEngine(h.db, nil, nil, newTestLogger())
}

// ---- Resume: ValidateDAG failure ----

func TestTS2_Resume_InvalidDAG_RollsBackToFailed(t *testing.T) {
	h, db, userID, wsID, crewID, missionID := covTSFixture(t)
	covTS2Engine(h)
	covTSSetMissionStatus(t, db, missionID, "FAILED")
	// A FAILED task whose depends_on references a nonexistent task id.
	covTSInsertTask(t, db, "ts2-bad", missionID, "T", "FAILED", 1, `["ghost-task"]`)

	req, rr := covTSReq(t, "POST", userID, wsID, crewID, missionID, "OWNER")
	h.Resume(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "Invalid task DAG") {
		t.Errorf("body = %q", rr.Body.String())
	}
	// Deferred rollback must restore FAILED (mission was claimed RESUMING).
	if got := covTSMissionStatus(t, db, missionID); got != "FAILED" {
		t.Errorf("mission status = %q, want FAILED after rollback", got)
	}
}

// ---- Resume: StartMission failure after commit ----

func TestTS2_Resume_EngineStartFails_RollsBackToFailed(t *testing.T) {
	h, db, userID, wsID, crewID, missionID := covTSFixture(t)
	covTS2Engine(h)
	covTSSetMissionStatus(t, db, missionID, "FAILED")
	covTSInsertTask(t, db, "ts2-start", missionID, "T", "FAILED", 1, "[]")

	// Resume itself never touches `crews`, but MissionEngine.StartMission
	// joins it — renaming the table makes only the engine's load fail.
	if _, err := db.Exec(`ALTER TABLE crews RENAME TO crews_hidden_ts2`); err != nil {
		t.Fatalf("rename crews: %v", err)
	}
	t.Cleanup(func() { _, _ = db.Exec(`ALTER TABLE crews_hidden_ts2 RENAME TO crews`) })

	req, rr := covTSReq(t, "POST", userID, wsID, crewID, missionID, "OWNER")
	h.Resume(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "Failed to start mission engine") {
		t.Errorf("body = %q", rr.Body.String())
	}
	if got := covTSMissionStatus(t, db, missionID); got != "FAILED" {
		t.Errorf("mission status = %q, want FAILED after engine rollback", got)
	}
}

// ---- Resume / Restart: task-reset write failures via RAISE triggers ----

func TestTS2_Restart_TaskResetFails500(t *testing.T) {
	h, db, userID, wsID, crewID, missionID := covTSFixture(t)
	covTSSetMissionStatus(t, db, missionID, "FAILED")
	covTSInsertTask(t, db, "ts2-rst", missionID, "T", "FAILED", 1, "[]")

	if _, err := db.Exec(`
		CREATE TRIGGER ts2_block_task_update BEFORE UPDATE ON mission_tasks
		BEGIN SELECT RAISE(ABORT, 'ts2 boom'); END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	req, rr := covTSReq(t, "POST", userID, wsID, crewID, missionID, "OWNER")
	h.Restart(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "Failed to reset tasks") {
		t.Errorf("body = %q", rr.Body.String())
	}
}

func TestTS2_Resume_TaskResetFails500(t *testing.T) {
	h, db, userID, wsID, crewID, missionID := covTSFixture(t)
	covTSSetMissionStatus(t, db, missionID, "FAILED")
	covTSInsertTask(t, db, "ts2-rsm", missionID, "T", "FAILED", 1, "[]")

	if _, err := db.Exec(`
		CREATE TRIGGER ts2_block_task_update2 BEFORE UPDATE ON mission_tasks
		BEGIN SELECT RAISE(ABORT, 'ts2 boom'); END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	req, rr := covTSReq(t, "POST", userID, wsID, crewID, missionID, "OWNER")
	h.Resume(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "Failed to reset task") {
		t.Errorf("body = %q", rr.Body.String())
	}
	// Deferred rollback restores FAILED.
	if got := covTSMissionStatus(t, db, missionID); got != "FAILED" {
		t.Errorf("mission status = %q, want FAILED", got)
	}
}

func TestTS2_Resume_MissionUpdateFails500(t *testing.T) {
	h, db, userID, wsID, crewID, missionID := covTSFixture(t)
	covTSSetMissionStatus(t, db, missionID, "FAILED")
	covTSInsertTask(t, db, "ts2-mup", missionID, "T", "FAILED", 1, "[]")

	// Only the IN_PROGRESS transition fails — the RESUMING claim and the
	// rollbacks (FAILED) pass the WHEN clause untouched.
	if _, err := db.Exec(`
		CREATE TRIGGER ts2_block_inprogress BEFORE UPDATE ON missions
		WHEN NEW.status = 'IN_PROGRESS'
		BEGIN SELECT RAISE(ABORT, 'ts2 no in-progress'); END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	req, rr := covTSReq(t, "POST", userID, wsID, crewID, missionID, "OWNER")
	h.Resume(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "Failed to update mission") {
		t.Errorf("body = %q", rr.Body.String())
	}
	if got := covTSMissionStatus(t, db, missionID); got != "FAILED" {
		t.Errorf("mission status = %q, want FAILED", got)
	}
}

// ---- Clone: per-insert failures ----

func TestTS2_Clone_ChatInsertFails500(t *testing.T) {
	h, db, userID, wsID, crewID, missionID := covTSFixture(t)

	if _, err := db.Exec(`
		CREATE TRIGGER ts2_block_chat_insert BEFORE INSERT ON chats
		BEGIN SELECT RAISE(ABORT, 'ts2 no chats'); END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	req, rr := covTSReq(t, "POST", userID, wsID, crewID, missionID, "OWNER")
	h.Clone(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "Failed to create mission") {
		t.Errorf("body = %q", rr.Body.String())
	}
}

func TestTS2_Clone_MissionInsertFails500(t *testing.T) {
	h, db, userID, wsID, crewID, missionID := covTSFixture(t)

	// Only the cloned mission ("... (copy)") trips the trigger.
	if _, err := db.Exec(`
		CREATE TRIGGER ts2_block_mission_insert BEFORE INSERT ON missions
		WHEN NEW.title LIKE '%(copy)'
		BEGIN SELECT RAISE(ABORT, 'ts2 no clones'); END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	req, rr := covTSReq(t, "POST", userID, wsID, crewID, missionID, "OWNER")
	h.Clone(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "Failed to clone mission") {
		t.Errorf("body = %q", rr.Body.String())
	}
}

func TestTS2_Clone_TaskInsertFails500(t *testing.T) {
	h, db, userID, wsID, crewID, missionID := covTSFixture(t)
	covTSInsertTask(t, db, "ts2-orig", missionID, "Orig", "COMPLETED", 1, "[]")

	// Original tasks already exist; only NEW task inserts fail.
	if _, err := db.Exec(`
		CREATE TRIGGER ts2_block_task_insert BEFORE INSERT ON mission_tasks
		BEGIN SELECT RAISE(ABORT, 'ts2 no task clones'); END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	req, rr := covTSReq(t, "POST", userID, wsID, crewID, missionID, "OWNER")
	h.Clone(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "Failed to clone task") {
		t.Errorf("body = %q", rr.Body.String())
	}
	// Nothing committed: no "(copy)" mission survives the rollback.
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM missions WHERE title LIKE '%(copy)'`).Scan(&n); err != nil {
		t.Fatalf("count clones: %v", err)
	}
	if n != 0 {
		t.Errorf("cloned missions = %d, want 0", n)
	}
}
