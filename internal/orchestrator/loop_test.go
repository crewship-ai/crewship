package orchestrator

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestLoopController_ShouldRetry(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, leadID, agentID := seedTestData(t, db)
	missionID := createTestMission(t, db, wsID, crewID, leadID)

	now := time.Now().UTC().Format(time.RFC3339)
	// Task with max_iterations=3, currently at iteration 1
	db.Exec(`INSERT INTO mission_tasks (id, mission_id, assigned_agent_id, title, status, task_order, depends_on, iteration, max_iterations, created_at, updated_at)
		VALUES ('t1', ?, ?, 'Develop feature', 'FAILED', 1, '[]', 1, 3, ?, ?)`, missionID, agentID, now, now)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	lc := NewLoopController(db, NewProgressWriter(), logger)

	retried, err := lc.ShouldRetry(context.Background(), "t1", missionID)
	if err != nil {
		t.Fatalf("ShouldRetry: %v", err)
	}
	if !retried {
		t.Fatal("expected retry to be initiated")
	}

	// Check that task was reset to PENDING with iteration=2
	var status string
	var iteration int
	db.QueryRow(`SELECT status, iteration FROM mission_tasks WHERE id = 't1'`).Scan(&status, &iteration)
	if status != "PENDING" {
		t.Errorf("status = %s, want PENDING", status)
	}
	if iteration != 2 {
		t.Errorf("iteration = %d, want 2", iteration)
	}
}

func TestLoopController_ShouldRetry_Exhausted(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, leadID, agentID := seedTestData(t, db)
	missionID := createTestMission(t, db, wsID, crewID, leadID)

	now := time.Now().UTC().Format(time.RFC3339)
	// Task at max iterations already
	db.Exec(`INSERT INTO mission_tasks (id, mission_id, assigned_agent_id, title, status, task_order, depends_on, iteration, max_iterations, created_at, updated_at)
		VALUES ('t1', ?, ?, 'Develop feature', 'FAILED', 1, '[]', 3, 3, ?, ?)`, missionID, agentID, now, now)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	lc := NewLoopController(db, NewProgressWriter(), logger)

	retried, err := lc.ShouldRetry(context.Background(), "t1", missionID)
	if err != nil {
		t.Fatalf("ShouldRetry: %v", err)
	}
	if retried {
		t.Fatal("expected no retry when exhausted")
	}
}

func TestLoopController_ShouldRetry_NoMaxIterations(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, leadID, agentID := seedTestData(t, db)
	missionID := createTestMission(t, db, wsID, crewID, leadID)

	now := time.Now().UTC().Format(time.RFC3339)
	// Task without max_iterations
	db.Exec(`INSERT INTO mission_tasks (id, mission_id, assigned_agent_id, title, status, task_order, depends_on, iteration, created_at, updated_at)
		VALUES ('t1', ?, ?, 'One-shot task', 'FAILED', 1, '[]', 1, ?, ?)`, missionID, agentID, now, now)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	lc := NewLoopController(db, NewProgressWriter(), logger)

	retried, err := lc.ShouldRetry(context.Background(), "t1", missionID)
	if err != nil {
		t.Fatalf("ShouldRetry: %v", err)
	}
	if retried {
		t.Fatal("expected no retry without max_iterations")
	}
}

func TestLoopController_RetryLoopBack(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, leadID, agentID := seedTestData(t, db)
	missionID := createTestMission(t, db, wsID, crewID, leadID)

	now := time.Now().UTC().Format(time.RFC3339)
	// dev-test-loop: develop (max 3 iter) -> test (depends on develop)
	db.Exec(`INSERT INTO mission_tasks (id, mission_id, assigned_agent_id, title, status, task_order, depends_on, iteration, max_iterations, created_at, updated_at)
		VALUES ('develop', ?, ?, 'Implement', 'COMPLETED', 1, '[]', 1, 3, ?, ?)`, missionID, agentID, now, now)
	db.Exec(`INSERT INTO mission_tasks (id, mission_id, assigned_agent_id, title, status, task_order, depends_on, iteration, max_iterations, created_at, updated_at)
		VALUES ('test', ?, ?, 'Test', 'FAILED', 2, '["develop"]', 1, NULL, ?, ?)`, missionID, agentID, now, now)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	lc := NewLoopController(db, NewProgressWriter(), logger)

	retried, err := lc.RetryLoopBack(context.Background(), "test", missionID)
	if err != nil {
		t.Fatalf("RetryLoopBack: %v", err)
	}
	if !retried {
		t.Fatal("expected loop-back retry")
	}

	// develop should be reset to PENDING with iteration=2
	var devStatus string
	var devIter int
	db.QueryRow(`SELECT status, iteration FROM mission_tasks WHERE id = 'develop'`).Scan(&devStatus, &devIter)
	if devStatus != "PENDING" {
		t.Errorf("develop status = %s, want PENDING", devStatus)
	}
	if devIter != 2 {
		t.Errorf("develop iteration = %d, want 2", devIter)
	}

	// test should be BLOCKED (waiting for develop to complete again)
	var testStatus string
	db.QueryRow(`SELECT status FROM mission_tasks WHERE id = 'test'`).Scan(&testStatus)
	if testStatus != "BLOCKED" {
		t.Errorf("test status = %s, want BLOCKED", testStatus)
	}
}
