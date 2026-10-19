package pipeline

import (
	"database/sql"
	"fmt"
	"testing"
	"time"
)

func cronCapacityRig(t *testing.T) (*sql.DB, *ScheduleStore, *PipelineScheduler, *Schedule, func()) {
	t.Helper()
	db, store, scheduler := newGoldenRig(t)
	goldenSeedTransformPipeline(t, db, "cron-capacity-pipeline", "cron-capacity", "done")
	definition := `{"dsl_version":"1.0","name":"cron-capacity","agentless":true,"concurrency_key":"cron-shared","max_concurrent":1,"steps":[{"id":"t","type":"transform","transform":{"input":"{{ inputs.message }}","expression":"."}}]}`
	if _, err := db.Exec(`UPDATE pipelines SET definition_json=? WHERE id='cron-capacity-pipeline'`, definition); err != nil {
		t.Fatal(err)
	}
	version, err := scheduler.pipelines.SaveVersion(t.Context(), SaveVersionInput{PipelineID: "cron-capacity-pipeline", DefinitionJSON: definition, AuthorType: "system"})
	if err != nil {
		t.Fatal(err)
	}
	occurrence := time.Now().UTC().Truncate(time.Minute)
	goldenInsertSchedule(t, db, goldenScheduleRow{ID: "cron-capacity-schedule", TargetPipelineID: "cron-capacity-pipeline", CronExpr: "* * * * *", NextRunAt: occurrence, ConsecutiveFailures: 2, MaxConsecutiveFailures: 3})
	if _, err := db.Exec(`UPDATE pipeline_schedules SET inputs_json='{"message":"queued-cron-result"}' WHERE id='cron-capacity-schedule'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE pipeline_schedules SET target_pipeline_version=? WHERE id='cron-capacity-schedule'`, version.Version); err != nil {
		t.Fatal(err)
	}
	schedule, err := store.GetByID(t.Context(), "cron-capacity-schedule")
	if err != nil {
		t.Fatal(err)
	}
	registry := NewRunRegistry()
	scheduler.executor.WithRunRegistry(registry).WithIdempotencyStore(NewIdempotencyStore(db))
	_, release, err := registry.Acquire(t.Context(), AcquireOpts{RunID: "cron-holder", WorkspaceID: goldenScheduleWS, PipelineID: schedule.TargetPipelineID, ConcurrencyKey: "cron-shared", MaxConcurrent: 1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(release)
	scheduler.nowFn = func() time.Time { return occurrence }
	return db, store, scheduler, schedule, release
}

func TestScheduleCapacityQueuesWithoutFailureOrBreaker(t *testing.T) {
	db, store, scheduler, schedule, _ := cronCapacityRig(t)
	scheduler.fireOne(t.Context(), schedule)
	updated, err := store.GetByID(t.Context(), schedule.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.LastStatus != "QUEUED" || updated.ConsecutiveFailures != 2 || !updated.Enabled || updated.DisabledReason != "" {
		t.Fatalf("capacity was treated as execution failure: %+v", updated)
	}
	var alerts int
	if err := db.QueryRow(`SELECT count(*) FROM inbox_items WHERE kind IN ('failed_run','schedule_circuit_breaker_tripped')`).Scan(&alerts); err != nil {
		t.Fatal(err)
	}
	if alerts != 0 {
		t.Fatalf("capacity emitted %d execution alerts", alerts)
	}
	var pending int
	if err := db.QueryRow(`SELECT count(*) FROM pending_runs WHERE status='pending'`).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if pending != 1 {
		t.Fatalf("pending=%d want1", pending)
	}
	rows, err := NewPendingRunStore(db).ListPending(t.Context(), goldenScheduleWS, 10)
	if err != nil || len(rows) != 1 {
		t.Fatalf("accepted receipt: rows=%+v err=%v", rows, err)
	}
	if rows[0].ReasonCode != "routine_capacity" || rows[0].FiredRunID != "" || rows[0].DispatchAttempts != 0 {
		t.Fatalf("waiting receipt invented a confirmed start or lost capacity reason: %+v", rows[0])
	}
}

// Advancing the injected clock holds one real registry slot for five cron
// occurrences without sleeping or invoking an external agent/provider.
func TestScheduleCapacityKeepsOneWaitingOccurrenceAcrossMinutes(t *testing.T) {
	db, store, scheduler, schedule, _ := cronCapacityRig(t)
	first := scheduler.now()
	scheduler.fireOne(t.Context(), schedule)
	pending := NewPendingRunStore(db)
	rows, err := pending.ListPending(t.Context(), goldenScheduleWS, 10)
	if err != nil || len(rows) != 1 {
		t.Fatalf("first admission: rows=%+v err=%v", rows, err)
	}
	accepted := rows[0]
	for minute := 1; minute <= 4; minute++ {
		now := first.Add(time.Duration(minute) * time.Minute)
		scheduler.nowFn = func() time.Time { return now }
		current, err := store.GetByID(t.Context(), schedule.ID)
		if err != nil {
			t.Fatal(err)
		}
		scheduler.fireOne(t.Context(), current)
		rows, err = pending.ListPending(t.Context(), goldenScheduleWS, 10)
		if err != nil || len(rows) != 1 {
			t.Fatalf("minute %d: pending=%d want1 err=%v", minute, len(rows), err)
		}
		if rows[0].RunID != accepted.RunID || rows[0].ID != accepted.ID || rows[0].InputsJSON != accepted.InputsJSON || rows[0].DueAt == nil || !rows[0].DueAt.Equal(*accepted.DueAt) {
			t.Fatalf("newer occurrence replaced accepted work: %+v", rows[0])
		}
		updated, err := store.GetByID(t.Context(), schedule.ID)
		if err != nil {
			t.Fatal(err)
		}
		if updated.LastStatus != "SKIPPED" || updated.LastMissedCount != 1 || updated.LastRunID != "" || updated.ConsecutiveFailures != 2 || !updated.Enabled || updated.DisabledReason != "" || updated.NextRunAt == nil || !updated.NextRunAt.Equal(now.Add(time.Minute)) {
			t.Fatalf("minute %d: incorrect skipped occurrence bookkeeping: %+v", minute, updated)
		}
	}
	var alerts, runs int
	if err := db.QueryRow(`SELECT count(*) FROM inbox_items`).Scan(&alerts); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM pipeline_runs`).Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if alerts != 0 || runs != 0 {
		t.Fatalf("capacity skip produced alerts=%d runs=%d", alerts, runs)
	}
}

func TestScheduleCapacityWaitingSlotLifecycle(t *testing.T) {
	for _, tc := range []struct {
		name       string
		update     string
		wantStatus string
		wantRows   int
	}{
		{"claimed", `status='fired',fired_run_id=''`, "SKIPPED", 1},
		{"uncertain", `status='fired',fired_run_id='',reason_code='recovery_unknown'`, "SKIPPED", 1},
		{"confirmed", `status='fired',fired_run_id=run_id`, "QUEUED", 2},
		{"expired", `status='expired'`, "QUEUED", 2},
		{"cancelled", `status='cancelled'`, "QUEUED", 2},
		{"other_schedule", `triggered_by_id='another-schedule'`, "QUEUED", 2},
		{"other_trigger", `triggered_via='webhook'`, "QUEUED", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, store, scheduler, schedule, _ := cronCapacityRig(t)
			scheduler.fireOne(t.Context(), schedule)
			if _, err := db.Exec(`UPDATE pending_runs SET ` + tc.update); err != nil {
				t.Fatal(err)
			}
			now := scheduler.now().Add(time.Minute)
			scheduler.nowFn = func() time.Time { return now }
			current, err := store.GetByID(t.Context(), schedule.ID)
			if err != nil {
				t.Fatal(err)
			}
			scheduler.fireOne(t.Context(), current)
			updated, err := store.GetByID(t.Context(), schedule.ID)
			if err != nil {
				t.Fatal(err)
			}
			var count int
			if err := db.QueryRow(`SELECT count(*) FROM pending_runs`).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if updated.LastStatus != tc.wantStatus || count != tc.wantRows {
				t.Fatalf("status=%s rows=%d want %s/%d", updated.LastStatus, count, tc.wantStatus, tc.wantRows)
			}
		})
	}
}

func TestScheduleQueuedOccurrenceRetainsIdentityAcrossDuplicateTicks(t *testing.T) {
	db, _, scheduler, schedule, release := cronCapacityRig(t)
	scheduler.fireOne(t.Context(), schedule)
	scheduler.fireOne(t.Context(), schedule)
	pending := NewPendingRunStore(db)
	rows, err := pending.ListPending(t.Context(), goldenScheduleWS, 10)
	if err != nil || len(rows) != 1 {
		t.Fatalf("duplicate intents: %+v %v", rows, err)
	}
	accepted := rows[0]
	if accepted.RunID == "" || accepted.IdempotencyKey != ScheduledFireIdempotencyKey("sched", schedule.ID, schedule.NextRunAt.UTC().Format(time.RFC3339)) || accepted.DueAt == nil || !accepted.DueAt.Equal(*schedule.NextRunAt) || accepted.PinnedVersion == nil || *accepted.PinnedVersion != *schedule.TargetPipelineVersion {
		t.Fatalf("lost occurrence identity: %+v", accepted)
	}
	release()
	// A repeated tick after capacity frees must not bypass the existing queue.
	scheduler.fireOne(t.Context(), schedule)
	var runs int
	if err := db.QueryRow(`SELECT count(*) FROM pipeline_runs`).Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if runs != 0 {
		t.Fatalf("duplicate tick bypassed accepted queue: %d runs", runs)
	}
	// Mutating head must not move the immutable accepted pin.
	if _, err := db.Exec(`UPDATE pipelines SET definition_json='{"dsl_version":"1.0","name":"new-head","agentless":true,"steps":[{"id":"other","type":"transform","transform":{"input":"changed","expression":"."}}]}' WHERE id=?`, schedule.TargetPipelineID); err != nil {
		t.Fatal(err)
	}
	dispatcher := NewPendingRunDispatcher(pending, scheduler.executor, nil)
	dispatcher.fireOne(t.Context(), accepted)
	receipt, err := pending.Get(t.Context(), goldenScheduleWS, accepted.ID)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.FiredRunID != accepted.RunID {
		t.Fatalf("planned identity changed: %+v", receipt)
	}
	run, err := NewRunStore(db).Get(t.Context(), accepted.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != RunStatusCompleted || run.Output != "queued-cron-result" || run.TriggeredVia != TriggeredViaSchedule || run.TriggeredByID != schedule.ID || run.DueAt == nil || !run.DueAt.Equal(*schedule.NextRunAt) {
		t.Fatalf("wrong dispatched cron: %+v", run)
	}
	scheduler.fireOne(t.Context(), schedule)
	if err := db.QueryRow(`SELECT count(*) FROM pipeline_runs`).Scan(&runs); err != nil || runs != 1 {
		t.Fatalf("duplicate execution: runs=%d err=%v", runs, err)
	}
}

func TestScheduleQueueAdmissionAndCursorCommitTogether(t *testing.T) {
	for _, table := range []string{"pending_runs", "pipeline_schedules"} {
		t.Run(table, func(t *testing.T) {
			db, store, scheduler, schedule, _ := cronCapacityRig(t)
			operation, condition := "INSERT", ""
			if table == "pipeline_schedules" {
				operation = "UPDATE"
				condition = " WHEN NEW.last_status='QUEUED'"
			}
			trigger := fmt.Sprintf(`CREATE TRIGGER refuse_cron_queue BEFORE %s ON %s%s BEGIN SELECT RAISE(ABORT,'queue transaction unavailable'); END`, operation, table, condition)
			if _, err := db.Exec(trigger); err != nil {
				t.Fatal(err)
			}
			scheduler.fireOne(t.Context(), schedule)
			updated, err := store.GetByID(t.Context(), schedule.ID)
			if err != nil {
				t.Fatal(err)
			}
			if updated.NextRunAt == nil || !updated.NextRunAt.Equal(*schedule.NextRunAt) || updated.ConsecutiveFailures != 2 || !updated.Enabled {
				t.Fatalf("failed queue admission consumed occurrence: %+v", updated)
			}
			var count int
			if err := db.QueryRow(`SELECT count(*) FROM pending_runs`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("uncommitted queue leaked count=%d err=%v", count, err)
			}
			if _, err := db.Exec(`DROP TRIGGER refuse_cron_queue`); err != nil {
				t.Fatal(err)
			}
			scheduler.fireOne(t.Context(), schedule)
			if err := db.QueryRow(`SELECT count(*) FROM pending_runs`).Scan(&count); err != nil || count != 1 {
				t.Fatalf("occurrence not retried count=%d err=%v", count, err)
			}
		})
	}
}

func TestScheduleCatchupAllKeepsFirstUnacceptedOccurrenceDue(t *testing.T) {
	db, store, scheduler, schedule, _ := cronCapacityRig(t)
	now := scheduler.now()
	first := now.Add(-2 * time.Minute)
	second := first.Add(time.Minute)
	schedule.NextRunAt = &first
	schedule.CatchupPolicy = CatchupAll
	if _, err := db.Exec(`UPDATE pipeline_schedules SET next_run_at=?,catchup_policy='all' WHERE id=?`, tsformatForTest(first), schedule.ID); err != nil {
		t.Fatal(err)
	}
	// After accepting the first occurrence, the second is skipped. A failed
	// skip/cursor write must still leave that occurrence due for retry.
	trigger := `CREATE TRIGGER refuse_second_occurrence BEFORE UPDATE ON pipeline_schedules WHEN NEW.last_status='SKIPPED' BEGIN SELECT RAISE(ABORT,'second occurrence unavailable'); END`
	if _, err := db.Exec(trigger); err != nil {
		t.Fatal(err)
	}
	scheduler.fireOne(t.Context(), schedule)
	updated, err := store.GetByID(t.Context(), schedule.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.NextRunAt == nil || !updated.NextRunAt.Equal(second) {
		t.Fatalf("unaccepted backlog disappeared: %+v", updated)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM pending_runs`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("continued after failed acceptance: count=%d err=%v", count, err)
	}
	if _, err := db.Exec(`DROP TRIGGER refuse_second_occurrence`); err != nil {
		t.Fatal(err)
	}
	scheduler.fireOne(t.Context(), updated)
	updated, err = store.GetByID(t.Context(), schedule.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.NextRunAt == nil || !updated.NextRunAt.Equal(now.Add(time.Minute)) {
		t.Fatalf("backlog did not advance after acceptance: %+v", updated)
	}
	if err := db.QueryRow(`SELECT count(*) FROM pending_runs`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("backlog waiting limit violated: count=%d err=%v", count, err)
	}
}

func TestScheduleCatchupAllPreservesBacklogBeyondPerTickCap(t *testing.T) {
	db, store, scheduler, schedule, _ := cronCapacityRig(t)
	now := scheduler.now()
	first := now.Add(-time.Duration(maxCatchupFireOccurrences+5) * time.Minute)
	schedule.NextRunAt = &first
	schedule.CatchupPolicy = CatchupAll
	if _, err := db.Exec(`UPDATE pipeline_schedules SET next_run_at=?,catchup_policy='all' WHERE id=?`, tsformatForTest(first), schedule.ID); err != nil {
		t.Fatal(err)
	}
	scheduler.fireOne(t.Context(), schedule)
	updated, err := store.GetByID(t.Context(), schedule.ID)
	if err != nil {
		t.Fatal(err)
	}
	expected := first.Add(time.Duration(maxCatchupFireOccurrences+1) * time.Minute)
	if updated.NextRunAt == nil || !updated.NextRunAt.Equal(expected) {
		t.Fatalf("capped backlog skipped: cursor=%v want %v", updated.NextRunAt, expected)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM pending_runs`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("first tick count=%d err=%v", count, err)
	}
	scheduler.fireOne(t.Context(), updated)
	updated, err = store.GetByID(t.Context(), schedule.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.NextRunAt == nil || !updated.NextRunAt.Equal(now.Add(time.Minute)) {
		t.Fatalf("remaining backlog not consumed: %+v", updated)
	}
	if err := db.QueryRow(`SELECT count(*) FROM pending_runs`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("remaining backlog lost or duplicated: count=%d err=%v", count, err)
	}
}

func installSchedulePendingSchema(t *testing.T, db *sql.DB) {
	t.Helper()
	fixture := newPendingDB(t)
	defer fixture.Close()
	var ddl string
	if err := fixture.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name='pending_runs'`).Scan(&ddl); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ddl); err != nil {
		t.Fatal(err)
	}
}
