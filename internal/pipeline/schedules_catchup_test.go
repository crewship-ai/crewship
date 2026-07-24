package pipeline

// Missed-run catch-up policy tests (#1422 item 2). Today an overdue
// schedule fires exactly once on the next tick regardless of how far
// behind it fell — that is the CatchupOnce default and stays unchanged.
// catchup_policy adds two more options: skip the backlog entirely, or
// fire once per missed occurrence.

import (
	"context"
	"testing"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/crewship-ai/crewship/internal/tsformat"
)

// tsformatForTest wraps tsformat.Format for readability at call sites below.
func tsformatForTest(t time.Time) string { return tsformat.Format(t) }

// cronParserForTest mirrors the parser flags schedules.go itself uses.
func cronParserForTest() cron.Parser {
	return cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)
}

func TestPipelineScheduler_Catchup_OnceIsDefaultAndUnchanged(t *testing.T) {
	db := openScheduleTestDB(t)
	defer db.Close()
	seedPipeline(t, db, "pipe_1", "x")
	_, err := db.ExecContext(context.Background(),
		`UPDATE pipelines SET definition_json = ?, last_test_run_at = ?, last_test_run_passed = 1 WHERE id = 'pipe_1'`,
		`{"name":"x","steps":[{"id":"s1","type":"agent_run","agent":"agent_lead","prompt":"hi"}]}`,
		time.Now().UTC().Format(time.RFC3339Nano), // tsformat:allow: last_test_run_at freshness is checked in Go via time.Since, never SQL-compared
	)
	if err != nil {
		t.Fatalf("update pipeline def: %v", err)
	}

	scheduleStore := NewScheduleStore(db)
	pipelineStore := NewStore(db)
	resolver := NewResolver(db)
	runner := &stubAgentRunner{}
	exec := NewExecutor(pipelineStore, resolver, runner, nil)

	// Due 5 whole minutes ago on an every-minute cron — a real backlog of
	// several occurrences, all dropped except the one that fires.
	dueAt := time.Now().UTC().Add(-5 * time.Minute).Truncate(time.Minute)
	now := time.Now().UTC().Format(time.RFC3339Nano) // tsformat:allow: schedule created_at/updated_at are not ordered/compared in SQL; next_run_at (the compared column) uses tsformatForTest
	_, err = db.ExecContext(context.Background(), `
INSERT INTO pipeline_schedules
  (id, workspace_id, name, target_pipeline_id, cron_expr, timezone, inputs_json, enabled, next_run_at, catchup_policy, created_at, updated_at)
VALUES ('psched_once', 'ws_test', 'once', 'pipe_1', '* * * * *', 'UTC', '{}', 1, ?, 'once', ?, ?)`,
		tsformatForTest(dueAt), now, now)
	if err != nil {
		t.Fatalf("seed overdue: %v", err)
	}

	scheduler := NewPipelineScheduler(scheduleStore, pipelineStore, exec, nil)
	scheduler.tick(context.Background())

	if got := runner.calls.Load(); got != 1 {
		t.Errorf("catchup=once: expected exactly 1 executor call (unchanged behaviour), got %d", got)
	}
	sc, err := scheduleStore.GetByID(context.Background(), "psched_once")
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if sc.LastMissedCount < 3 {
		t.Errorf("catchup=once: expected last_missed_count >= 3 for a 5-minute backlog, got %d", sc.LastMissedCount)
	}
	if sc.LastStatus != "COMPLETED" {
		t.Errorf("catchup=once: expected last_status COMPLETED, got %q", sc.LastStatus)
	}
}

