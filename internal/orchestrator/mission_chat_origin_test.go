package orchestrator

import (
	"io"
	"log/slog"
	"testing"
	"time"
)

func TestMissionTaskAssignmentCopiesDurableChatOrigin(t *testing.T) {
	db := setupTestDB(t)
	workspace, crew, lead, agent := seedTestData(t, db)
	mission := createTestMission(t, db, workspace, crew, lead)
	mustExec(t, db, `UPDATE missions SET chat_origin_id='persisted-mission-origin' WHERE id=?`, mission)
	stamp := time.Now().UTC().Format(time.RFC3339)
	mustExec(t, db, `INSERT INTO mission_tasks(id,mission_id,assigned_agent_id,title,status,task_order,depends_on,created_at,updated_at) VALUES('origin-task',?,?,'Task','PENDING',1,'[]',?,?)`, mission, agent, stamp, stamp)
	engine := NewMissionEngine(db, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	engine.SetDispatcher(&mockDispatcher{})
	state := &missionState{ID: mission, WorkspaceID: workspace, CrewID: crew, CrewSlug: "dev-crew", LeadAgentID: lead, TraceID: "origin-trace"}
	task := TaskInfo{ID: "origin-task", MissionID: mission, AssignedAgentID: &agent, Title: "Task", Status: "PENDING"}
	if err := engine.scheduleTask(t.Context(), state, task, nil); err != nil {
		t.Fatal(err)
	}
	var origin string
	if err := db.QueryRow(`SELECT COALESCE(a.chat_origin_id,'') FROM assignments a JOIN mission_tasks t ON t.assignment_id=a.id WHERE t.id='origin-task'`).Scan(&origin); err != nil {
		t.Fatal(err)
	}
	if origin != "persisted-mission-origin" {
		t.Fatalf("task assignment lost origin %q", origin)
	}
}

func TestMissionPlanningAssignmentCopiesDurableChatOrigin(t *testing.T) {
	db := setupTestDB(t)
	workspace, crew, lead, _ := seedTestData(t, db)
	mission := createTestMission(t, db, workspace, crew, lead)
	mustExec(t, db, `UPDATE missions SET chat_origin_id='persisted-planning-origin' WHERE id=?`, mission)
	engine := NewMissionEngine(db, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	engine.SetDispatcher(&mockDispatcher{})
	state := &missionState{ID: mission, WorkspaceID: workspace, CrewID: crew, CrewSlug: "dev-crew", LeadAgentID: lead, TraceID: "planning-origin-trace"}
	if err := engine.dispatchLeadPlanning(t.Context(), state); err != nil {
		t.Fatal(err)
	}
	var origin string
	if err := db.QueryRow(`SELECT COALESCE(chat_origin_id,'') FROM assignments WHERE mission_id=? AND lead_planning=1`, mission).Scan(&origin); err != nil {
		t.Fatal(err)
	}
	if origin != "persisted-planning-origin" {
		t.Fatalf("planning assignment lost origin %q", origin)
	}
}
