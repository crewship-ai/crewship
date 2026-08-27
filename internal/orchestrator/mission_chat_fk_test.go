package orchestrator

// mission_chat_fk_test.go pins the fix for a mission-creating door that left
// missions with no `chats` row: api/missions_internal.go's
// InternalMissionHandler.Create — the sidecar's own "an agent plans its own
// mission" endpoint — inserted the `missions` row (and, when the request
// carried tasks, the `mission_tasks` rows) but never the synthetic `chats`
// row that `assignments.chat_id TEXT NOT NULL REFERENCES chats(id)` requires.
// Create itself reported success; the failure only showed up when this
// engine tried to schedule the mission's first task
// (`INSERT INTO assignments ... chat_id = <mission id>`), which hit the FK
// on every attempt, and scheduleReadyTasks swallows that into a FAILED task
// rather than surfacing it — so the operator-visible symptom was a mission
// stuck with every task FAILED for no reason discoverable from the task
// list. Same root cause as #2128 (internal/cartographer/fork.go), a
// different door.
//
// Deliberately built on testutil.MigratedSQLDB — the REAL migration chain —
// rather than this package's own setupTestDB/covMissionDB fixtures (see
// mission_test.go, mission_cov_test.go). Those hand-roll
// `chats (id TEXT PRIMARY KEY, agent_id TEXT, workspace_id TEXT)` with no
// foreign key at all, so the constraint this bug violates can never fire
// against them — which is exactly why this package's large existing
// scheduleTask/dispatchLeadPlanning suite (TestMissionAssignmentRowsCarryDepthZero
// and friends) never caught it.
import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/testutil"
	_ "modernc.org/sqlite"
)

func fkMissionExec(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.ExecContext(t.Context(), q, args...); err != nil {
		t.Fatalf("exec %.80s: %v", q, err)
	}
}

