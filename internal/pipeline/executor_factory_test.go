package pipeline

// executor_factory.go — NewWiredExecutor, the ONE shared construction
// path for production executors.
//
// Background (the defect class these tests close): the executor used to
// be hand-assembled at four call sites — HTTP handler (newExecutor in
// internal/api/pipelines.go), boot-resume scan + cron scheduler +
// pending-run dispatcher (cmd/crewship/cmd_start.go) — and every site
// wired a DIFFERENT subset of the With* options. Features proven on the
// HTTP path silently failed on the unattended paths: a cron-fired
// routine with a wait:approval step hit the nil-WaitpointStore 60s
// fallback ("works when I click Run, breaks at 3 AM"), a resumed run
// with a code step failed "no CodeRunner wired", resumed runs dropped
// step overrides and failed wait:event immediately.
//
// The tests below pin, in order:
//   (d) construction: the factory wires EVERY dependency (explicit
//       assertions + a reflection sweep so a future With* option cannot
//       be forgotten on one path again);
//   (a) scheduler fire path: a cron-fired wait:approval routine PARKS
//       as waiting + registers a waitpoint instead of failing;
//   (b) boot resume: a resumed run with a type:code step executes;
//   (c) boot resume: a resumed run applies per-step overrides.

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"reflect"
	"testing"
	"time"
)

// openFactoryTestDB layers every schema the fully-wired executor can
// touch at run time on top of the resume-test schema: pipelines +
// pipeline_runs + pipeline_waitpoints (openResumeTestDB), plus
// pipeline_schedules (scheduler fire path) and routine_step_overrides
// (v123 override layer, queried at every run start when wired).
func openFactoryTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db := openResumeTestDB(t)
	if _, err := db.ExecContext(context.Background(), scheduleSchemaSQL); err != nil {
		t.Fatalf("schedule schema: %v", err)
	}
	// A factory-wired executor wires the idempotency store whenever DB != nil
	// (NewWiredExecutor), so the rig must carry the idempotency table to match
	// production — the cron/deferred trigger paths now always set a key.
	if _, err := db.ExecContext(context.Background(), idempotencySchemaSQL); err != nil {
		t.Fatalf("idempotency schema: %v", err)
	}
	if _, err := db.ExecContext(context.Background(), `
CREATE TABLE IF NOT EXISTS routine_step_overrides (
    pipeline_id    TEXT NOT NULL,
    workspace_id   TEXT NOT NULL,
    step_id        TEXT NOT NULL,
    prompt         TEXT,
    model_override TEXT,
    updated_at     TEXT NOT NULL DEFAULT (datetime('now','subsec')),
    PRIMARY KEY (pipeline_id, step_id)
);`); err != nil {
		t.Fatalf("step overrides schema: %v", err)
	}
	// wait:event durability (#1409, migration v154) — NewWiredExecutor
	// wires a SignalWaitStore whenever DB != nil, so the rig must carry
	// the table to match production.
	if _, err := db.ExecContext(context.Background(), `
CREATE TABLE IF NOT EXISTS pipeline_signal_waits (
    id           TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    run_id       TEXT NOT NULL,
    step_id      TEXT NOT NULL,
    event_type   TEXT NOT NULL,
    status       TEXT NOT NULL DEFAULT 'pending',
    payload      TEXT,
    created_at   TEXT NOT NULL,
    delivered_at TEXT,
    consumed_at  TEXT,
    UNIQUE (run_id, step_id)
);`); err != nil {
		t.Fatalf("signal waits schema: %v", err)
	}
	return db
}

// fullExecutorDeps builds an ExecutorDeps with every field populated —
// the shape every production call site is expected to pass.
func fullExecutorDeps(t *testing.T, db *sql.DB, runner AgentRunner) ExecutorDeps {
	t.Helper()
	wpStore := NewSQLWaitpointStore(db)
	t.Cleanup(wpStore.Close)
	return ExecutorDeps{
		Store:        NewStore(db),
		Resolver:     NewResolver(db),
		Runner:       runner,
		Emitter:      &captureEmitter{},
		DB:           db,
		Waitpoints:   wpStore,
		WS:           &captureWSBroadcaster{},
		Runs:         NewRunRegistry(),
		RunStore:     NewRunStore(db),
		CodeRunner:   NewMultiCodeRunner(),
		Signals:      NewSignalRegistry(),
		ScriptRunner: &fakeScriptRunner{},
	}
}