func TestPipelineScheduler_Catchup_Skip(t *testing.T) {
	db := openScheduleTestDB(t)
	defer db.Close()
	seedPipeline(t, db, "pipe_1", "x")
	_, err := db.ExecContext(context.Background(),
		`UPDATE pipelines SET definition_json = ?, last_test_run_at = ?, last_test_run_passed = 1 WHERE id = 'pipe_1'`,
		`{"name":"x","steps":[{"id":"s1","type":"agent_run","agent":"agent_lead","prompt":"hi"}]}`,
		time.Now().UTC().Format(time.RFC3339Nano), // tsformat:allow: last_test_run_at freshness is checked in Go via time.Since, never SQL-compared
	)
	if err != nil {
		t.Fatalf("update pipeline def: %v", err)
	}

	scheduleStore := NewScheduleStore(db)
	pipelineStore := NewStore(db)
	resolver := NewResolver(db)
	runner := &stubAgentRunner{}
	exec := NewExecutor(pipelineStore, resolver, runner, nil)

	dueAt := time.Now().UTC().Add(-5 * time.Minute).Truncate(time.Minute)
	now := time.Now().UTC().Format(time.RFC3339Nano) // tsformat:allow: schedule created_at/updated_at are not ordered/compared in SQL; next_run_at (the compared column) uses tsformatForTest
	_, err = db.ExecContext(context.Background(), `
INSERT INTO pipeline_schedules
  (id, workspace_id, name, target_pipeline_id, cron_expr, timezone, inputs_json, enabled, next_run_at, catchup_policy, created_at, updated_at)
VALUES ('psched_skip', 'ws_test', 'skip', 'pipe_1', '* * * * *', 'UTC', '{}', 1, ?, 'skip', ?, ?)`,
		tsformatForTest(dueAt), now, now)
	if err != nil {
		t.Fatalf("seed overdue: %v", err)
	}

	scheduler := NewPipelineScheduler(scheduleStore, pipelineStore, exec, nil)
	scheduler.tick(context.Background())

	if got := runner.calls.Load(); got != 0 {
		t.Errorf("catchup=skip: expected 0 executor calls, got %d", got)
	}
	sc, err := scheduleStore.GetByID(context.Background(), "psched_skip")
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if sc.LastMissedCount < 4 {
		t.Errorf("catchup=skip: expected last_missed_count >= 4 (whole backlog dropped), got %d", sc.LastMissedCount)
	}
	if sc.NextRunAt == nil || !sc.NextRunAt.After(time.Now()) {
		t.Errorf("catchup=skip: expected next_run_at advanced past now, got %v", sc.NextRunAt)
	}
	if sc.LastRunID != "" {
		t.Errorf("catchup=skip: expected no run id recorded, got %q", sc.LastRunID)
	}
}

func TestPipelineScheduler_Catchup_All(t *testing.T) {
	db := openScheduleTestDB(t)
	defer db.Close()
	seedPipeline(t, db, "pipe_1", "x")
	_, err := db.ExecContext(context.Background(),
		`UPDATE pipelines SET definition_json = ?, last_test_run_at = ?, last_test_run_passed = 1 WHERE id = 'pipe_1'`,
		`{"name":"x","steps":[{"id":"s1","type":"agent_run","agent":"agent_lead","prompt":"hi"}]}`,
		time.Now().UTC().Format(time.RFC3339Nano), // tsformat:allow: last_test_run_at freshness is checked in Go via time.Since, never SQL-compared
	)
	if err != nil {
		t.Fatalf("update pipeline def: %v", err)
	}

	scheduleStore := NewScheduleStore(db)
	pipelineStore := NewStore(db)
	resolver := NewResolver(db)
	runner := &stubAgentRunner{}
	exec := NewExecutor(pipelineStore, resolver, runner, nil)

	dueAt := time.Now().UTC().Add(-5 * time.Minute).Truncate(time.Minute)
	now := time.Now().UTC().Format(time.RFC3339Nano) // tsformat:allow: schedule created_at/updated_at are not ordered/compared in SQL; next_run_at (the compared column) uses tsformatForTest
	_, err = db.ExecContext(context.Background(), `
INSERT INTO pipeline_schedules
  (id, workspace_id, name, target_pipeline_id, cron_expr, timezone, inputs_json, enabled, next_run_at, catchup_policy, created_at, updated_at)
VALUES ('psched_all', 'ws_test', 'all', 'pipe_1', '* * * * *', 'UTC', '{}', 1, ?, 'all', ?, ?)`,
		tsformatForTest(dueAt), now, now)
	if err != nil {
		t.Fatalf("seed overdue: %v", err)
	}

	scheduler := NewPipelineScheduler(scheduleStore, pipelineStore, exec, nil)
	scheduler.tick(context.Background())

	if got := runner.calls.Load(); got < 4 {
		t.Errorf("catchup=all: expected >= 4 executor calls for a 5-minute backlog, got %d", got)
	}
	sc, err := scheduleStore.GetByID(context.Background(), "psched_all")
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if sc.LastMissedCount != 0 {
		t.Errorf("catchup=all: expected last_missed_count 0 (nothing dropped), got %d", sc.LastMissedCount)
	}
	if sc.LastStatus != "COMPLETED" {
		t.Errorf("catchup=all: expected last_status COMPLETED, got %q", sc.LastStatus)
	}
	if sc.NextRunAt == nil || !sc.NextRunAt.After(time.Now()) {
		t.Errorf("catchup=all: expected next_run_at advanced past now, got %v", sc.NextRunAt)
	}
}

