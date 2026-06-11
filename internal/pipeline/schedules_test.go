package pipeline

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// scheduleSchemaSQL adds the pipeline_schedules table on top of
// schemaSQL. Kept inline (rather than reusing the database package)
// for the same reason store_test does it: keeps the test fast and
// dependency-free.
const scheduleSchemaSQL = `
CREATE TABLE IF NOT EXISTS pipeline_schedules (
    id                       TEXT PRIMARY KEY,
    workspace_id             TEXT NOT NULL,
    name                     TEXT NOT NULL,
    target_pipeline_id       TEXT NOT NULL,
    target_pipeline_version  INTEGER,
    cron_expr                TEXT NOT NULL,
    timezone                 TEXT NOT NULL DEFAULT 'UTC',
    inputs_json              TEXT NOT NULL DEFAULT '{}',
    enabled                  INTEGER NOT NULL DEFAULT 1,
    last_run_at              TEXT,
    last_status              TEXT,
    last_run_id              TEXT,
    next_run_at              TEXT,
    created_at               TEXT NOT NULL DEFAULT (datetime('now','subsec')),
    updated_at               TEXT NOT NULL DEFAULT (datetime('now','subsec')),
    deleted_at               TEXT,
    wake_pipeline_id         TEXT,
    wake_inputs_json         TEXT NOT NULL DEFAULT '{}',
    wake_check_count         INTEGER NOT NULL DEFAULT 0,
    wake_fire_count          INTEGER NOT NULL DEFAULT 0,
    last_wake_at             TEXT,
    last_wake_status         TEXT
);
`

func openScheduleTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db := openStoreTestDB(t)
	if _, err := db.ExecContext(context.Background(), scheduleSchemaSQL); err != nil {
		t.Fatalf("schedule schema: %v", err)
	}
	return db
}

// seedPipeline inserts a pipeline row directly so schedule tests
// have a real target_pipeline_id to bind to. Avoids the test-run
// gate dance the proper Save flow requires.
func seedPipeline(t *testing.T, db *sql.DB, id, slug string) {
	t.Helper()
	_, err := db.ExecContext(context.Background(),
		`INSERT INTO pipelines (id, workspace_id, slug, name, definition_json, definition_hash)
		 VALUES (?, 'ws_test', ?, ?, '{"name":"x","steps":[]}', 'hash')`,
		id, slug, slug)
	if err != nil {
		t.Fatalf("seed pipeline: %v", err)
	}
}

func TestScheduleStore_Save_Create(t *testing.T) {
	db := openScheduleTestDB(t)
	defer db.Close()
	seedPipeline(t, db, "pipe_1", "daily-digest")
	store := NewScheduleStore(db)

	out, err := store.Save(context.Background(), SaveScheduleInput{
		WorkspaceID:      "ws_test",
		Name:             "Daily 8 AM",
		TargetPipelineID: "pipe_1",
		CronExpr:         "0 8 * * *",
		Timezone:         "UTC",
		Inputs:           map[string]any{"since": "yesterday"},
		Enabled:          true,
	})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if !strings.HasPrefix(out.ID, "psched_") {
		t.Errorf("expected psched_ prefix, got %s", out.ID)
	}
	if out.NextRunAt == nil || out.NextRunAt.Before(time.Now()) {
		t.Errorf("expected next_run_at in future, got %v", out.NextRunAt)
	}
	if !out.Enabled {
		t.Errorf("expected enabled")
	}
}

func TestScheduleStore_Save_RejectsBadCron(t *testing.T) {
	db := openScheduleTestDB(t)
	defer db.Close()
	seedPipeline(t, db, "pipe_1", "x")
	store := NewScheduleStore(db)

	_, err := store.Save(context.Background(), SaveScheduleInput{
		WorkspaceID:      "ws_test",
		Name:             "broken",
		TargetPipelineID: "pipe_1",
		CronExpr:         "not-a-cron-expr",
	})
	if err == nil || !strings.Contains(err.Error(), "invalid cron") {
		t.Errorf("expected cron parse error, got %v", err)
	}
}

func TestScheduleStore_Save_RejectsBadTimezone(t *testing.T) {
	db := openScheduleTestDB(t)
	defer db.Close()
	seedPipeline(t, db, "pipe_1", "x")
	store := NewScheduleStore(db)

	_, err := store.Save(context.Background(), SaveScheduleInput{
		WorkspaceID:      "ws_test",
		Name:             "broken",
		TargetPipelineID: "pipe_1",
		CronExpr:         "0 8 * * *",
		Timezone:         "Atlantis/Lost",
	})
	if err == nil || !strings.Contains(err.Error(), "invalid timezone") {
		t.Errorf("expected timezone error, got %v", err)
	}
}

