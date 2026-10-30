package orchestrator

import (
	"testing"
)

func TestMissionCompletionReceiptRetriesWithoutDuplicateEffects(t *testing.T) {
	db := covMissionDB(t)
	covSeed(t, db)
	covMission(t, db, "m1", "IN_PROGRESS")
	mustExec(t, db, `INSERT INTO mission_tasks(id,mission_id,assigned_agent_id,title,status,assignment_id,depends_on) VALUES('first','m1','agent-worker','Work','IN_PROGRESS','run','[]')`)
	mustExec(t, db, `INSERT INTO mission_tasks(id,mission_id,title,status,depends_on) VALUES('next','m1','Next','BLOCKED','["first"]')`)
	e := newLifecycleEngine(t, db)
	// Failing the receipt must roll back the task, comment and activity too.
	mustExec(t, db, `CREATE TRIGGER refuse_completion BEFORE UPDATE OF completion_assignment_id ON mission_tasks BEGIN SELECT RAISE(ABORT,'receipt unavailable'); END`)
	if err := e.OnAssignmentCompleted(t.Context(), "run", "COMPLETED", "done", ""); err == nil {
		t.Fatal("receipt failure was acknowledged")
	}
	for _, query := range []string{`SELECT COUNT(*) FROM mission_comments`, `SELECT COUNT(*) FROM mission_activity`, `SELECT COUNT(*) FROM mission_tasks WHERE id='first' AND status!='IN_PROGRESS'`} {
		var n int
		if err := db.QueryRow(query).Scan(&n); err != nil || n != 0 {
			t.Fatalf("partial commit %s n=%d err=%v", query, n, err)
		}
	}
	mustExec(t, db, `DROP TRIGGER refuse_completion`)
	// Crash after persistent callback effects, before dependency projection.
	mustExec(t, db, `CREATE TRIGGER refuse_unblock BEFORE UPDATE OF status ON mission_tasks WHEN NEW.id='next' BEGIN SELECT RAISE(ABORT,'unblock unavailable'); END`)
	if err := e.OnAssignmentCompleted(t.Context(), "run", "COMPLETED", "done", ""); err == nil {
		t.Fatal("unblock failure was acknowledged")
	}
	mustExec(t, db, `DROP TRIGGER refuse_unblock`)
	for range 3 {
		if err := e.OnAssignmentCompleted(t.Context(), "run", "COMPLETED", "done", ""); err != nil {
			t.Fatal(err)
		}
	}
	for _, query := range []string{`SELECT COUNT(*) FROM mission_comments`, `SELECT COUNT(*) FROM mission_activity`} {
		var n int
		if err := db.QueryRow(query).Scan(&n); err != nil || n != 1 {
			t.Fatalf("duplicate effects %s n=%d err=%v", query, n, err)
		}
	}
	var status string
	if err := db.QueryRow(`SELECT status FROM mission_tasks WHERE id='next'`).Scan(&status); err != nil || status != "PENDING" {
		t.Fatalf("frontier status=%s err=%v", status, err)
	}
}

func TestMissionCompletionLoopBackRollsBackOnResetFailure(t *testing.T) {
	db := covMissionDB(t)
	covSeed(t, db)
	covMission(t, db, "m1", "IN_PROGRESS")
	mustExec(t, db, `INSERT INTO mission_tasks(id,mission_id,assigned_agent_id,title,status,depends_on,max_iterations,iteration) VALUES('up','m1','agent-worker','Develop','COMPLETED','[]',3,1)`)
	mustExec(t, db, `INSERT INTO mission_tasks(id,mission_id,assigned_agent_id,title,status,assignment_id,depends_on) VALUES('down','m1','agent-worker','Test','IN_PROGRESS','run','["up"]')`)
	e := newLifecycleEngine(t, db)
	e.lc = NewLoopController(db, nil, e.logger)
	mustExec(t, db, `CREATE TRIGGER refuse_loop_reset BEFORE UPDATE OF status ON mission_tasks WHEN NEW.id='down' AND NEW.status='BLOCKED' BEGIN SELECT RAISE(ABORT,'downstream reset unavailable'); END`)
	if err := e.OnAssignmentCompleted(t.Context(), "run", "FAILED", "", "failed"); err == nil {
		t.Fatal("partial loop reset acknowledged")
	}
	var status string
	var iteration int
	if err := db.QueryRow(`SELECT status,iteration FROM mission_tasks WHERE id='up'`).Scan(&status, &iteration); err != nil || status != "COMPLETED" || iteration != 1 {
		t.Fatalf("partial upstream reset status=%s iteration=%d err=%v", status, iteration, err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM mission_tasks WHERE completion_assignment_id='run'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("receipt committed n=%d err=%v", n, err)
	}
	mustExec(t, db, `DROP TRIGGER refuse_loop_reset`)
	if err := e.OnAssignmentCompleted(t.Context(), "run", "FAILED", "", "failed"); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT status,iteration FROM mission_tasks WHERE id='up'`).Scan(&status, &iteration); err != nil || status != "PENDING" || iteration != 2 {
		t.Fatalf("retry status=%s iteration=%d err=%v", status, iteration, err)
	}
}
