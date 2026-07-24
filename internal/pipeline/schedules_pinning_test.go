package pipeline

// Version pinning + WAITING outcome tests for the scheduler fire path.
//
// Defect 1 (dead pin): schedules persist target_pipeline_version but
// fireOne always loaded HEAD via Store.GetByID — the pin was write-only
// config and every fire executed the head definition, defeating the
// documented promise that pinning protects production schedules from
// agent edits. These tests pin the fixed contract:
//
//   - a schedule pinned to v1 executes v1's definition even after the
//     routine's head moved to v2 (run row stamped with the executed
//     pipeline_version + the pinned definition_hash);
//   - a pin pointing at a version that no longer exists FAILS the fire
//     with a legible error + the scheduled-failure inbox alert — it
//     must NOT silently fall back to head (that recreates the bug);
//   - an unpinned schedule keeps executing head (regression guard);
//   - a pinned run that parked mid-flight resumes against the PINNED
//     definition, not head, so a head edit can't strand it.
//
// Defect 2 (WAITING misrecorded): a cron-fired routine that parks on a
// wait:approval step is a healthy run, but fireOne recorded it as
// last_status=FAILED and raised the failed-run MANAGER alert — a false
// alarm on every scheduled approval gate. Pinned contract: WAITING
// records last_status=WAITING and no alert fires.

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// openPinningTestDB layers pipeline_versions + inbox_items on top of
// the factory-test schema (pipelines, pipeline_runs, waitpoints,
// schedules, step overrides) so Store.Save's dual-write appends real
// version rows and the scheduler's failure alert has a table to land
// in.
func openPinningTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db := openFactoryTestDB(t)
	if _, err := db.ExecContext(context.Background(), `
ALTER TABLE pipelines ADD COLUMN head_version INTEGER NOT NULL DEFAULT 0;
CREATE TABLE IF NOT EXISTS pipeline_versions (
    id              TEXT PRIMARY KEY,
    pipeline_id     TEXT NOT NULL,
    version         INTEGER NOT NULL,
    definition_json TEXT NOT NULL,
    definition_hash TEXT NOT NULL,
    author_type     TEXT NOT NULL,
    author_id       TEXT NOT NULL,
    parent_version  INTEGER,
    change_summary  TEXT,
    created_at      TEXT NOT NULL,
    UNIQUE (pipeline_id, version),
    UNIQUE (pipeline_id, definition_hash)
);
CREATE TABLE IF NOT EXISTS inbox_items (
    id                  TEXT PRIMARY KEY,
    workspace_id        TEXT NOT NULL,
    kind                TEXT NOT NULL,
    source_id           TEXT NOT NULL,
    target_user_id      TEXT,
    target_role         TEXT,
    title               TEXT NOT NULL,
    body_md             TEXT,
    sender_type         TEXT,
    sender_id           TEXT,
    sender_name         TEXT,
    state               TEXT NOT NULL,
    priority            TEXT NOT NULL,
    blocking            INTEGER NOT NULL DEFAULT 0,
    payload_json        TEXT NOT NULL DEFAULT '{}',
    resolved_at         TEXT,
    resolved_by_user_id TEXT,
    resolved_action     TEXT,
    created_at          TEXT NOT NULL,
    updated_at          TEXT NOT NULL,
    UNIQUE (kind, source_id)
);`); err != nil {
		t.Fatalf("pinning schema: %v", err)
	}
	return db
}

// Two versions of the same routine with disjoint step ids so run-row
// step outputs unambiguously identify WHICH definition executed.
const (
	pinV1DSL = `{"dsl_version":"1.0","name":"pin-target","steps":[{"id":"v1step","type":"transform","transform":{"input":"v1-out","expression":"."}}]}`
	pinV2DSL = `{"dsl_version":"1.0","name":"pin-target","steps":[{"id":"v2step","type":"transform","transform":{"input":"v2-out","expression":"."}}]}`
)

type pinningRig struct {
	db        *sql.DB
	deps      ExecutorDeps
	store     *ScheduleStore
	scheduler *PipelineScheduler
}

func newPinningRig(t *testing.T) *pinningRig {
	t.Helper()
	db := openPinningTestDB(t)
	t.Cleanup(func() { _ = db.Close() })
	deps := fullExecutorDeps(t, db, newMockRunner())
	exec := NewWiredExecutor(deps)
	store := NewScheduleStore(db)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return &pinningRig{
		db:        db,
		deps:      deps,
		store:     store,
		scheduler: NewPipelineScheduler(store, deps.Store, exec, logger),
	}
}

func (r *pinningRig) saveSchedule(t *testing.T, pipelineID string, pinnedVersion *int) *Schedule {
	t.Helper()
	sched, err := r.store.Save(context.Background(), SaveScheduleInput{
		WorkspaceID:           "ws_test",
		Name:                  "pin-sched",
		TargetPipelineID:      pipelineID,
		TargetPipelineVersion: pinnedVersion,
		CronExpr:              "0 8 * * *",
		Timezone:              "UTC",
		Enabled:               true,
	})
	if err != nil {
		t.Fatalf("save schedule: %v", err)
	}
	return sched
}

