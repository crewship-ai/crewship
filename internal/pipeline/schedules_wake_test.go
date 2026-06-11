package pipeline

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Wake gates: a schedule may reference an agentless probe routine via
// wake_pipeline_id. On each cron tick the scheduler runs the probe
// first; the main routine fires only when the probe's final output is
// truthy (same falsey rule as step `if:`). Probe errors fail OPEN —
// monitoring schedules must not go silently blind because the probe
// broke.
// ---------------------------------------------------------------------------

// seedPipelineDef inserts a pipeline row with a caller-supplied
// definition so wake tests can run real (non-LLM) probe routines
// through the executor.
func seedPipelineDef(t *testing.T, db *sql.DB, id, slug, definitionJSON string) {
	t.Helper()
	_, err := db.ExecContext(context.Background(),
		`INSERT INTO pipelines (id, workspace_id, slug, name, definition_json, definition_hash)
		 VALUES (?, 'ws_test', ?, ?, ?, 'hash')`,
		id, slug, slug, definitionJSON)
	if err != nil {
		t.Fatalf("seed pipeline %s: %v", slug, err)
	}
}

// transformPipelineDef returns a minimal agentless definition whose
// single transform step echoes `output` — a deterministic, LLM-free
// probe the executor can run for real inside the test.
func transformPipelineDef(name, output string) string {
	return fmt.Sprintf(
		`{"dsl_version":"1.0","name":%q,"agentless":true,"steps":[{"id":"t","type":"transform","transform":{"input":%q,"expression":"."}}]}`,
		name, output)
}

func newWakeTestRig(t *testing.T) (*sql.DB, *ScheduleStore, *PipelineScheduler) {
	t.Helper()
	db := openScheduleTestDB(t)
	store := NewScheduleStore(db)
	pipelines := NewStore(db)
	exec := NewExecutor(pipelines, NewResolver(db), newMockRunner(), nil)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return db, store, NewPipelineScheduler(store, pipelines, exec, logger)
}

func mustSaveWakeSchedule(t *testing.T, store *ScheduleStore, wakePipelineID string) *Schedule {
	t.Helper()
	sched, err := store.Save(context.Background(), SaveScheduleInput{
		WorkspaceID:      "ws_test",
		Name:             "gated",
		TargetPipelineID: "pipe_main",
		CronExpr:         "* * * * *",
		WakePipelineID:   wakePipelineID,
		WakeInputs:       map[string]any{"threshold": "100"},
		Enabled:          true,
	})
	if err != nil {
		t.Fatalf("save schedule: %v", err)
	}
	return sched
}

