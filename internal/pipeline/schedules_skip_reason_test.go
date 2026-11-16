package pipeline

import (
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/tsformat"
)

func storedScheduleSkipReason(t *testing.T, db *sql.DB, id string) string {
	t.Helper()
	var reason string
	if err := db.QueryRow(`SELECT last_skip_reason FROM pipeline_schedules WHERE id=?`, id).Scan(&reason); err != nil {
		t.Fatal(err)
	}
	return reason
}

// Real executor governance/registry boundaries, with and without catch-up
// misses: missed-count or today's routine lifecycle cannot identify the cause.
func TestScheduleSkipReason_BusyAndInactive(t *testing.T) {
	for _, reason := range []string{"busy", "inactive"} {
		for _, backlog := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/backlog=%t", reason, backlog), func(t *testing.T) {
				db, store, scheduler := newGoldenRig(t)
				goldenSeedTransformPipeline(t, db, "skip-pipeline", "skip-pipeline", "done")
				now := time.Now().UTC().Truncate(time.Minute)
				scheduler.nowFn = func() time.Time { return now }
				due := now
				if backlog {
					due = now.Add(-3 * time.Minute)
				}
				goldenInsertSchedule(t, db, goldenScheduleRow{ID: "skip-schedule", TargetPipelineID: "skip-pipeline", CronExpr: "* * * * *", NextRunAt: due, ConsecutiveFailures: 2})
				release := func() {}
				if reason == "busy" {
					def := `{"dsl_version":"1.0","name":"skip-pipeline","agentless":true,"concurrency_key":"skip-key","max_concurrent":1,"concurrency_policy":"skip","steps":[{"id":"t","type":"transform","transform":{"input":"done","expression":"."}}]}`
					if _, err := db.Exec(`UPDATE pipelines SET definition_json=? WHERE id='skip-pipeline'`, def); err != nil {
						t.Fatal(err)
					}
					registry := NewRunRegistry()
					scheduler.executor.WithRunRegistry(registry)
					var err error
					_, release, err = registry.Acquire(t.Context(), AcquireOpts{RunID: "held", WorkspaceID: goldenScheduleWS, PipelineID: "skip-pipeline", ConcurrencyKey: "skip-key", MaxConcurrent: 1})
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(release)
				} else if _, err := db.Exec(`UPDATE pipelines SET status='disabled' WHERE id='skip-pipeline'`); err != nil {
					t.Fatal(err)
				}
				row, err := store.GetByID(t.Context(), "skip-schedule")
				if err != nil {
					t.Fatal(err)
				}
				before := time.Now()
				scheduler.fireOne(t.Context(), row)
				after := time.Now()
				row, err = store.GetByID(t.Context(), row.ID)
				if err != nil {
					t.Fatal(err)
				}
				if got := storedScheduleSkipReason(t, db, row.ID); got != reason {
					t.Errorf("stored skip reason=%q, want %q", got, reason)
				}
				if row.LastStatus != "SKIPPED" || row.LastRunID != "" || row.LastRunAt == nil || row.LastRunAt.Before(before) || row.LastRunAt.After(after) || row.ConsecutiveFailures != 2 {
					t.Fatalf("bad skip outcome: %+v", row)
				}
				if backlog && row.LastMissedCount == 0 {
					t.Fatal("fixture did not exercise catch-up count")
				}
				if !backlog && row.LastMissedCount != 0 {
					t.Fatalf("unexpected misses: %d", row.LastMissedCount)
				}
				if goldenRunCount(t, db, row.TargetPipelineID) != 0 {
					t.Fatal("a skip executed a run")
				}
				// Changing current lifecycle does not rewrite the historical reason.
				release()
				if _, err := db.Exec(`UPDATE pipelines SET status='active' WHERE id='skip-pipeline'`); err != nil {
					t.Fatal(err)
				}
				if got := storedScheduleSkipReason(t, db, row.ID); got != reason {
					t.Errorf("lifecycle change rewrote skip reason: %q", got)
				}
				// A later actual successful fire clears the old reason.
				next := *row.NextRunAt
				scheduler.nowFn = func() time.Time { return next }
				scheduler.fireOne(t.Context(), row)
				row, err = store.GetByID(t.Context(), row.ID)
				if err != nil {
					t.Fatal(err)
				}
				if row.LastStatus != "COMPLETED" || storedScheduleSkipReason(t, db, row.ID) != "" {
					t.Fatalf("later outcome kept stale skip reason: %+v", row)
				}
			})
		}
	}
}

