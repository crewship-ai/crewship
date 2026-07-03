package pipeline

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"
)

// TestScheduledFireIsIdempotent pins exactly-once semantics on the cron path.
// The executor already dedupes on IdempotencyKey, but the scheduler never set
// one — so a duplicate tick within the same minute, or a process restart before
// next_run_at is advanced, re-fires the SAME occurrence and produces a second
// run. Firing the same occurrence (same schedule ID + same NextRunAt) twice must
// yield exactly one run. On current main this is 2 → RED.
func TestScheduledFireIsIdempotent(t *testing.T) {
	db := openFactoryTestDB(t)
	defer db.Close()
	ctx := context.Background()

	runner := newMockRunner()
	runner.outputsBySlug["agent_lead"] = []string{"ok"}
	deps := fullExecutorDeps(t, db, runner)
	exec := NewWiredExecutor(deps)
	store := deps.Store
	p := saveResumePipeline(t, store, "cron-idem", agentStepDef)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	scheduler := NewPipelineScheduler(NewScheduleStore(db), store, exec, logger)

	occ := time.Date(2026, 1, 1, 8, 0, 0, 0, time.UTC)
	mk := func() *Schedule {
		o := occ // fresh pointer per call; same value = same occurrence identity
		return &Schedule{
			ID:               "psched_idem",
			WorkspaceID:      "ws_test",
			TargetPipelineID: p.ID,
			CronExpr:         "0 8 * * *",
			Timezone:         "UTC",
			NextRunAt:        &o,
		}
	}

	scheduler.fireOne(ctx, mk())
	scheduler.fireOne(ctx, mk()) // same occurrence — must dedupe, not re-run

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pipeline_runs WHERE workspace_id = ?`, "ws_test").Scan(&n); err != nil {
		t.Fatalf("count runs: %v", err)
	}
	if n != 1 {
		t.Fatalf("run count = %d, want 1 (a re-fire of the same occurrence must dedupe)", n)
	}
}

// A DISTINCT occurrence (a later next_run_at) must NOT be deduped — the key is
// per-occurrence, not per-schedule, so the schedule keeps firing on schedule.
func TestScheduledFire_DistinctOccurrences_BothRun(t *testing.T) {
	db := openFactoryTestDB(t)
	defer db.Close()
	ctx := context.Background()

	runner := newMockRunner()
	runner.outputsBySlug["agent_lead"] = []string{"ok"}
	deps := fullExecutorDeps(t, db, runner)
	exec := NewWiredExecutor(deps)
	store := deps.Store
	p := saveResumePipeline(t, store, "cron-idem2", agentStepDef)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	scheduler := NewPipelineScheduler(NewScheduleStore(db), store, exec, logger)

	mk := func(occ time.Time) *Schedule {
		o := occ
		return &Schedule{
			ID:               "psched_idem2",
			WorkspaceID:      "ws_test2",
			TargetPipelineID: p.ID,
			CronExpr:         "0 8 * * *",
			Timezone:         "UTC",
			NextRunAt:        &o,
		}
	}

	scheduler.fireOne(ctx, mk(time.Date(2026, 1, 1, 8, 0, 0, 0, time.UTC)))
	scheduler.fireOne(ctx, mk(time.Date(2026, 1, 2, 8, 0, 0, 0, time.UTC)))

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pipeline_runs WHERE workspace_id = ?`, "ws_test2").Scan(&n); err != nil {
		t.Fatalf("count runs: %v", err)
	}
	if n != 2 {
		t.Fatalf("run count = %d, want 2 (distinct occurrences must both fire)", n)
	}
}