// On-time schedules (no backlog) behave identically under every policy —
// the policy only matters once a schedule has fallen behind.
func TestPipelineScheduler_Catchup_OnTimeIgnoresPolicy(t *testing.T) {
	for _, policy := range []string{CatchupSkip, CatchupOnce, CatchupAll} {
		t.Run(policy, func(t *testing.T) {
			db := openScheduleTestDB(t)
			defer db.Close()
			seedPipeline(t, db, "pipe_1", "x")
			_, err := db.ExecContext(context.Background(),
				`UPDATE pipelines SET definition_json = ?, last_test_run_at = ?, last_test_run_passed = 1 WHERE id = 'pipe_1'`,
				`{"name":"x","steps":[{"id":"s1","type":"agent_run","agent":"agent_lead","prompt":"hi"}]}`,
				time.Now().UTC().Format(time.RFC3339Nano), // tsformat:allow: last_test_run_at freshness is checked in Go via time.Since, never SQL-compared
			)
			if err != nil {
				t.Fatalf("update pipeline def: %v", err)
			}
			scheduleStore := NewScheduleStore(db)
			pipelineStore := NewStore(db)
			resolver := NewResolver(db)
			runner := &stubAgentRunner{}
			exec := NewExecutor(pipelineStore, resolver, runner, nil)

			// next_run_at IS ordered/compared in SQL by listDueSchedules, so
			// seed it with the same fixed-width tsformat the store writes.
			pastTime := tsformatForTest(time.Now().Add(-time.Minute).UTC())
			now := time.Now().UTC().Format(time.RFC3339Nano) // tsformat:allow: schedule created_at/updated_at are not ordered/compared in SQL; next_run_at (the compared column) uses tsformatForTest
			_, err = db.ExecContext(context.Background(), `
INSERT INTO pipeline_schedules
  (id, workspace_id, name, target_pipeline_id, cron_expr, timezone, inputs_json, enabled, next_run_at, catchup_policy, created_at, updated_at)
VALUES ('psched_ontime', 'ws_test', 'ontime', 'pipe_1', '0 8 * * *', 'UTC', '{}', 1, ?, ?, ?, ?)`,
				pastTime, policy, now, now)
			if err != nil {
				t.Fatalf("seed due: %v", err)
			}

			scheduler := NewPipelineScheduler(scheduleStore, pipelineStore, exec, nil)
			scheduler.tick(context.Background())

			if got := runner.calls.Load(); got != 1 {
				t.Errorf("policy=%s on-time: expected exactly 1 executor call, got %d", policy, got)
			}
			sc, err := scheduleStore.GetByID(context.Background(), "psched_ontime")
			if err != nil {
				t.Fatalf("reload: %v", err)
			}
			if sc.LastMissedCount != 0 {
				t.Errorf("policy=%s on-time: expected last_missed_count 0, got %d", policy, sc.LastMissedCount)
			}
		})
	}
}