func TestScheduleStore_Save_WakeFieldsRoundTrip(t *testing.T) {
	db, store, _ := newWakeTestRig(t)
	defer db.Close()
	seedPipelineDef(t, db, "pipe_main", "main", transformPipelineDef("main", "ran"))
	seedPipelineDef(t, db, "pipe_probe", "probe", transformPipelineDef("probe", "true"))

	sched := mustSaveWakeSchedule(t, store, "pipe_probe")
	if sched.WakePipelineID != "pipe_probe" {
		t.Errorf("wake_pipeline_id: got %q", sched.WakePipelineID)
	}
	if sched.WakeInputsJSON == "" || sched.WakeInputsJSON == "{}" {
		t.Errorf("wake_inputs_json should carry the inputs, got %q", sched.WakeInputsJSON)
	}
	if sched.WakeCheckCount != 0 || sched.WakeFireCount != 0 {
		t.Errorf("fresh schedule should have zero wake counters, got %d/%d", sched.WakeCheckCount, sched.WakeFireCount)
	}

	// Whole-row update with empty WakePipelineID clears the gate.
	cleared, err := store.Save(context.Background(), SaveScheduleInput{
		ID:               sched.ID,
		WorkspaceID:      "ws_test",
		Name:             "gated",
		TargetPipelineID: "pipe_main",
		CronExpr:         "* * * * *",
		Enabled:          true,
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if cleared.WakePipelineID != "" {
		t.Errorf("expected gate cleared, got %q", cleared.WakePipelineID)
	}
}

func TestScheduleStore_RecordWakeCheck(t *testing.T) {
	db, store, _ := newWakeTestRig(t)
	defer db.Close()
	seedPipelineDef(t, db, "pipe_main", "main", transformPipelineDef("main", "ran"))
	seedPipelineDef(t, db, "pipe_probe", "probe", transformPipelineDef("probe", "true"))
	sched := mustSaveWakeSchedule(t, store, "pipe_probe")

	next := time.Now().Add(1 * time.Hour)
	if err := store.recordWakeCheck(context.Background(), sched.ID, WakeStatusSkipped, next, true); err != nil {
		t.Fatalf("recordWakeCheck skip: %v", err)
	}
	got, _ := store.GetByID(context.Background(), sched.ID)
	if got.WakeCheckCount != 1 || got.WakeFireCount != 0 {
		t.Errorf("after skip: counters %d/%d, want 1/0", got.WakeCheckCount, got.WakeFireCount)
	}
	if got.LastWakeStatus != WakeStatusSkipped {
		t.Errorf("last_wake_status: got %q", got.LastWakeStatus)
	}
	if got.LastWakeAt == nil {
		t.Error("last_wake_at should be stamped")
	}
	if got.NextRunAt == nil || got.NextRunAt.Sub(next.UTC()).Abs() > time.Second {
		t.Errorf("skip must advance next_run_at to %v, got %v", next.UTC(), got.NextRunAt)
	}
	// Skips must NOT touch the main-run telemetry.
	if got.LastStatus != "" || got.LastRunID != "" || got.LastRunAt != nil {
		t.Errorf("skip must not touch last_run_* fields: %q %q %v", got.LastStatus, got.LastRunID, got.LastRunAt)
	}

	if err := store.recordWakeCheck(context.Background(), sched.ID, WakeStatusWoke, next, false); err != nil {
		t.Fatalf("recordWakeCheck woke: %v", err)
	}
	got, _ = store.GetByID(context.Background(), sched.ID)
	if got.WakeCheckCount != 2 || got.WakeFireCount != 1 {
		t.Errorf("after woke: counters %d/%d, want 2/1", got.WakeCheckCount, got.WakeFireCount)
	}
}

func TestPipelineScheduler_FireOne_WakeGateSkips(t *testing.T) {
	db, store, sched := newWakeTestRig(t)
	defer db.Close()
	seedPipelineDef(t, db, "pipe_main", "main", transformPipelineDef("main", "ran"))
	seedPipelineDef(t, db, "pipe_probe", "probe", transformPipelineDef("probe", "false"))
	row := mustSaveWakeSchedule(t, store, "pipe_probe")

	sched.fireOne(context.Background(), row)

	got, _ := store.GetByID(context.Background(), row.ID)
	if got.LastWakeStatus != WakeStatusSkipped {
		t.Errorf("last_wake_status: got %q, want SKIPPED", got.LastWakeStatus)
	}
	if got.WakeCheckCount != 1 || got.WakeFireCount != 0 {
		t.Errorf("counters: got %d/%d, want 1/0", got.WakeCheckCount, got.WakeFireCount)
	}
	// Main routine must NOT have run.
	if got.LastStatus != "" || got.LastRunID != "" {
		t.Errorf("main run telemetry must stay empty on skip, got status=%q run=%q", got.LastStatus, got.LastRunID)
	}
	if got.NextRunAt == nil || !got.NextRunAt.After(time.Now()) {
		t.Errorf("skip must still advance next_run_at, got %v", got.NextRunAt)
	}
}

func TestPipelineScheduler_FireOne_WakeGateWakes(t *testing.T) {
	db, store, sched := newWakeTestRig(t)
	defer db.Close()
	seedPipelineDef(t, db, "pipe_main", "main", transformPipelineDef("main", "ran"))
	seedPipelineDef(t, db, "pipe_probe", "probe", transformPipelineDef("probe", "true"))
	row := mustSaveWakeSchedule(t, store, "pipe_probe")

	sched.fireOne(context.Background(), row)

	got, _ := store.GetByID(context.Background(), row.ID)
	if got.LastWakeStatus != WakeStatusWoke {
		t.Errorf("last_wake_status: got %q, want WOKE", got.LastWakeStatus)
	}
	if got.WakeCheckCount != 1 || got.WakeFireCount != 1 {
		t.Errorf("counters: got %d/%d, want 1/1", got.WakeCheckCount, got.WakeFireCount)
	}
	if got.LastStatus != "COMPLETED" {
		t.Errorf("main run should have completed, got %q", got.LastStatus)
	}
	if got.LastRunID == "" {
		t.Error("last_run_id should point at the main run")
	}
}

func TestPipelineScheduler_FireOne_WakeGateFailsOpen(t *testing.T) {
	// Probe pipeline id points nowhere → probe run errors → fail
	// OPEN: the main routine fires anyway and the wake status records
	// ERROR so the operator can see the probe is broken.
	db, store, sched := newWakeTestRig(t)
	defer db.Close()
	seedPipelineDef(t, db, "pipe_main", "main", transformPipelineDef("main", "ran"))
	row := mustSaveWakeSchedule(t, store, "pipe_ghost")

	sched.fireOne(context.Background(), row)

	got, _ := store.GetByID(context.Background(), row.ID)
	if got.LastWakeStatus != WakeStatusError {
		t.Errorf("last_wake_status: got %q, want ERROR", got.LastWakeStatus)
	}
	if got.LastStatus != "COMPLETED" {
		t.Errorf("fail-open: main run should have completed, got %q", got.LastStatus)
	}
	if got.WakeCheckCount != 1 || got.WakeFireCount != 1 {
		t.Errorf("counters: got %d/%d, want 1/1", got.WakeCheckCount, got.WakeFireCount)
	}
}

func TestPipelineScheduler_FireOne_NoGate_Unchanged(t *testing.T) {
	// Regression pin: schedules without a wake gate keep today's
	// behaviour — fire immediately, no wake telemetry.
	db, store, sched := newWakeTestRig(t)
	defer db.Close()
	seedPipelineDef(t, db, "pipe_main", "main", transformPipelineDef("main", "ran"))
	row := mustSaveWakeSchedule(t, store, "")

	sched.fireOne(context.Background(), row)

	got, _ := store.GetByID(context.Background(), row.ID)
	if got.LastStatus != "COMPLETED" {
		t.Errorf("main run should have completed, got %q", got.LastStatus)
	}
	if got.WakeCheckCount != 0 || got.LastWakeStatus != "" {
		t.Errorf("no gate → no wake telemetry, got count=%d status=%q", got.WakeCheckCount, got.LastWakeStatus)
	}
}
