package orchestrator

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"testing"

	_ "modernc.org/sqlite"
)

// BenchmarkBuildMissionBrief mirrors the scheduleTask hot path — the
// orchestrator invokes buildMissionBrief once per task scheduled. With
// 30 tasks per mission the DAG overview loop runs 30×, so per-iteration
// allocation count is the bottleneck we want to squeeze.
func BenchmarkBuildMissionBrief(b *testing.B) {
	const nTasks = 30
	const nDeps = 6

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		b.Fatalf("open db: %v", err)
	}
	b.Cleanup(func() { db.Close() })

	schema := `
		CREATE TABLE missions (id TEXT PRIMARY KEY, title TEXT, description TEXT);
		CREATE TABLE mission_comments (id TEXT PRIMARY KEY, mission_id TEXT,
			author_type TEXT, author_id TEXT, body TEXT, created_at TEXT);
		CREATE TABLE agents (id TEXT, name TEXT);
		CREATE TABLE users (id TEXT, name TEXT, email TEXT);
	`
	if _, err := db.Exec(schema); err != nil {
		b.Fatalf("schema: %v", err)
	}

	missionID := "mission-bench"
	if _, err := db.Exec(`INSERT INTO missions (id, title, description) VALUES (?, 'Bench', 'Benchmark mission')`, missionID); err != nil {
		b.Fatalf("seed mission: %v", err)
	}

	summary := "Completed the work. Produced a clean artifact ready for downstream consumption."
	slug := "anna"

	allTasks := make([]TaskInfo, 0, nTasks)
	for i := 0; i < nTasks; i++ {
		id := fmt.Sprintf("t_%d", i)
		status := "COMPLETED"
		if i >= nTasks-5 {
			status = "PENDING"
		}
		resSum := summary
		allTasks = append(allTasks, TaskInfo{
			ID:            id,
			MissionID:     missionID,
			AgentSlug:     &slug,
			Title:         fmt.Sprintf("Task %d — do the thing", i),
			Status:        status,
			TaskOrder:     i + 1,
			DependsOn:     "[]",
			Iteration:     1,
			ResultSummary: &resSum,
		})
	}

	depIDs := make([]string, 0, nDeps)
	for i := 0; i < nDeps; i++ {
		depIDs = append(depIDs, fmt.Sprintf("t_%d", i))
	}
	depsJSON, _ := json.Marshal(depIDs)
	targetTask := TaskInfo{
		ID:        "t_target",
		MissionID: missionID,
		AgentSlug: &slug,
		Title:     "Target task",
		Status:    "PENDING",
		TaskOrder: nTasks + 1,
		DependsOn: string(depsJSON),
		Iteration: 1,
	}
	allTasks = append(allTasks, targetTask)

	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
	engine := NewMissionEngine(db, nil, nil, logger)
	ms := &missionState{
		ID:          missionID,
		CrewID:      "crew-b",
		CrewSlug:    "bench-crew",
		WorkspaceID: "ws-b",
		TraceID:     "trace-b",
		cancel:      func() {},
	}

	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = engine.buildMissionBrief(ctx, ms, targetTask, allTasks)
	}
}