func TestScheduleStore_Save_CatchupPolicy(t *testing.T) {
	db := openScheduleTestDB(t)
	defer db.Close()
	seedPipeline(t, db, "pipe_1", "daily-digest")
	store := NewScheduleStore(db)

	// Default: empty CatchupPolicy becomes "once".
	out, err := store.Save(context.Background(), SaveScheduleInput{
		WorkspaceID: "ws_test", Name: "n", TargetPipelineID: "pipe_1",
		CronExpr: "0 8 * * *", Timezone: "UTC", Enabled: true,
	})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if out.CatchupPolicy != CatchupOnce {
		t.Errorf("default catchup_policy = %q, want %q", out.CatchupPolicy, CatchupOnce)
	}

	// Explicit valid policy round-trips.
	out2, err := store.Save(context.Background(), SaveScheduleInput{
		WorkspaceID: "ws_test", Name: "n2", TargetPipelineID: "pipe_1",
		CronExpr: "0 9 * * *", Timezone: "UTC", Enabled: true, CatchupPolicy: CatchupAll,
	})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if out2.CatchupPolicy != CatchupAll {
		t.Errorf("catchup_policy = %q, want %q", out2.CatchupPolicy, CatchupAll)
	}

	// Invalid policy is rejected loudly.
	if _, err := store.Save(context.Background(), SaveScheduleInput{
		WorkspaceID: "ws_test", Name: "n3", TargetPipelineID: "pipe_1",
		CronExpr: "0 10 * * *", Timezone: "UTC", Enabled: true, CatchupPolicy: "sometimes",
	}); err == nil {
		t.Error("expected error for invalid catchup_policy")
	}
}

func TestMissedOccurrencesSince(t *testing.T) {
	parser := cronParserForTest()
	sched, err := parser.Parse("* * * * *")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	from := time.Date(2026, 7, 20, 10, 0, 0, 0, time.UTC)
	now := time.Date(2026, 7, 20, 10, 4, 30, 0, time.UTC) // 4.5 minutes later
	occs := missedOccurrencesSince(sched, from, now)
	want := []time.Time{
		time.Date(2026, 7, 20, 10, 1, 0, 0, time.UTC),
		time.Date(2026, 7, 20, 10, 2, 0, 0, time.UTC),
		time.Date(2026, 7, 20, 10, 3, 0, 0, time.UTC),
		time.Date(2026, 7, 20, 10, 4, 0, 0, time.UTC),
	}
	if len(occs) != len(want) {
		t.Fatalf("got %d occurrences, want %d: %v", len(occs), len(want), occs)
	}
	for i, w := range want {
		if !occs[i].Equal(w) {
			t.Errorf("occs[%d] = %v, want %v", i, occs[i], w)
		}
	}
}

// TestPipelineScheduler_Catchup_Skip_RaisesInboxNotice uses the pinning
// rig (which wires a real inbox_items table) to assert catchup=skip
// actually lands a KindScheduleMissed inbox row, not just a log line.
func TestPipelineScheduler_Catchup_Skip_RaisesInboxNotice(t *testing.T) {
	rig := newPinningRig(t)
	p := saveResumePipeline(t, rig.deps.Store, "skip-target", pinV1DSL)
	sched, err := rig.store.Save(context.Background(), SaveScheduleInput{
		WorkspaceID:      "ws_test",
		Name:             "skip-sched",
		TargetPipelineID: p.ID,
		CronExpr:         "* * * * *",
		Timezone:         "UTC",
		Enabled:          true,
		CatchupPolicy:    CatchupSkip,
	})
	if err != nil {
		t.Fatalf("save schedule: %v", err)
	}
	dueAt := time.Now().UTC().Add(-5 * time.Minute).Truncate(time.Minute)
	if _, err := rig.db.Exec(`UPDATE pipeline_schedules SET next_run_at = ? WHERE id = ?`,
		tsformatForTest(dueAt), sched.ID); err != nil {
		t.Fatalf("backdate next_run_at: %v", err)
	}

	rig.scheduler.tick(context.Background())

	var n int
	if err := rig.db.QueryRow(`SELECT COUNT(*) FROM inbox_items WHERE kind = 'schedule_missed'`).Scan(&n); err != nil {
		t.Fatalf("count inbox notices: %v", err)
	}
	if n != 1 {
		t.Errorf("expected 1 schedule_missed inbox notice, got %d", n)
	}
}

func TestMissedOccurrencesSince_Cap(t *testing.T) {
	parser := cronParserForTest()
	sched, err := parser.Parse("* * * * *")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	from := time.Date(2026, 7, 20, 10, 0, 0, 0, time.UTC)
	now := from.Add(time.Duration(maxCatchupFireOccurrences+10) * time.Minute)
	occs := missedOccurrencesSince(sched, from, now)
	if len(occs) != maxCatchupFireOccurrences {
		t.Errorf("got %d occurrences, want capped at %d", len(occs), maxCatchupFireOccurrences)
	}
}
