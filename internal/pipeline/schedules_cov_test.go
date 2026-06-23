package pipeline

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// schedules.go — Save validation + marshal failures, closed-DB error
// paths, fireOne's pipeline-load / run-failure / record-failure
// branches, runWakeCheck's non-completed-probe branch, nullInt.
// ---------------------------------------------------------------------------

func TestScheduleStore_Save_Validation_Cov(t *testing.T) {
	db := openScheduleTestDB(t)
	defer db.Close()
	store := NewScheduleStore(db)
	ctx := context.Background()

	if _, err := store.Save(ctx, SaveScheduleInput{TargetPipelineID: "p", CronExpr: "* * * * *"}); err == nil || !strings.Contains(err.Error(), "required") {
		t.Errorf("missing workspace: %v", err)
	}
	if _, err := store.Save(ctx, SaveScheduleInput{WorkspaceID: "ws", CronExpr: "* * * * *"}); err == nil || !strings.Contains(err.Error(), "required") {
		t.Errorf("missing pipeline: %v", err)
	}
	if _, err := store.Save(ctx, SaveScheduleInput{WorkspaceID: "ws", TargetPipelineID: "p"}); err == nil || !strings.Contains(err.Error(), "required") {
		t.Errorf("missing cron: %v", err)
	}

	// Unmarshalable Inputs map → marshal error.
	if _, err := store.Save(ctx, SaveScheduleInput{
		WorkspaceID: "ws_test", TargetPipelineID: "p", CronExpr: "* * * * *",
		Inputs: map[string]any{"bad": make(chan int)},
	}); err == nil || !strings.Contains(err.Error(), "marshal inputs") {
		t.Errorf("bad inputs: %v", err)
	}

	// Unmarshalable WakeInputs map → marshal error.
	if _, err := store.Save(ctx, SaveScheduleInput{
		WorkspaceID: "ws_test", TargetPipelineID: "p", CronExpr: "* * * * *",
		WakeInputs: map[string]any{"bad": make(chan int)},
	}); err == nil || !strings.Contains(err.Error(), "marshal wake inputs") {
		t.Errorf("bad wake inputs: %v", err)
	}
}

func TestScheduleStore_ClosedDB_ErrorPaths(t *testing.T) {
	db := openScheduleTestDB(t)
	store := NewScheduleStore(db)
	ctx := context.Background()
	_ = db.Close()

	if _, err := store.Save(ctx, SaveScheduleInput{WorkspaceID: "ws", TargetPipelineID: "p", CronExpr: "* * * * *"}); err == nil || !strings.Contains(err.Error(), "insert schedule") {
		t.Errorf("Save insert: %v", err)
	}
	if _, err := store.Save(ctx, SaveScheduleInput{ID: "psched_x", WorkspaceID: "ws", TargetPipelineID: "p", CronExpr: "* * * * *"}); err == nil || !strings.Contains(err.Error(), "update schedule") {
		t.Errorf("Save update: %v", err)
	}
	if _, err := store.GetByID(ctx, "x"); err == nil {
		t.Error("GetByID should error on closed DB")
	}
	if _, err := store.List(ctx, "ws"); err == nil {
		t.Error("List should error on closed DB")
	}
	if err := store.SoftDelete(ctx, "x"); err == nil {
		t.Error("SoftDelete should error on closed DB")
	}
	if _, err := store.listDueSchedules(ctx); err == nil {
		t.Error("listDueSchedules should error on closed DB")
	}
	if err := store.recordRun(ctx, "x", "r", "COMPLETED", time.Now().UTC()); err == nil {
		t.Error("recordRun should error on closed DB")
	}
	if err := store.recordWakeCheck(ctx, "x", WakeStatusWoke, time.Now().UTC(), false); err == nil {
		t.Error("recordWakeCheck (no advance) should error on closed DB")
	}
	if err := store.recordWakeCheck(ctx, "x", WakeStatusSkipped, time.Now().UTC(), true); err == nil {
		t.Error("recordWakeCheck (advance) should error on closed DB")
	}
}