// TestNewWiredExecutor_WiresEveryDependency is the regression guard for
// the whole per-call-site-drift class. Two layers:
//
//  1. Explicit: every dependency the HTTP reference path wires must land
//     on the executor the factory builds.
//  2. Reflection sweep: EVERY dependency-shaped field (pointer /
//     interface / func / map) on Executor must be non-nil after a
//     full-deps construction, unless it is explicitly allowlisted below
//     with a reason. A future WithNewCapability that adds a field but
//     not a factory wire fails here — instead of silently working on
//     one path and failing at 3 AM on another.
func TestNewWiredExecutor_WiresEveryDependency(t *testing.T) {
	db := openFactoryTestDB(t)
	defer db.Close()

	exec := NewWiredExecutor(fullExecutorDeps(t, db, newMockRunner()))
	if exec == nil {
		t.Fatal("NewWiredExecutor returned nil")
	}

	// Layer 1 — explicit field assertions (same fields the HTTP
	// handler's newExecutor has always wired).
	checks := map[string]bool{
		"store":            exec.store != nil,
		"resolver":         exec.resolver != nil,
		"runner":           exec.runner != nil,
		"emitter":          exec.emitter != nil,
		"waitpoints":       exec.waitpoints != nil,
		"ws":               exec.ws != nil,
		"runs":             exec.runs != nil,
		"idempotency":      exec.idempotency != nil,
		"stepOverrides":    exec.stepOverrides != nil,
		"runStore":         exec.runStore != nil,
		"codeRunner":       exec.codeRunner != nil,
		"scriptRunner":     exec.scriptRunner != nil,
		"signals":          exec.signals != nil,
		"egressAllowed":    exec.egressAllowed != nil,
		"credentialByType": exec.credentialByType != nil,
	}
	for field, ok := range checks {
		if !ok {
			t.Errorf("Executor.%s is nil after NewWiredExecutor with full deps", field)
		}
	}

	// Layer 2 — reflection sweep. Fields listed here are the ONLY ones
	// allowed to stay nil on a fully-wired executor; add a new field
	// here only with a documented reason, otherwise wire it in
	// NewWiredExecutor (and ExecutorDeps). The list is EMPTY on
	// purpose: the last two residents (egressAllowed, credentialByType)
	// were the http-step security gap — DSL-validated egress_targets /
	// credential_ref that no production executor actually enforced or
	// resolved. Do not re-grow this list for security-relevant fields.
	notFactoryWired := map[string]string{
		// Test-only injection points for the retry backoff clock — nil in
		// production (real timer sleep + full jitter via math/rand); set
		// only from tests to drive the schedule deterministically.
		"sleepFn":  "test-only retry-clock override; production uses real time",
		"jitterFn": "test-only retry-jitter override; production uses math/rand",
	}
	v := reflect.ValueOf(exec).Elem()
	typ := v.Type()
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		if _, exempt := notFactoryWired[f.Name]; exempt {
			continue
		}
		fv := v.Field(i)
		switch fv.Kind() {
		case reflect.Ptr, reflect.Interface, reflect.Func, reflect.Map, reflect.Slice, reflect.Chan:
			if fv.IsNil() {
				t.Errorf("Executor.%s is nil after NewWiredExecutor with full deps — a new dependency was added without factory wiring; add it to ExecutorDeps + NewWiredExecutor (or allowlist it here with a reason)", f.Name)
			}
		default:
			// scalars (bool, time.Time, time.Duration) are per-run/
			// per-site tuning, not injectable dependencies — skip.
		}
	}
}

// TestNewWiredExecutor_NilOptionalsDegrade pins that the factory keeps
// NewExecutor's graceful-degradation contract: optional deps left nil
// stay nil (feature-disabled code paths), and a nil Emitter falls back
// to the no-op emitter exactly like NewExecutor does.
func TestNewWiredExecutor_NilOptionalsDegrade(t *testing.T) {
	db := openFactoryTestDB(t)
	defer db.Close()

	exec := NewWiredExecutor(ExecutorDeps{
		Store:    NewStore(db),
		Resolver: NewResolver(db),
		Runner:   newMockRunner(),
	})
	if exec.emitter == nil {
		t.Error("nil Emitter must fall back to the no-op emitter")
	}
	if exec.waitpoints != nil || exec.ws != nil || exec.runs != nil ||
		exec.idempotency != nil || exec.stepOverrides != nil ||
		exec.runStore != nil || exec.codeRunner != nil || exec.signals != nil ||
		exec.egressAllowed != nil || exec.credentialByType != nil {
		t.Error("optional deps left nil must stay nil (documented degraded behaviour)")
	}
}