func countFailedRunAlerts(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM inbox_items WHERE kind = 'failed_run'`).Scan(&n); err != nil {
		t.Fatalf("count inbox alerts: %v", err)
	}
	return n
}

// TestSchedulerFire_PinnedVersion_ExecutesPinnedDefinition is the core
// defect-1 pin: schedule pinned to v1 + head moved to v2 → the fire
// executes v1's definition and stamps the run row with the executed
// version + the pinned definition hash.
func TestSchedulerFire_PinnedVersion_ExecutesPinnedDefinition(t *testing.T) {
	rig := newPinningRig(t)
	ctx := context.Background()

	p := saveResumePipeline(t, rig.deps.Store, "pin-target", pinV1DSL) // v1
	_ = saveResumePipeline(t, rig.deps.Store, "pin-target", pinV2DSL)  // head → v2

	one := 1
	sched := rig.saveSchedule(t, p.ID, &one)
	rig.scheduler.fireOne(ctx, sched)

	got, err := rig.store.GetByID(ctx, sched.ID)
	if err != nil {
		t.Fatalf("get schedule: %v", err)
	}
	if got.LastStatus != "COMPLETED" {
		t.Fatalf("last_status: got %q, want COMPLETED", got.LastStatus)
	}
	if got.LastRunID == "" {
		t.Fatal("last_run_id empty — run never recorded")
	}
	rec, err := rig.deps.RunStore.Get(ctx, got.LastRunID)
	if err != nil {
		t.Fatalf("load run row: %v", err)
	}
	if rec.PipelineVersion == nil || *rec.PipelineVersion != 1 {
		t.Errorf("run pipeline_version: got %v, want 1 (executed version must be recorded)", rec.PipelineVersion)
	}
	if want := definitionHash(pinV1DSL); rec.DefinitionHash != want {
		t.Errorf("run definition_hash: got %q, want the PINNED v1 hash %q", rec.DefinitionHash, want)
	}
	outputs, err := rig.deps.RunStore.GetStepOutputs(ctx, rec.ID)
	if err != nil {
		t.Fatalf("get step outputs: %v", err)
	}
	if _, ok := outputs["v1step"]; !ok {
		t.Errorf("step outputs %#v missing v1step — the pinned v1 definition did not execute", outputs)
	}
	if _, ok := outputs["v2step"]; ok {
		t.Errorf("step outputs %#v contain v2step — HEAD executed despite the pin", outputs)
	}
}

// TestSchedulerFire_Unpinned_ExecutesHead is the regression guard for
// the default path: no pin → head executes, run row carries no pinned
// version.
func TestSchedulerFire_Unpinned_ExecutesHead(t *testing.T) {
	rig := newPinningRig(t)
	ctx := context.Background()

	p := saveResumePipeline(t, rig.deps.Store, "pin-target", pinV1DSL)
	_ = saveResumePipeline(t, rig.deps.Store, "pin-target", pinV2DSL)

	sched := rig.saveSchedule(t, p.ID, nil)
	rig.scheduler.fireOne(ctx, sched)

	got, _ := rig.store.GetByID(ctx, sched.ID)
	if got.LastStatus != "COMPLETED" {
		t.Fatalf("last_status: got %q, want COMPLETED", got.LastStatus)
	}
	rec, err := rig.deps.RunStore.Get(ctx, got.LastRunID)
	if err != nil {
		t.Fatalf("load run row: %v", err)
	}
	outputs, err := rig.deps.RunStore.GetStepOutputs(ctx, rec.ID)
	if err != nil {
		t.Fatalf("get step outputs: %v", err)
	}
	if _, ok := outputs["v2step"]; !ok {
		t.Errorf("unpinned schedule must execute HEAD (v2), got outputs %#v", outputs)
	}
	if rec.PipelineVersion != nil {
		t.Errorf("unpinned run should not stamp a pinned version, got %d", *rec.PipelineVersion)
	}
}

// TestSchedulerFire_PinnedVersionMissing_FailsWithAlert: the pin points
// at a version that doesn't exist. The fire must FAIL legibly + alert —
// never fall back to head (a silent fallback IS the original defect).
func TestSchedulerFire_PinnedVersionMissing_FailsWithAlert(t *testing.T) {
	rig := newPinningRig(t)
	ctx := context.Background()

	p := saveResumePipeline(t, rig.deps.Store, "pin-target", pinV1DSL) // only v1 exists

	ninetyNine := 99
	sched := rig.saveSchedule(t, p.ID, &ninetyNine)
	rig.scheduler.fireOne(ctx, sched)

	got, _ := rig.store.GetByID(ctx, sched.ID)
	if got.LastStatus != "FAILED" {
		t.Errorf("last_status: got %q, want FAILED (missing pinned version must not silently run head)", got.LastStatus)
	}
	if got.LastRunID != "" {
		t.Errorf("last_run_id should stay empty (no run started), got %q", got.LastRunID)
	}
	if got.NextRunAt == nil {
		t.Error("next_run_at must still advance after a failed fire")
	}

	// No head execution: zero run rows.
	var runCount int
	if err := rig.db.QueryRow(`SELECT COUNT(*) FROM pipeline_runs`).Scan(&runCount); err != nil {
		t.Fatal(err)
	}
	if runCount != 0 {
		t.Errorf("pipeline_runs rows: got %d, want 0 — a missing pinned version must not execute anything", runCount)
	}

	// The failed-fire MANAGER alert landed and names the missing version.
	if n := countFailedRunAlerts(t, rig.db); n != 1 {
		t.Fatalf("failed_run inbox alerts: got %d, want 1", n)
	}
	var body string
	if err := rig.db.QueryRow(`SELECT body_md FROM inbox_items WHERE kind = 'failed_run'`).Scan(&body); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "version 99") {
		t.Errorf("alert body %q should name the missing version so the operator can fix the pin", body)
	}
}

// TestSchedulerFire_WaitingRun_RecordsWaitingNoAlert is the defect-2
// pin: a cron-fired routine parking on a wait:approval step is healthy
// — last_status must say WAITING (not FAILED) and no failed-run alert
// may fire.
func TestSchedulerFire_WaitingRun_RecordsWaitingNoAlert(t *testing.T) {
	rig := newPinningRig(t)
	ctx := context.Background()

	runner := newMockRunner()
	runner.outputsBySlug["s_a"] = []string{"out-a"}
	deps := fullExecutorDeps(t, rig.db, runner)
	exec := NewWiredExecutor(deps)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	scheduler := NewPipelineScheduler(rig.store, deps.Store, exec, logger)

	p := saveResumePipeline(t, deps.Store, "cron-wait", factoryWaitDSL)
	sched := rig.saveSchedule(t, p.ID, nil)

	done := make(chan struct{})
	go func() {
		defer close(done)
		scheduler.fireOne(ctx, sched)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("fireOne did not return within 10s")
	}

	got, _ := rig.store.GetByID(ctx, sched.ID)
	if got.LastStatus != "WAITING" {
		t.Errorf("last_status: got %q, want WAITING — a parked approval gate is not a failure", got.LastStatus)
	}
	if got.LastRunID == "" {
		t.Error("last_run_id should point at the parked run")
	}
	if n := countFailedRunAlerts(t, rig.db); n != 0 {
		t.Errorf("failed_run alerts: got %d, want 0 — WAITING must not raise the failed-run alarm", n)
	}
}

// TestResume_PinnedRun_ResumesPinnedDefinition: a pinned run that was
// interrupted mid-flight must resume against the PINNED definition. The
// hash drift gate compares the run's stamped hash against what will
// execute — before the fix that was head, so any head edit stranded
// every parked pinned run as interrupted.
func TestResume_PinnedRun_ResumesPinnedDefinition(t *testing.T) {
	rig := newPinningRig(t)
	ctx := context.Background()

	p := saveResumePipeline(t, rig.deps.Store, "pin-target", pinV1DSL) // v1
	_ = saveResumePipeline(t, rig.deps.Store, "pin-target", pinV2DSL)  // head → v2

	one := 1
	insertInFlightRun(t, rig.deps.RunStore, &RunRecord{
		ID:              "run_pin_resume",
		WorkspaceID:     "ws_test",
		PipelineID:      p.ID,
		PipelineSlug:    p.Slug,
		Status:          RunStatusRunning,
		Mode:            ModeRun,
		PipelineVersion: &one,
		DefinitionHash:  definitionHash(pinV1DSL),
		CurrentStepID:   "v1step",
	})

	exec := NewWiredExecutor(rig.deps)
	resumed, interrupted, err := exec.ResumeInterruptedRuns(ctx, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("resume scan: %v", err)
	}
	if resumed != 1 || interrupted != 0 {
		t.Fatalf("resumed=%d interrupted=%d, want 1/0 — pinned run must resume against its pinned definition, not head", resumed, interrupted)
	}
	rec := waitForRunStatus(t, rig.deps.RunStore, "run_pin_resume", RunStatusCompleted, 5*time.Second)
	outputs, err := rig.deps.RunStore.GetStepOutputs(ctx, rec.ID)
	if err != nil {
		t.Fatalf("get step outputs: %v", err)
	}
	if _, ok := outputs["v1step"]; !ok {
		t.Errorf("resumed outputs %#v missing v1step — resume did not execute the pinned definition", outputs)
	}
}