func TestScheduleStore_SoftDelete_NotFound_Cov(t *testing.T) {
	db := openScheduleTestDB(t)
	defer db.Close()
	store := NewScheduleStore(db)
	if err := store.SoftDelete(context.Background(), "psched_ghost"); !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

// TestPipelineScheduler_FireOne_PipelineLoadFails covers the branch
// where the main pipeline row is gone at fire time: the schedule must
// record a FAILED run (with empty run id) and advance next_run_at.
func TestPipelineScheduler_FireOne_PipelineLoadFails(t *testing.T) {
	db, store, sched := newWakeTestRig(t)
	defer db.Close()
	// No pipeline seeded for pipe_main.
	row := mustSaveWakeSchedule(t, store, "")

	sched.fireOne(context.Background(), row)

	got, err := store.GetByID(context.Background(), row.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.LastStatus != "FAILED" {
		t.Errorf("last_status: %q, want FAILED", got.LastStatus)
	}
	if got.LastRunID != "" {
		t.Errorf("last_run_id should be empty on load failure, got %q", got.LastRunID)
	}
	if got.NextRunAt == nil {
		t.Error("next_run_at should still advance")
	}
}

// TestPipelineScheduler_FireOne_RunErrorRecordsFailed covers the
// runErr != nil branch: a stored definition that fails to parse makes
// executor.Run return an error and nil result.
func TestPipelineScheduler_FireOne_RunErrorRecordsFailed(t *testing.T) {
	db, store, sched := newWakeTestRig(t)
	defer db.Close()
	seedPipelineDef(t, db, "pipe_main", "main", "this is not json")
	row := mustSaveWakeSchedule(t, store, "")

	sched.fireOne(context.Background(), row)

	got, _ := store.GetByID(context.Background(), row.ID)
	if got.LastStatus != "FAILED" {
		t.Errorf("last_status: %q, want FAILED", got.LastStatus)
	}
}

// TestPipelineScheduler_FireOne_RecordRunFailureIsLogged covers the
// recordRun error branch: the schedules table disappears between the
// fire and the record write. fireOne must not panic.
func TestPipelineScheduler_FireOne_RecordRunFailureIsLogged(t *testing.T) {
	db, store, sched := newWakeTestRig(t)
	defer db.Close()
	seedPipelineDef(t, db, "pipe_main", "main", transformPipelineDef("main", "ran"))
	row := mustSaveWakeSchedule(t, store, "")
	_ = store // row already loaded; drop the table under the scheduler
	if _, err := db.Exec(`DROP TABLE pipeline_schedules`); err != nil {
		t.Fatalf("drop: %v", err)
	}

	sched.fireOne(context.Background(), row) // must not panic
}

// TestPipelineScheduler_RunWakeCheck_ProbeFailedRun covers the branch
// where the probe run itself returns a result with a non-COMPLETED
// status (run error captured in res.ErrorMessage) → fail OPEN, ERROR.
func TestPipelineScheduler_RunWakeCheck_ProbeFailedRun(t *testing.T) {
	db, store, sched := newWakeTestRig(t)
	defer db.Close()
	seedPipelineDef(t, db, "pipe_main", "main", transformPipelineDef("main", "ran"))
	// Probe routine whose only step is an unsupported type → run
	// completes with Status=FAILED (no Go error).
	seedPipelineDef(t, db, "pipe_probe", "probe",
		`{"dsl_version":"1.0","name":"probe","steps":[{"id":"x","type":"transform","transform":{"input":"{{ steps.ghost.output }}","expression":"MISSING("}}]}`)
	row := mustSaveWakeSchedule(t, store, "pipe_probe")

	proceed, status := sched.runWakeCheck(context.Background(), row)
	if !proceed {
		t.Error("failed probe must fail OPEN (proceed=true)")
	}
	if status != WakeStatusError {
		t.Errorf("status: %q, want ERROR", status)
	}
}

// TestScanSchedule_AllNullableBranches scans a row carrying every
// optional column (pinned version, run + wake telemetry, deleted_at)
// directly, since the public lookups filter soft-deleted rows out.
func TestScanSchedule_AllNullableBranches(t *testing.T) {
	db := openScheduleTestDB(t)
	defer db.Close()

	if _, err := db.Exec(`
INSERT INTO pipeline_schedules (
    id, workspace_id, name, target_pipeline_id, target_pipeline_version,
    cron_expr, timezone, inputs_json, enabled,
    last_run_at, last_status, last_run_id, next_run_at,
    wake_pipeline_id, wake_inputs_json, wake_check_count, wake_fire_count,
    last_wake_at, last_wake_status, created_at, updated_at, deleted_at
) VALUES (
    'psched_full', 'ws_test', 'full', 'pln_x', 4,
    '* * * * *', 'UTC', '{}', 1,
    '2026-01-01T01:00:00Z', 'COMPLETED', 'run_9', '2026-01-01T02:00:00Z',
    'pln_probe', '{"a":1}', 7, 3,
    '2026-01-01T01:30:00Z', 'WOKE', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', '2026-01-02T00:00:00Z'
)`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	rows, err := db.Query(scheduleSelect + ` WHERE id = 'psched_full'`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatal("row missing")
	}
	s, err := scanSchedule(rows)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if s.TargetPipelineVersion == nil || *s.TargetPipelineVersion != 4 {
		t.Errorf("pinned version: %v", s.TargetPipelineVersion)
	}
	if s.LastRunAt == nil || s.NextRunAt == nil || s.LastWakeAt == nil {
		t.Errorf("timestamps lost: %v %v %v", s.LastRunAt, s.NextRunAt, s.LastWakeAt)
	}
	if s.DeletedAt == nil {
		t.Error("deleted_at lost")
	}
	if s.WakeCheckCount != 7 || s.WakeFireCount != 3 || s.LastWakeStatus != "WOKE" {
		t.Errorf("wake telemetry: %d/%d %q", s.WakeCheckCount, s.WakeFireCount, s.LastWakeStatus)
	}
}

func TestNullIntAndBoolToInt(t *testing.T) {
	t.Parallel()
	if v, ok := nullInt(nil).(sql.NullInt64); !ok || v.Valid {
		t.Errorf("nil *int should map to invalid NullInt64, got %v", nullInt(nil))
	}
	x := 9
	if nullInt(&x) != 9 {
		t.Errorf("non-nil *int should deref")
	}
	if boolToInt(true) != 1 || boolToInt(false) != 0 {
		t.Error("boolToInt mapping wrong")
	}
}

func TestGenerateScheduleID_Format(t *testing.T) {
	t.Parallel()
	id1 := generateScheduleID()
	id2 := generateScheduleID()
	if !strings.HasPrefix(id1, "psched_c") {
		t.Errorf("prefix: %q", id1)
	}
	if id1 == id2 {
		t.Error("ids must be unique")
	}
}