func TestScheduleSkipReason_CapacityQueue(t *testing.T) {
	db, store, scheduler, row, _ := cronCapacityRig(t)
	// QUEUED is not a skip, and must replace any historical skip reason.
	if _, err := db.Exec(`UPDATE pipeline_schedules SET last_skip_reason='busy',last_status='SKIPPED' WHERE id=?`, row.ID); err != nil {
		t.Fatal(err)
	}
	scheduler.fireOne(t.Context(), row)
	if got := storedScheduleSkipReason(t, db, row.ID); got != "" {
		t.Fatalf("queue admission retained stale reason %q", got)
	}
	next := scheduler.now().Add(time.Minute)
	scheduler.nowFn = func() time.Time { return next }
	row, err := store.GetByID(t.Context(), row.ID)
	if err != nil {
		t.Fatal(err)
	}
	scheduler.fireOne(t.Context(), row)
	row, err = store.GetByID(t.Context(), row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if row.LastStatus != "SKIPPED" || storedScheduleSkipReason(t, db, row.ID) != "capacity" || row.LastRunAt == nil || !row.LastRunAt.Equal(next) || row.LastRunID != "" || row.ConsecutiveFailures != 2 {
		t.Fatalf("capacity skip outcome: %+v", row)
	}
}

func TestScheduleSkipReason_CatchupReplacesPreviousRunTimeAndLink(t *testing.T) {
	db, store, scheduler := newGoldenRig(t)
	goldenSeedTransformPipeline(t, db, "catchup-pipeline", "catchup-pipeline", "done")
	now := time.Now().UTC().Truncate(time.Minute)
	goldenInsertSchedule(t, db, goldenScheduleRow{ID: "catchup-schedule", TargetPipelineID: "catchup-pipeline", CronExpr: "* * * * *", NextRunAt: now})
	scheduler.nowFn = func() time.Time { return now }
	row, err := store.GetByID(t.Context(), "catchup-schedule")
	if err != nil {
		t.Fatal(err)
	}
	scheduler.fireOne(t.Context(), row)
	row, err = store.GetByID(t.Context(), row.ID)
	if err != nil || row.LastRunID == "" {
		t.Fatalf("initial execution missing: %+v %v", row, err)
	}
	if _, err := db.Exec(`UPDATE pipeline_schedules SET catchup_policy='skip',next_run_at=?,last_run_at=? WHERE id=?`, tsformat.Format(now.Add(-5*time.Minute)), tsformat.Format(now.Add(-time.Hour)), row.ID); err != nil {
		t.Fatal(err)
	}
	row, err = store.GetByID(t.Context(), row.ID)
	if err != nil {
		t.Fatal(err)
	}
	before := time.Now()
	scheduler.fireOne(t.Context(), row)
	after := time.Now()
	row, err = store.GetByID(t.Context(), row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if row.LastStatus != "SKIPPED" || storedScheduleSkipReason(t, db, row.ID) != "catchup" || row.LastMissedCount < 2 {
		t.Errorf("catchup reason/count not recorded: %+v", row)
	}
	if row.LastRunAt == nil || row.LastRunAt.Before(before) || row.LastRunAt.After(after) || row.LastRunID != "" {
		t.Errorf("skip retained an old execution's time/link: %+v", row)
	}
	if goldenRunCount(t, db, row.TargetPipelineID) != 1 {
		t.Fatal("catchup skip executed another run")
	}
}

func TestScheduleSkipReason_NonSkippedOutcomesClearReason(t *testing.T) {
	for _, status := range []string{"COMPLETED", "FAILED", "WAITING", "DEDUPED"} {
		t.Run(status, func(t *testing.T) {
			db, store, _ := newGoldenRig(t)
			goldenSeedTransformPipeline(t, db, "clear-pipeline", "clear-pipeline", "done")
			goldenInsertSchedule(t, db, goldenScheduleRow{ID: "clear-schedule", TargetPipelineID: "clear-pipeline", CronExpr: "* * * * *", NextRunAt: time.Now()})
			if _, err := db.Exec(`UPDATE pipeline_schedules SET last_status='SKIPPED',last_skip_reason='inactive' WHERE id='clear-schedule'`); err != nil {
				t.Fatal(err)
			}
			if err := store.recordRun(t.Context(), "clear-schedule", "", status, time.Now().Add(time.Minute), 0); err != nil {
				t.Fatal(err)
			}
			if got := storedScheduleSkipReason(t, db, "clear-schedule"); got != "" {
				t.Fatalf("%s left stale reason %q", status, got)
			}
		})
	}
}

// A wake decision is a separate telemetry channel. Its SKIPPED/HELD tick
// must not overwrite or clear the historical outcome of the main routine.
func TestScheduleSkipReason_WakeOnlyDecisionPreservesMainOutcome(t *testing.T) {
	for _, wakeStatus := range []string{WakeStatusSkipped, WakeStatusHeld} {
		t.Run(wakeStatus, func(t *testing.T) {
			db, store, scheduler := newGoldenRig(t)
			goldenSeedTransformPipeline(t, db, "wake-main", "wake-main", "done")
			goldenSeedTransformPipeline(t, db, "wake-probe", "wake-probe", "false")
			if wakeStatus == WakeStatusHeld {
				if _, err := db.Exec(`UPDATE pipelines SET status='disabled' WHERE id='wake-probe'`); err != nil {
					t.Fatal(err)
				}
			}
			now := time.Now().UTC().Truncate(time.Minute)
			scheduler.nowFn = func() time.Time { return now }
			goldenInsertSchedule(t, db, goldenScheduleRow{ID: "wake-schedule", TargetPipelineID: "wake-main", CronExpr: "* * * * *", NextRunAt: now, WakePipelineID: "wake-probe", WakeFailClosed: true})
			old := now.Add(-time.Hour)
			if _, err := db.Exec(`UPDATE pipeline_schedules SET last_status='SKIPPED',last_skip_reason='busy',last_run_at=? WHERE id='wake-schedule'`, tsformat.Format(old)); err != nil {
				t.Fatal(err)
			}
			row, err := store.GetByID(t.Context(), "wake-schedule")
			if err != nil {
				t.Fatal(err)
			}
			scheduler.fireOne(t.Context(), row)
			row, err = store.GetByID(t.Context(), row.ID)
			if err != nil {
				t.Fatal(err)
			}
			if row.LastStatus != "SKIPPED" || row.LastSkipReason != "busy" || row.LastRunAt == nil || !row.LastRunAt.Equal(old) || row.LastRunID != "" {
				t.Fatalf("wake decision changed main outcome: %+v", row)
			}
			if row.LastWakeStatus != wakeStatus || row.LastWakeAt == nil {
				t.Fatalf("wake telemetry missing: %+v", row)
			}
			if goldenRunCount(t, db, "wake-main") != 0 {
				t.Fatal("wake decision executed the main routine")
			}
		})
	}
}