const factoryWaitDSL = `{
  "dsl_version": "1.0",
  "name": "cron-wait",
  "steps": [
    {"id": "a", "type": "agent_run", "agent_slug": "s_a", "prompt": "do a"},
    {"id": "gate", "type": "wait", "wait": {"kind": "approval", "approval_prompt": "ok?"}, "timeout_seconds": 3600},
    {"id": "b", "type": "agent_run", "agent_slug": "s_b", "prompt": "after gate"}
  ]
}`

// TestSchedulerFire_WaitApprovalStep_ParksAsWaiting drives the
// scheduler's real fire path (fireOne) with a factory-built executor —
// the exact wiring cmd_start.go gives the cron scheduler — and pins the
// 3 AM contract: a cron-fired routine with a wait:approval step must
// PARK (run row status=waiting + a pending waitpoint registered for the
// gate step), not stall 60s into the nil-WaitpointStore failure.
func TestSchedulerFire_WaitApprovalStep_ParksAsWaiting(t *testing.T) {
	db := openFactoryTestDB(t)
	defer db.Close()
	ctx := context.Background()

	runner := newMockRunner()
	runner.outputsBySlug["s_a"] = []string{"out-a"}
	deps := fullExecutorDeps(t, db, runner)
	exec := NewWiredExecutor(deps)

	store := deps.Store
	runStore := deps.RunStore
	p := saveResumePipeline(t, store, "cron-wait", factoryWaitDSL)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	scheduler := NewPipelineScheduler(NewScheduleStore(db), store, exec, logger)

	done := make(chan struct{})
	go func() {
		defer close(done)
		scheduler.fireOne(ctx, &Schedule{
			ID:               "psched_wait",
			WorkspaceID:      "ws_test",
			TargetPipelineID: p.ID,
			CronExpr:         "0 8 * * *",
			Timezone:         "UTC",
		})
	}()
	select {
	case <-done:
		// fireOne returned promptly — the run parked instead of
		// blocking in the 60s no-store fallback.
	case <-time.After(10 * time.Second):
		t.Fatal("fireOne did not return within 10s — cron-fired wait:approval is stalling instead of parking (WaitpointStore not wired on the scheduler executor?)")
	}

	// The run row must be parked WAITING at the gate step.
	runs, err := runStore.ListActive(ctx, "ws_test")
	if err != nil {
		t.Fatalf("list active runs: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("active runs after cron fire: got %d, want 1 (parked)", len(runs))
	}
	rec := runs[0]
	if rec.Status != RunStatusWaiting {
		t.Errorf("cron-fired run status: got %q, want %q", rec.Status, RunStatusWaiting)
	}
	if rec.CurrentStepID != "gate" {
		t.Errorf("cron-fired run current step: got %q, want gate", rec.CurrentStepID)
	}

	// And a PENDING waitpoint must exist for (run, gate) — the token
	// the inbox approve endpoint resolves.
	var wpCount int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pipeline_waitpoints WHERE pipeline_run_id = ? AND step_id = 'gate' AND status = 'pending'`,
		rec.ID,
	).Scan(&wpCount); err != nil {
		t.Fatal(err)
	}
	if wpCount != 1 {
		t.Errorf("pending waitpoints for cron-fired run: got %d, want 1", wpCount)
	}
}

const factoryCodeDSL = `{
  "dsl_version": "1.0",
  "name": "resume-code",
  "steps": [
    {"id": "a", "type": "agent_run", "agent_slug": "s_a", "prompt": "do a"},
    {"id": "calc", "type": "code", "code": {"runtime": "expr", "code": "6 > 5"}},
    {"id": "c", "type": "agent_run", "agent_slug": "s_c", "prompt": "got {{ steps.calc.output }}"}
  ]
}`

// TestResume_CodeStep_ExecutesWithFactoryExecutor pins the boot-resume
// half of the wiring defect: a run killed at a type:code step must
// resume and EXECUTE the code step after a restart when the resume
// executor is factory-built — the old hand-wired resume executor had no
// CodeRunner and failed the resumed run with "no CodeRunner wired".
func TestResume_CodeStep_ExecutesWithFactoryExecutor(t *testing.T) {
	db := openFactoryTestDB(t)
	defer db.Close()
	ctx := context.Background()

	runner := newMockRunner()
	runner.outputsBySlug["s_c"] = []string{"final-c"}
	deps := fullExecutorDeps(t, db, runner)
	exec := NewWiredExecutor(deps)

	p := saveResumePipeline(t, deps.Store, "resume-code", factoryCodeDSL)
	insertInFlightRun(t, deps.RunStore, &RunRecord{
		ID:              "run_resume_code",
		WorkspaceID:     "ws_test",
		PipelineID:      p.ID,
		PipelineSlug:    p.Slug,
		Status:          RunStatusRunning,
		Mode:            ModeRun,
		CurrentStepID:   "calc",
		StepOutputsJSON: `{"a":"restored-a"}`,
	})

	resumed, interrupted, err := exec.ResumeInterruptedRuns(ctx, slog.Default())
	if err != nil {
		t.Fatalf("resume scan: %v", err)
	}
	if resumed != 1 || interrupted != 0 {
		t.Fatalf("resumed=%d interrupted=%d, want 1/0", resumed, interrupted)
	}

	rec := waitForRunStatus(t, deps.RunStore, "run_resume_code", RunStatusCompleted, 5*time.Second)
	outputs, err := deps.RunStore.GetStepOutputs(ctx, rec.ID)
	if err != nil {
		t.Fatalf("get step outputs: %v", err)
	}
	if outputs["a"] != "restored-a" || outputs["calc"] != "true" || outputs["c"] != "final-c" {
		t.Errorf("step outputs after code-step resume: %#v", outputs)
	}

	// Step a was restored, not re-run; step c saw the code output.
	runner.mu.Lock()
	defer runner.mu.Unlock()
	for _, call := range runner.calls {
		if call.AgentSlug == "s_a" {
			t.Errorf("step a was re-executed on resume")
		}
		if call.AgentSlug == "s_c" && call.Prompt != "got true" {
			t.Errorf("step c prompt did not render the resumed code output: %q", call.Prompt)
		}
	}
}

// TestResume_StepOverrideApplied pins the third dropped capability: a
// per-step prompt override (v123) must apply on the RESUME path exactly
// as it does on the HTTP path. The old hand-wired resume executor had
// no StepOverrideStore, so operator overrides were silently dropped on
// every post-restart resume.
func TestResume_StepOverrideApplied(t *testing.T) {
	db := openFactoryTestDB(t)
	defer db.Close()
	ctx := context.Background()

	runner := newMockRunner()
	runner.outputsBySlug["s_b"] = []string{"out-b"}
	runner.outputsBySlug["s_c"] = []string{"out-c"}
	deps := fullExecutorDeps(t, db, runner)
	exec := NewWiredExecutor(deps)

	p := saveResumePipeline(t, deps.Store, "resume-override", resumeLinearDSL)
	if err := NewStepOverrideStore(db).Set(ctx, "ws_test", p.ID, "b", "patched-b-prompt", ""); err != nil {
		t.Fatalf("set override: %v", err)
	}
	insertInFlightRun(t, deps.RunStore, &RunRecord{
		ID:              "run_resume_override",
		WorkspaceID:     "ws_test",
		PipelineID:      p.ID,
		PipelineSlug:    p.Slug,
		Status:          RunStatusRunning,
		Mode:            ModeRun,
		CurrentStepID:   "b",
		StepOutputsJSON: `{"a":"restored-a"}`,
	})

	resumed, interrupted, err := exec.ResumeInterruptedRuns(ctx, slog.Default())
	if err != nil {
		t.Fatalf("resume scan: %v", err)
	}
	if resumed != 1 || interrupted != 0 {
		t.Fatalf("resumed=%d interrupted=%d, want 1/0", resumed, interrupted)
	}
	waitForRunStatus(t, deps.RunStore, "run_resume_override", RunStatusCompleted, 5*time.Second)

	runner.mu.Lock()
	defer runner.mu.Unlock()
	foundB := false
	for _, call := range runner.calls {
		if call.AgentSlug == "s_b" {
			foundB = true
			if call.Prompt != "patched-b-prompt" {
				t.Errorf("resumed step b prompt: got %q, want the operator override %q", call.Prompt, "patched-b-prompt")
			}
		}
	}
	if !foundB {
		t.Error("step b was never executed on resume")
	}
}