// fkMissionSeed inserts one workspace/crew/lead/worker against the real
// schema, which — unlike the package's hand fixtures — requires
// name+slug NOT NULL on workspaces/crews/agents.
func fkMissionSeed(t *testing.T, db *sql.DB) (wsID, crewID, leadID, workerID string) {
	t.Helper()
	wsID, crewID, leadID, workerID = "ws-fk", "crew-fk", "agent-lead-fk", "agent-worker-fk"
	fkMissionExec(t, db, `INSERT INTO workspaces (id, name, slug) VALUES (?, 'WS', 'ws-fk')`, wsID)
	fkMissionExec(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES (?, ?, 'Crew', 'crew-fk')`, crewID, wsID)
	fkMissionExec(t, db, `INSERT INTO agents (id, workspace_id, crew_id, name, slug, agent_role) VALUES (?, ?, ?, 'Anna', 'anna-fk', 'LEAD')`, leadID, wsID, crewID)
	fkMissionExec(t, db, `INSERT INTO agents (id, workspace_id, crew_id, name, slug, agent_role) VALUES (?, ?, ?, 'Bob', 'bob-fk', 'AGENT')`, workerID, wsID, crewID)
	return
}

// fkMission inserts a mission row the way
// api/missions_internal.go's InternalMissionHandler.Create does —
// deliberately WITHOUT the synthetic chats row the other mission-creating
// doors stamp. That omission is the bug's precondition, reproduced
// faithfully against the real schema rather than asserted by comment.
func fkMission(t *testing.T, db *sql.DB, id, wsID, crewID, leadID, status string) *missionState {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	fkMissionExec(t, db, `INSERT INTO missions
		(id, workspace_id, crew_id, lead_agent_id, trace_id, title, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, 'FK Mission', ?, ?, ?)`,
		id, wsID, crewID, leadID, "trace-"+id, status, now, now)
	return &missionState{
		ID: id, Title: "FK Mission", CrewID: crewID, CrewSlug: "crew-fk",
		LeadAgentID: leadID, TraceID: "trace-" + id, WorkspaceID: wsID,
		cancel: func() {},
	}
}

func fkEngine(t *testing.T, db *sql.DB) *MissionEngine {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	return NewMissionEngine(db, nil, nil, logger)
}

// TestScheduleTask_MissionWithNoChatRow_DispatchesInsteadOfFKFailure is the
// regression test for mission_tasks.go:502's assignment insert (before this
// fix, unconditional on chat_id = mission id).
//
// Fails on main with:
//
//	scheduleTask: create assignment: FOREIGN KEY constraint failed
func TestScheduleTask_MissionWithNoChatRow_DispatchesInsteadOfFKFailure(t *testing.T) {
	db := testutil.MigratedSQLDB(t)
	wsID, crewID, leadID, workerID := fkMissionSeed(t, db)
	ms := fkMission(t, db, "m-fk-1", wsID, crewID, leadID, "IN_PROGRESS")

	now := time.Now().UTC().Format(time.RFC3339)
	fkMissionExec(t, db, `INSERT INTO mission_tasks
		(id, mission_id, assigned_agent_id, title, status, task_order, depends_on, created_at, updated_at)
		VALUES ('t-fk-1', ?, ?, 'Do the thing', 'PENDING', 1, '[]', ?, ?)`,
		ms.ID, workerID, now, now)

	// Precondition: exactly the state missions_internal.go's Create leaves —
	// a mission with no chats row at its id.
	var exists int
	if err := db.QueryRowContext(t.Context(), `SELECT 1 FROM chats WHERE id = ?`, ms.ID).Scan(&exists); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("precondition: mission %s must have no chats row yet, got err=%v", ms.ID, err)
	}

	e := fkEngine(t, db)
	task := TaskInfo{ID: "t-fk-1", MissionID: ms.ID, AssignedAgentID: &workerID, Title: "Do the thing", Status: "PENDING"}
	if err := e.scheduleTask(context.Background(), ms, task, nil); err != nil {
		t.Fatalf("scheduleTask: %v — a mission-creating door with no chats row must not break dispatch", err)
	}

	var status string
	var errMsg sql.NullString
	if err := db.QueryRowContext(t.Context(), `SELECT status, error_message FROM mission_tasks WHERE id = ?`, "t-fk-1").
		Scan(&status, &errMsg); err != nil {
		t.Fatalf("read task: %v", err)
	}
	if status != "IN_PROGRESS" {
		t.Errorf("task status = %q (error_message=%q), want IN_PROGRESS — on main this ends FAILED "+
			"with a FOREIGN KEY constraint failed error because missions_internal.go's Create "+
			"never inserts a chats row", status, errMsg.String)
	}

	var chatCount int
	if err := db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM chats WHERE id = ?`, ms.ID).Scan(&chatCount); err != nil {
		t.Fatalf("count chats: %v", err)
	}
	if chatCount != 1 {
		t.Errorf("chats rows for mission %s = %d, want 1 — scheduleTask must create the row it depends on", ms.ID, chatCount)
	}
}

// TestDispatchLeadPlanning_MissionWithNoChatRow_DispatchesInsteadOfFKFailure
// is the same gap at the other insert site: mission_tasks_planning.go:247's
// planning-assignment insert, hit when an agent-created mission has zero
// tasks and the engine dispatches the lead for autonomous planning.
func TestDispatchLeadPlanning_MissionWithNoChatRow_DispatchesInsteadOfFKFailure(t *testing.T) {
	db := testutil.MigratedSQLDB(t)
	wsID, crewID, leadID, _ := fkMissionSeed(t, db)
	ms := fkMission(t, db, "m-fk-2", wsID, crewID, leadID, "IN_PROGRESS")

	e := fkEngine(t, db)
	if err := e.dispatchLeadPlanning(context.Background(), ms); err != nil {
		t.Fatalf("dispatchLeadPlanning: %v — a mission-creating door with no chats row must not break dispatch", err)
	}

	var n int
	if err := db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM assignments WHERE group_id = ?`, ms.ID).Scan(&n); err != nil {
		t.Fatalf("count assignments: %v", err)
	}
	if n != 1 {
		t.Errorf("planning assignments for %s = %d, want 1", ms.ID, n)
	}

	var chatCount int
	if err := db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM chats WHERE id = ?`, ms.ID).Scan(&chatCount); err != nil {
		t.Fatalf("count chats: %v", err)
	}
	if chatCount != 1 {
		t.Errorf("chats rows for mission %s = %d, want 1 — dispatchLeadPlanning must create the row it depends on", ms.ID, chatCount)
	}
}
