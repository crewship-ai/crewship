package pipeline

// Scheduler head-of-line blocking (#1406).
//
// PipelineScheduler.tick used to iterate due schedules and call fireOne
// serially — one slow COMPLETED/FAILED routine stalled every other due
// schedule in that 30s tick. These tests pin the fixed contract,
// mirroring PendingRunDispatcher's bounded-worker-pool pattern
// (pending_dispatcher.go):
//
//   - two due schedules where the first is slow: the second fires
//     WITHOUT waiting for the first to finish (proven via a blocking
//     runner + start signals, not wall-clock timing).
//   - concurrent ticks against the SAME due schedule still dedupe to
//     exactly one run — the idempotency chokepoint (already proven
//     serially in schedules_idempotency_test.go) also holds when two
//     ticks race concurrently, which is the shape #1406 introduces.
//   - the worker pool records a breadcrumb when it saturates (more due
//     schedules in one tick than free workers), so an under-provisioned
//     scheduler is observable rather than silently queueing.

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/tsformat"
)

// dispatchBlockingRunner signals `started` with the agent slug on every RunStep
// call (so a test can prove two calls happened concurrently rather than
// one waiting on the other) and blocks any call whose slug is in
// `blockSlugs` until the test sends on `release`.
type dispatchBlockingRunner struct {
	started    chan string
	release    chan struct{}
	blockSlugs map[string]bool
}

func newDispatchBlockingRunner(blockSlugs ...string) *dispatchBlockingRunner {
	set := map[string]bool{}
	for _, s := range blockSlugs {
		set[s] = true
	}
	return &dispatchBlockingRunner{
		started:    make(chan string, 16),
		release:    make(chan struct{}),
		blockSlugs: set,
	}
}

func (r *dispatchBlockingRunner) RunStep(ctx context.Context, req AgentStepRequest) (AgentStepResult, error) {
	r.started <- req.AgentSlug
	if r.blockSlugs[req.AgentSlug] {
		select {
		case <-r.release:
		case <-ctx.Done():
			return AgentStepResult{}, ctx.Err()
		}
	}
	return AgentStepResult{Output: "ok", DurationMs: 1, CostUSD: 0}, nil
}

func seedDueScheduleRow(t *testing.T, db *sql.DB, id, pipelineID string) {
	t.Helper()
	pastTime := tsformat.Format(time.Now().Add(-time.Minute).UTC()) // match schedules.go's fixed-width next_run_at (#990); it is string-compared in listDueSchedules
	now := tsformat.Format(time.Now().UTC())
	_, err := db.ExecContext(context.Background(), `
INSERT INTO pipeline_schedules
  (id, workspace_id, name, target_pipeline_id, cron_expr, timezone, inputs_json, enabled, next_run_at, created_at, updated_at)
VALUES (?, 'ws_test', ?, ?, '0 8 * * *', 'UTC', '{}', 1, ?, ?, ?)`,
		id, id, pipelineID, pastTime, now, now)
	if err != nil {
		t.Fatalf("seed due schedule %s: %v", id, err)
	}
}

const oneStepAgentDefFmt = `{"dsl_version":"1.0","name":%q,"steps":[{"id":"s1","type":"agent_run","agent_slug":%q,"prompt":"go"}]}`

func TestPipelineScheduler_Tick_DueSchedulesFireConcurrently(t *testing.T) {
	db := openScheduleTestDB(t)
	defer db.Close()
	seedPipelineDef(t, db, "pipe_slow", "slow", fmt.Sprintf(oneStepAgentDefFmt, "slow", "slow_agent"))
	seedPipelineDef(t, db, "pipe_fast", "fast", fmt.Sprintf(oneStepAgentDefFmt, "fast", "fast_agent"))
	seedDueScheduleRow(t, db, "psched_slow", "pipe_slow")
	seedDueScheduleRow(t, db, "psched_fast", "pipe_fast")

	runner := newDispatchBlockingRunner("slow_agent")
	scheduleStore := NewScheduleStore(db)
	pipelineStore := NewStore(db)
	exec := NewExecutor(pipelineStore, NewResolver(db), runner, nil)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	scheduler := NewPipelineScheduler(scheduleStore, pipelineStore, exec, logger)

	tickDone := make(chan struct{})
	go func() {
		scheduler.tick(context.Background())
		close(tickDone)
	}()

	// Both schedules must have STARTED within a short window — if the old
	// serial for-loop were still in effect, fast_agent would never even
	// start until slow_agent's blocking call returned (it never does
	// until we release it below), so this would time out on current
	// broken behaviour.
	seen := map[string]bool{}
	deadline := time.After(2 * time.Second)
	for len(seen) < 2 {
		select {
		case slug := <-runner.started:
			seen[slug] = true
		case <-deadline:
			t.Fatalf("timed out waiting for both schedules to start concurrently; saw %v", seen)
		}
	}

	close(runner.release)
	select {
	case <-tickDone:
	case <-time.After(2 * time.Second):
		t.Fatal("tick did not complete after releasing the slow runner")
	}

	slow, err := scheduleStore.GetByID(context.Background(), "psched_slow")
	if err != nil {
		t.Fatalf("get slow schedule: %v", err)
	}
	fast, err := scheduleStore.GetByID(context.Background(), "psched_fast")
	if err != nil {
		t.Fatalf("get fast schedule: %v", err)
	}
	if slow.LastStatus != "COMPLETED" {
		t.Errorf("slow schedule last_status = %q, want COMPLETED", slow.LastStatus)
	}
	if fast.LastStatus != "COMPLETED" {
		t.Errorf("fast schedule last_status = %q, want COMPLETED", fast.LastStatus)
	}
}