func TestScheduleStore_Save_Update_PreservesID(t *testing.T) {
	db := openScheduleTestDB(t)
	defer db.Close()
	seedPipeline(t, db, "pipe_1", "x")
	store := NewScheduleStore(db)
	ctx := context.Background()

	created, err := store.Save(ctx, SaveScheduleInput{
		WorkspaceID:      "ws_test",
		Name:             "v1",
		TargetPipelineID: "pipe_1",
		CronExpr:         "0 8 * * *",
		Enabled:          true,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	updated, err := store.Save(ctx, SaveScheduleInput{
		ID:               created.ID,
		WorkspaceID:      "ws_test",
		Name:             "v2 renamed",
		TargetPipelineID: "pipe_1",
		CronExpr:         "0 9 * * *",
		Enabled:          false,
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.ID != created.ID {
		t.Errorf("expected ID preserved, got %s vs %s", updated.ID, created.ID)
	}
	if updated.Name != "v2 renamed" {
		t.Errorf("expected name updated, got %s", updated.Name)
	}
	if updated.Enabled {
		t.Errorf("expected enabled=false after disable")
	}
	if updated.CronExpr != "0 9 * * *" {
		t.Errorf("expected cron updated")
	}
}

func TestScheduleStore_List_OrdersByNextRun(t *testing.T) {
	db := openScheduleTestDB(t)
	defer db.Close()
	seedPipeline(t, db, "pipe_1", "x")
	store := NewScheduleStore(db)
	ctx := context.Background()

	// Two schedules with different cadences — daily fires far less
	// often than every-minute, so every-minute should come first.
	_, err := store.Save(ctx, SaveScheduleInput{
		WorkspaceID: "ws_test", Name: "daily",
		TargetPipelineID: "pipe_1", CronExpr: "0 0 * * *", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Save(ctx, SaveScheduleInput{
		WorkspaceID: "ws_test", Name: "every-minute",
		TargetPipelineID: "pipe_1", CronExpr: "* * * * *", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	got, err := store.List(ctx, "ws_test")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2, got %d", len(got))
	}
	if got[0].Name != "every-minute" {
		t.Errorf("expected every-minute first, got %s", got[0].Name)
	}
}

func TestScheduleStore_SoftDelete(t *testing.T) {
	db := openScheduleTestDB(t)
	defer db.Close()
	seedPipeline(t, db, "pipe_1", "x")
	store := NewScheduleStore(db)
	ctx := context.Background()

	created, _ := store.Save(ctx, SaveScheduleInput{
		WorkspaceID: "ws_test", Name: "x",
		TargetPipelineID: "pipe_1", CronExpr: "0 8 * * *", Enabled: true,
	})
	if err := store.SoftDelete(ctx, created.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := store.GetByID(ctx, created.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound after delete, got %v", err)
	}
	rows, _ := store.List(ctx, "ws_test")
	if len(rows) != 0 {
		t.Errorf("expected list to skip deleted, got %d", len(rows))
	}
}

func TestScheduleStore_listDueSchedules(t *testing.T) {
	db := openScheduleTestDB(t)
	defer db.Close()
	seedPipeline(t, db, "pipe_1", "x")
	store := NewScheduleStore(db)
	ctx := context.Background()

	// Manually insert a row whose next_run_at is in the past so
	// listDueSchedules picks it up. Going through Save would compute
	// next_run_at from "now", which is always in the future.
	pastID := "psched_past_test"
	pastTime := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := db.ExecContext(ctx, `
INSERT INTO pipeline_schedules
  (id, workspace_id, name, target_pipeline_id, cron_expr, timezone, inputs_json, enabled, next_run_at, created_at, updated_at)
VALUES (?, 'ws_test', 'past-due', 'pipe_1', '0 8 * * *', 'UTC', '{}', 1, ?, ?, ?)`,
		pastID, pastTime, now, now)
	if err != nil {
		t.Fatalf("seed past schedule: %v", err)
	}
	// And one in the future — shouldn't be returned.
	_, err = store.Save(ctx, SaveScheduleInput{
		WorkspaceID: "ws_test", Name: "future",
		TargetPipelineID: "pipe_1", CronExpr: "0 0 1 1 *", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	due, err := store.listDueSchedules(ctx)
	if err != nil {
		t.Fatalf("listDue: %v", err)
	}
	if len(due) != 1 {
		t.Fatalf("expected 1 due, got %d", len(due))
	}
	if due[0].ID != pastID {
		t.Errorf("expected past-due, got %s", due[0].ID)
	}
}

func TestScheduleStore_listDueSchedules_SkipsDisabled(t *testing.T) {
	db := openScheduleTestDB(t)
	defer db.Close()
	seedPipeline(t, db, "pipe_1", "x")
	store := NewScheduleStore(db)
	ctx := context.Background()

	pastTime := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := db.ExecContext(ctx, `
INSERT INTO pipeline_schedules
  (id, workspace_id, name, target_pipeline_id, cron_expr, timezone, inputs_json, enabled, next_run_at, created_at, updated_at)
VALUES ('psched_disabled', 'ws_test', 'disabled', 'pipe_1', '0 8 * * *', 'UTC', '{}', 0, ?, ?, ?)`,
		pastTime, now, now)
	if err != nil {
		t.Fatalf("seed disabled: %v", err)
	}

	due, err := store.listDueSchedules(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 0 {
		t.Errorf("expected 0 due (disabled rows skipped), got %d", len(due))
	}
}

// stubAgentRunner counts RunStep calls; unlike orchestrator-based
// runs we don't need a real CLI for the scheduler test. We're
// verifying scheduler -> executor wiring, not the executor itself.
type stubAgentRunner struct {
	calls atomic.Int64
}

func (r *stubAgentRunner) RunStep(ctx context.Context, req AgentStepRequest) (AgentStepResult, error) {
	r.calls.Add(1)
	return AgentStepResult{
		Output:     "ok",
		DurationMs: 10,
		CostUSD:    0,
	}, nil
}

func TestPipelineScheduler_Tick_FiresDueSchedule(t *testing.T) {
	db := openScheduleTestDB(t)
	defer db.Close()
	seedPipeline(t, db, "pipe_1", "x")
	// Replace the seeded definition with a one-step pipeline so the
	// executor has work to do.
	_, err := db.ExecContext(context.Background(),
		`UPDATE pipelines SET definition_json = ?, last_test_run_at = ?, last_test_run_passed = 1 WHERE id = 'pipe_1'`,
		`{"name":"x","steps":[{"id":"s1","type":"agent_run","agent":"agent_lead","prompt":"hi"}]}`,
		time.Now().UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		t.Fatalf("update pipeline def: %v", err)
	}

	scheduleStore := NewScheduleStore(db)
	pipelineStore := NewStore(db)
	resolver := NewResolver(db)
	runner := &stubAgentRunner{}
	exec := NewExecutor(pipelineStore, resolver, runner, nil)

	// Seed a past-due schedule
	pastTime := time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = db.ExecContext(context.Background(), `
INSERT INTO pipeline_schedules
  (id, workspace_id, name, target_pipeline_id, cron_expr, timezone, inputs_json, enabled, next_run_at, created_at, updated_at)
VALUES ('psched_due', 'ws_test', 'due', 'pipe_1', '0 8 * * *', 'UTC', '{}', 1, ?, ?, ?)`,
		pastTime, now, now)
	if err != nil {
		t.Fatalf("seed due: %v", err)
	}

	scheduler := NewPipelineScheduler(scheduleStore, pipelineStore, exec, nil)
	scheduler.tick(context.Background())

	if runner.calls.Load() != 1 {
		t.Errorf("expected 1 agent run from scheduler tick, got %d", runner.calls.Load())
	}

	// next_run_at should have been advanced past now
	got, err := scheduleStore.GetByID(context.Background(), "psched_due")
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got.NextRunAt == nil || !got.NextRunAt.After(time.Now()) {
		t.Errorf("expected next_run_at advanced past now, got %v", got.NextRunAt)
	}
	if got.LastStatus != "COMPLETED" {
		t.Errorf("expected last_status COMPLETED, got %q", got.LastStatus)
	}
}

func TestPipelineScheduler_Tick_DisablesScheduleOnBadCron(t *testing.T) {
	db := openScheduleTestDB(t)
	defer db.Close()
	seedPipeline(t, db, "pipe_1", "x")
	scheduleStore := NewScheduleStore(db)
	pipelineStore := NewStore(db)
	exec := NewExecutor(pipelineStore, NewResolver(db), &stubAgentRunner{}, nil)

	// Bypass Save's validation by inserting a bad cron directly —
	// simulates a row whose cron expr was once valid but the parser
	// stopped accepting it (e.g. after a robfig/cron upgrade).
	pastTime := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := db.ExecContext(context.Background(), `
INSERT INTO pipeline_schedules
  (id, workspace_id, name, target_pipeline_id, cron_expr, timezone, inputs_json, enabled, next_run_at, created_at, updated_at)
VALUES ('psched_bad', 'ws_test', 'bad', 'pipe_1', 'gibberish', 'UTC', '{}', 1, ?, ?, ?)`,
		pastTime, now, now)
	if err != nil {
		t.Fatal(err)
	}

	scheduler := NewPipelineScheduler(scheduleStore, pipelineStore, exec, nil)
	scheduler.tick(context.Background())

	// Schedule must have been disabled to prevent infinite loops
	var enabled int
	if err := db.QueryRow(`SELECT enabled FROM pipeline_schedules WHERE id = 'psched_bad'`).Scan(&enabled); err != nil {
		t.Fatal(err)
	}
	if enabled != 0 {
		t.Errorf("expected bad-cron schedule disabled, still enabled")
	}
}
