package orchestrator

import (
	"context"
	"testing"
	"time"
)

func TestScheduleTask_AcceptanceRequiresCurrentMissionAndTask(t *testing.T) {
	for _, scenario := range []struct{ name, mission, task string }{
		{"stop", "CANCELLED", "CANCELLED"}, {"handoff", "TODO", "PENDING"}, {"pause", "PAUSED", "PENDING"}, {"completed", "COMPLETED", "PENDING"}, {"cancelled-task", "IN_PROGRESS", "CANCELLED"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			db := covMissionDB(t)
			_, _, _, worker := covSeed(t, db)
			ms := covMission(t, db, "mission-admission", "IN_PROGRESS")
			e := newLifecycleEngine(t, db)
			disp := newHoldDispatcher(nil)
			e.SetDispatcher(disp)
			now := time.Now().UTC().Format(time.RFC3339)
			mustExec(t, db, `INSERT INTO mission_tasks(id,mission_id,assigned_agent_id,title,status,task_order,depends_on,created_at,updated_at) VALUES('task-admission',?,?,'Task','PENDING',1,'[]',?,?)`, ms.ID, worker, now, now)
			// The tick already read PENDING/IN_PROGRESS. Stop or handoff commits
			// before its acceptance transaction; stale in-memory data is no authority.
			mustExec(t, db, `UPDATE missions SET status=? WHERE id=?`, scenario.mission, ms.ID)
			mustExec(t, db, `UPDATE mission_tasks SET status=? WHERE id='task-admission'`, scenario.task)
			if err := e.scheduleTask(context.Background(), ms, TaskInfo{ID: "task-admission", MissionID: ms.ID, AssignedAgentID: &worker, Title: "Task", Status: "PENDING"}, nil); err != nil {
				t.Fatal(err)
			}
			for _, q := range []string{`SELECT COUNT(*) FROM assignments`, `SELECT COUNT(*) FROM work_items`} {
				var n int
				if err := db.QueryRow(q).Scan(&n); err != nil || n != 0 {
					t.Fatalf("unauthorized admission: count=%d err=%v", n, err)
				}
			}
			if got := covTaskStatus(t, db, "task-admission"); got != scenario.task {
				t.Fatalf("task resurrected: %s", got)
			}
			if len(disp.snapshot()) != 0 {
				t.Fatal("stale tick woke work")
			}
		})
	}
}

func TestScheduleTask_AcceptanceFailureRollsBackTaskClaim(t *testing.T) {
	db := covMissionDB(t)
	_, _, _, worker := covSeed(t, db)
	ms := covMission(t, db, "mission-rollback", "IN_PROGRESS")
	e := newLifecycleEngine(t, db)
	now := time.Now().UTC().Format(time.RFC3339)
	mustExec(t, db, `INSERT INTO mission_tasks(id,mission_id,assigned_agent_id,title,status,task_order,depends_on,created_at,updated_at) VALUES('task-rollback',?,?,'Task','PENDING',1,'[]',?,?)`, ms.ID, worker, now, now)
	mustExec(t, db, `CREATE TRIGGER refuse_task_work BEFORE INSERT ON work_items BEGIN SELECT RAISE(ABORT,'injected task admission failure'); END`)
	if err := e.scheduleTask(context.Background(), ms, TaskInfo{ID: "task-rollback", MissionID: ms.ID, AssignedAgentID: &worker, Title: "Task", Status: "PENDING"}, nil); err == nil {
		t.Fatal("failed work insert accepted")
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM mission_tasks WHERE id='task-rollback' AND status='PENDING' AND assignment_id IS NULL AND started_at IS NULL`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("task claim leaked: n=%d err=%v", n, err)
	}
	if got := holdCountAssignments(t, db); got != 0 {
		t.Fatalf("orphan assignments=%d", got)
	}
}