func TestPipelineScheduler_Tick_ConcurrentTicksDoNotDoubleFire(t *testing.T) {
	db := openFactoryTestDB(t)
	defer db.Close()
	ctx := context.Background()

	runner := newMockRunner()
	runner.outputsBySlug["agent_lead"] = []string{"ok", "ok", "ok"}
	deps := fullExecutorDeps(t, db, runner)
	exec := NewWiredExecutor(deps)
	store := deps.Store
	p := saveResumePipeline(t, store, "cron-concurrent-tick", agentStepDef)

	scheduleStore := NewScheduleStore(db)
	seedDueScheduleRow(t, db, "psched_ct", p.ID)
	// seedDueScheduleRow uses 'ws_test' for both id and name column by
	// convenience — workspace_id is what matters for the run-count query
	// below and is hardcoded to ws_test in the helper.

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	scheduler := NewPipelineScheduler(scheduleStore, store, exec, logger)

	var wg sync.WaitGroup
	wg.Add(3)
	for i := 0; i < 3; i++ {
		go func() {
			defer wg.Done()
			scheduler.tick(ctx)
		}()
	}
	wg.Wait()

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pipeline_runs WHERE workspace_id = 'ws_test'`).Scan(&n); err != nil {
		t.Fatalf("count runs: %v", err)
	}
	if n != 1 {
		t.Errorf("run count = %d, want 1 (concurrent ticks racing the same due schedule must not double-fire)", n)
	}
}

func TestPipelineScheduler_Tick_PoolSaturation_RecordsBreadcrumb(t *testing.T) {
	db := openScheduleTestDB(t)
	defer db.Close()
	seedPipelineDef(t, db, "pipe_a", "a", fmt.Sprintf(oneStepAgentDefFmt, "a", "agent_a"))
	seedPipelineDef(t, db, "pipe_b", "b", fmt.Sprintf(oneStepAgentDefFmt, "b", "agent_b"))
	seedDueScheduleRow(t, db, "psched_a", "pipe_a")
	seedDueScheduleRow(t, db, "psched_b", "pipe_b")

	runner := newDispatchBlockingRunner("agent_a")
	scheduleStore := NewScheduleStore(db)
	pipelineStore := NewStore(db)
	exec := NewExecutor(pipelineStore, NewResolver(db), runner, nil)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	scheduler := NewPipelineScheduler(scheduleStore, pipelineStore, exec, logger)
	// Force a single-worker pool so the second due schedule in this tick
	// MUST wait for a slot — that's the saturation condition #1406 asks
	// to be observable.
	scheduler.maxConcurrency = 1

	tickDone := make(chan struct{})
	go func() {
		scheduler.tick(context.Background())
		close(tickDone)
	}()

	select {
	case <-runner.started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the first schedule to start")
	}
	// agent_b cannot have started yet (pool size 1, agent_a is holding
	// the only slot) — give the dispatcher a moment to have attempted
	// (and blocked on) acquiring the second slot, then release agent_a.
	time.Sleep(50 * time.Millisecond)
	close(runner.release)

	select {
	case <-tickDone:
	case <-time.After(2 * time.Second):
		t.Fatal("tick did not complete")
	}

	if got := scheduler.dispatchSaturatedCount.Load(); got < 1 {
		t.Errorf("dispatchSaturatedCount = %d, want >= 1 (pool saturation must be observable)", got)
	}
}
