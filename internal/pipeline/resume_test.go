package pipeline

// Boot-time resume-from-step tests (Release 1.0 hardening W6).
//
// The kill-between-steps scenario is simulated by fabricating the
// exact DB state a hard kill leaves behind: a pipeline_runs row in
// status=running with current_step_id + step_outputs_json mid-flight
// and no terminal write. TestExecutor_PersistsStepStateMidRun pins
// that this fabricated shape matches what the executor actually
// writes at step boundaries, so the two tests together cover the
// real crash → restart → resume path without killing a process.

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// openResumeTestDB layers the pipeline_runs + pipeline_waitpoints
// schemas on top of the standard store schema so a single in-memory
// DB serves the pipelines table (Save/GetByID), the run projection,
// and waitpoint persistence at once.
func openResumeTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db := openStoreTestDB(t)
	if _, err := db.ExecContext(context.Background(), `
CREATE TABLE IF NOT EXISTS pipeline_runs (
    id                  TEXT PRIMARY KEY,
    workspace_id        TEXT NOT NULL,
    pipeline_id         TEXT NOT NULL,
    pipeline_slug       TEXT NOT NULL,
    pipeline_version    INTEGER,
    definition_hash     TEXT,
    status              TEXT NOT NULL,
    mode                TEXT NOT NULL DEFAULT 'run',
    started_at          TEXT NOT NULL,
    ended_at            TEXT,
    current_step_id     TEXT,
    step_outputs_json   TEXT NOT NULL DEFAULT '{}',
    output              TEXT,
    cost_usd            REAL NOT NULL DEFAULT 0,
    duration_ms         INTEGER NOT NULL DEFAULT 0,
    error_message       TEXT,
    failed_at_step      TEXT,
    error_fingerprint   TEXT,
    invoking_crew_id    TEXT,
    invoking_agent_id   TEXT,
    invoking_user_id    TEXT,
    triggered_via       TEXT NOT NULL DEFAULT 'manual',
    triggered_by_id     TEXT,
    idempotency_key     TEXT,
    inputs_json         TEXT NOT NULL DEFAULT '{}',
    concurrency_key     TEXT,
    metadata_json       TEXT NOT NULL DEFAULT '{}',
    is_replay           INTEGER NOT NULL DEFAULT 0,
    replay_of           TEXT,
    created_at          TEXT NOT NULL DEFAULT (datetime('now','subsec')),
    updated_at          TEXT NOT NULL DEFAULT (datetime('now','subsec'))
);
CREATE TABLE IF NOT EXISTS pipeline_waitpoints (
    token              TEXT PRIMARY KEY,
    workspace_id       TEXT NOT NULL,
    pipeline_run_id    TEXT NOT NULL,
    step_id            TEXT NOT NULL,
    kind               TEXT NOT NULL,
    prompt             TEXT,
    invoking_crew_id   TEXT,
    status             TEXT NOT NULL DEFAULT 'pending',
    decision_payload   TEXT,
    decided_by_user_id TEXT,
    timeout_at         TEXT NOT NULL,
    created_at         TEXT NOT NULL DEFAULT (datetime('now','subsec')),
    decided_at         TEXT
);`); err != nil {
		t.Fatalf("resume schema: %v", err)
	}
	return db
}

// saveResumePipeline saves a pipeline whose DefinitionJSON is the
// supplied DSL JSON, passing the test-run gate.
func saveResumePipeline(t *testing.T, store *Store, slug, definitionJSON string) *Pipeline {
	t.Helper()
	in := validSaveInput(slug)
	in.DefinitionJSON = definitionJSON
	p, err := store.Save(context.Background(), in)
	if err != nil {
		t.Fatalf("save pipeline %q: %v", slug, err)
	}
	return p
}

// insertInFlightRun fabricates the row a hard kill leaves behind.
func insertInFlightRun(t *testing.T, runStore *RunStore, rec *RunRecord) {
	t.Helper()
	if rec.Status == "" {
		rec.Status = RunStatusRunning
	}
	if rec.Mode == "" {
		rec.Mode = ModeRun
	}
	if err := runStore.Insert(context.Background(), rec); err != nil {
		t.Fatalf("insert in-flight run: %v", err)
	}
}

// waitForRunStatus polls the run row until it reaches `want` or the
// deadline passes.
func waitForRunStatus(t *testing.T, runStore *RunStore, runID string, want RunStatus, timeout time.Duration) *RunRecord {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		rec, err := runStore.Get(context.Background(), runID)
		if err == nil && rec.Status == want {
			return rec
		}
		if time.Now().After(deadline) {
			status := RunStatus("<missing>")
			if err == nil {
				status = rec.Status
			}
			t.Fatalf("run %s never reached status %q (last=%q, err=%v)", runID, want, status, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

const resumeLinearDSL = `{
  "dsl_version": "1.0",
  "name": "resume-linear",
  "steps": [
    {"id": "a", "type": "agent_run", "agent_slug": "s_a", "prompt": "do a"},
    {"id": "b", "type": "agent_run", "agent_slug": "s_b", "prompt": "use {{ steps.a.output }}"},
    {"id": "c", "type": "agent_run", "agent_slug": "s_c", "prompt": "finish"}
  ]
}`

// TestResume_KillBetweenSteps_ResumesAndCompletes is the core W6
// contract: a run killed between step a and step b resumes from b
// with a's restored output and completes without re-running a.
func TestResume_KillBetweenSteps_ResumesAndCompletes(t *testing.T) {
	db := openResumeTestDB(t)
	defer db.Close()
	ctx := context.Background()

	store := NewStore(db)
	runStore := NewRunStore(db)
	p := saveResumePipeline(t, store, "resume-linear", resumeLinearDSL)

	insertInFlightRun(t, runStore, &RunRecord{
		ID:              "run_resume_linear",
		WorkspaceID:     "ws_test",
		PipelineID:      p.ID,
		PipelineSlug:    p.Slug,
		Status:          RunStatusRunning,
		Mode:            ModeRun,
		CurrentStepID:   "b",
		StepOutputsJSON: `{"a":"restored-output-a"}`,
	})

	runner := newMockRunner()
	runner.outputsBySlug["s_b"] = []string{"output-b"}
	runner.outputsBySlug["s_c"] = []string{"output-c"}
	em := &captureEmitter{}
	exec := NewExecutor(store, NewResolver(db), runner, em).WithRunStore(runStore)

	resumed, interrupted, err := exec.ResumeInterruptedRuns(ctx, slog.Default())
	if err != nil {
		t.Fatalf("resume scan: %v", err)
	}
	if resumed != 1 || interrupted != 0 {
		t.Fatalf("resumed=%d interrupted=%d, want 1/0", resumed, interrupted)
	}

	rec := waitForRunStatus(t, runStore, "run_resume_linear", RunStatusCompleted, 5*time.Second)
	var outputs map[string]string
	if err := json.Unmarshal([]byte(rec.StepOutputsJSON), &outputs); err != nil {
		t.Fatalf("unmarshal outputs: %v", err)
	}
	if outputs["a"] != "restored-output-a" || outputs["b"] != "output-b" || outputs["c"] != "output-c" {
		t.Errorf("step outputs after resume: %#v", outputs)
	}
	if rec.Output != "output-c" {
		t.Errorf("final output: got %q, want output-c", rec.Output)
	}

	// Step a must NOT have been re-executed; b's prompt must have
	// rendered against the restored output.
	runner.mu.Lock()
	defer runner.mu.Unlock()
	for _, call := range runner.calls {
		if call.AgentSlug == "s_a" {
			t.Errorf("step a was re-executed on resume")
		}
	}
	foundB := false
	for _, call := range runner.calls {
		if call.AgentSlug == "s_b" {
			foundB = true
			if call.Prompt != "use restored-output-a" {
				t.Errorf("step b prompt did not render restored output: %q", call.Prompt)
			}
		}
	}
	if !foundB {
		t.Errorf("step b was never executed")
	}
}

const resumeWaitDSL = `{
  "dsl_version": "1.0",
  "name": "resume-wait",
  "steps": [
    {"id": "a", "type": "agent_run", "agent_slug": "s_a", "prompt": "do a"},
    {"id": "gate", "type": "wait", "wait": {"kind": "approval", "approval_prompt": "ok?"}, "timeout_seconds": 3600},
    {"id": "b", "type": "agent_run", "agent_slug": "s_b", "prompt": "after gate"}
  ]
}`

// TestResume_WaitStep_SurvivesRestartAndAcceptsApproval pins the
// waitpoint re-registration half of W6: a run parked on a `wait`
// approval step survives a simulated restart (fresh waitpoint store
// instance = empty in-memory listeners), re-attaches to the EXISTING
// pending waitpoint row (no duplicate approval row), and completes
// once the original token is approved.
func TestResume_WaitStep_SurvivesRestartAndAcceptsApproval(t *testing.T) {
	db := openResumeTestDB(t)
	defer db.Close()
	ctx := context.Background()

	store := NewStore(db)
	runStore := NewRunStore(db)
	p := saveResumePipeline(t, store, "resume-wait", resumeWaitDSL)

	insertInFlightRun(t, runStore, &RunRecord{
		ID:              "run_resume_wait",
		WorkspaceID:     "ws_test",
		PipelineID:      p.ID,
		PipelineSlug:    p.Slug,
		Status:          RunStatusRunning,
		Mode:            ModeRun,
		CurrentStepID:   "gate",
		StepOutputsJSON: `{"a":"out-a"}`,
	})
	// The pending approval row the previous lifetime left behind.
	timeoutAt := time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)
	if _, err := db.ExecContext(ctx, `
INSERT INTO pipeline_waitpoints (token, workspace_id, pipeline_run_id, step_id, kind, prompt, status, timeout_at)
VALUES ('tok_resume_wait', 'ws_test', 'run_resume_wait', 'gate', 'approval', 'ok?', 'pending', ?)`, timeoutAt); err != nil {
		t.Fatalf("seed waitpoint: %v", err)
	}

	// Fresh store instance — simulates the restart (listeners map empty).
	wpStore := NewSQLWaitpointStore(db)
	defer wpStore.Close()

	runner := newMockRunner()
	runner.outputsBySlug["s_b"] = []string{"output-b"}
	em := &captureEmitter{}
	exec := NewExecutor(store, NewResolver(db), runner, em).
		WithRunStore(runStore).
		WithWaitpointStore(wpStore)

	resumed, interrupted, err := exec.ResumeInterruptedRuns(ctx, slog.Default())
	if err != nil {
		t.Fatalf("resume scan: %v", err)
	}
	if resumed != 1 || interrupted != 0 {
		t.Fatalf("resumed=%d interrupted=%d, want 1/0", resumed, interrupted)
	}

	// The approval still works after the restart: approving the
	// ORIGINAL token unblocks the resumed run.
	approveErr := make(chan error, 1)
	go func() {
		// Give the resumed goroutine a moment to park on WaitFor —
		// both orderings must work (WaitFor re-checks DB state), so
		// this is pacing, not a correctness wait.
		time.Sleep(100 * time.Millisecond)
		approveErr <- wpStore.CompleteApproval(context.Background(), "tok_resume_wait", true, "user_test", "")
	}()

	rec := waitForRunStatus(t, runStore, "run_resume_wait", RunStatusCompleted, 5*time.Second)
	if err := <-approveErr; err != nil {
		t.Fatalf("approve original token: %v", err)
	}

	var outputs map[string]string
	_ = json.Unmarshal([]byte(rec.StepOutputsJSON), &outputs)
	if outputs["a"] != "out-a" || outputs["b"] != "output-b" {
		t.Errorf("step outputs after wait resume: %#v", outputs)
	}

	// Exactly ONE waitpoint row for (run, step) — resume re-attached
	// instead of minting a duplicate approval.
	var wpCount int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pipeline_waitpoints WHERE pipeline_run_id = 'run_resume_wait' AND step_id = 'gate'`,
	).Scan(&wpCount); err != nil {
		t.Fatal(err)
	}
	if wpCount != 1 {
		t.Errorf("waitpoint rows for (run, gate): got %d, want 1 (no duplicate approval)", wpCount)
	}

	// Step a was restored, not re-run.
	runner.mu.Lock()
	defer runner.mu.Unlock()
	for _, call := range runner.calls {
		if call.AgentSlug == "s_a" {
			t.Errorf("step a was re-executed on wait-step resume")
		}
	}
}

const resumeDAGDSL = `{
  "dsl_version": "1.0",
  "name": "resume-dag",
  "steps": [
    {"id": "a", "type": "agent_run", "agent_slug": "s_a", "prompt": "do a"},
    {"id": "b", "type": "agent_run", "agent_slug": "s_b", "prompt": "use {{ steps.a.output }}", "needs": ["a"]},
    {"id": "c", "type": "agent_run", "agent_slug": "s_c", "prompt": "finish", "needs": ["b"]}
  ]
}`

// TestResume_DAGRun_SkipsRestoredSteps covers the DAG scheduler path:
// restored outputs seed the completed set so only unfinished steps run.
func TestResume_DAGRun_SkipsRestoredSteps(t *testing.T) {
	db := openResumeTestDB(t)
	defer db.Close()
	ctx := context.Background()

	store := NewStore(db)
	runStore := NewRunStore(db)
	p := saveResumePipeline(t, store, "resume-dag", resumeDAGDSL)

	insertInFlightRun(t, runStore, &RunRecord{
		ID:              "run_resume_dag",
		WorkspaceID:     "ws_test",
		PipelineID:      p.ID,
		PipelineSlug:    p.Slug,
		Status:          RunStatusRunning,
		Mode:            ModeRun,
		StepOutputsJSON: `{"a":"dag-restored-a"}`,
	})

	runner := newMockRunner()
	runner.outputsBySlug["s_b"] = []string{"dag-b"}
	runner.outputsBySlug["s_c"] = []string{"dag-c"}
	em := &captureEmitter{}
	exec := NewExecutor(store, NewResolver(db), runner, em).WithRunStore(runStore)

	resumed, interrupted, err := exec.ResumeInterruptedRuns(ctx, slog.Default())
	if err != nil {
		t.Fatalf("resume scan: %v", err)
	}
	if resumed != 1 || interrupted != 0 {
		t.Fatalf("resumed=%d interrupted=%d, want 1/0", resumed, interrupted)
	}

	rec := waitForRunStatus(t, runStore, "run_resume_dag", RunStatusCompleted, 5*time.Second)
	var outputs map[string]string
	_ = json.Unmarshal([]byte(rec.StepOutputsJSON), &outputs)
	if outputs["a"] != "dag-restored-a" || outputs["b"] != "dag-b" || outputs["c"] != "dag-c" {
		t.Errorf("DAG step outputs after resume: %#v", outputs)
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	for _, call := range runner.calls {
		if call.AgentSlug == "s_a" {
			t.Errorf("DAG step a was re-executed on resume")
		}
	}
}

// TestResume_FallbackToInterrupted pins the honesty contract: when
// state is insufficient (missing pipeline, schema drift), the run is
// stamped interrupted — never silently dropped, never wrongly resumed.
func TestResume_FallbackToInterrupted(t *testing.T) {
	db := openResumeTestDB(t)
	defer db.Close()
	ctx := context.Background()

	store := NewStore(db)
	runStore := NewRunStore(db)
	p := saveResumePipeline(t, store, "resume-fallback", resumeLinearDSL)

	// Case 1: pipeline row is gone.
	insertInFlightRun(t, runStore, &RunRecord{
		ID: "run_missing_pipeline", WorkspaceID: "ws_test",
		PipelineID: "pln_ghost", PipelineSlug: "ghost",
		Status: RunStatusRunning, Mode: ModeRun,
	})
	// Case 2: schema drift — persisted outputs reference a step that
	// no longer exists in the definition.
	insertInFlightRun(t, runStore, &RunRecord{
		ID: "run_schema_drift", WorkspaceID: "ws_test",
		PipelineID: p.ID, PipelineSlug: p.Slug,
		Status: RunStatusRunning, Mode: ModeRun,
		StepOutputsJSON: `{"renamed_step":"stale"}`,
	})
	// Case 3: a non-live (dry_run) mode row from a previous lifetime —
	// only ModeRun resumes; a preview-mode row must be interrupted, never
	// re-run (resuming would only burn tokens).
	insertInFlightRun(t, runStore, &RunRecord{
		ID: "run_testrun_mode", WorkspaceID: "ws_test",
		PipelineID: p.ID, PipelineSlug: p.Slug,
		Status: RunStatusRunning, Mode: ModeDryRun,
	})

	runner := newMockRunner()
	em := &captureEmitter{}
	exec := NewExecutor(store, NewResolver(db), runner, em).WithRunStore(runStore)

	resumed, interrupted, err := exec.ResumeInterruptedRuns(ctx, slog.Default())
	if err != nil {
		t.Fatalf("resume scan: %v", err)
	}
	if resumed != 0 || interrupted != 3 {
		t.Fatalf("resumed=%d interrupted=%d, want 0/3", resumed, interrupted)
	}
	for _, id := range []string{"run_missing_pipeline", "run_schema_drift", "run_testrun_mode"} {
		rec, err := runStore.Get(ctx, id)
		if err != nil {
			t.Fatalf("get %s: %v", id, err)
		}
		if rec.Status != RunStatusInterrupted {
			t.Errorf("%s: status=%q, want interrupted", id, rec.Status)
		}
		if rec.EndedAt == nil {
			t.Errorf("%s: ended_at not stamped", id)
		}
		if rec.ErrorMessage == "" {
			t.Errorf("%s: error_message empty — fallback reason must be recorded", id)
		}
	}
	if len(runner.calls) != 0 {
		t.Errorf("fallback runs must not invoke the runner; got %d calls", len(runner.calls))
	}
}

// blockingRunner lets a test freeze a run mid-step so the DB state at
// the freeze point can be asserted — pinning that the executor's
// step-boundary persistence writes exactly the shape the resume tests
// fabricate.
type blockingRunner struct {
	inner    *mockRunner
	blockOn  string        // agent_slug to park on
	entered  chan struct{} // closed when the blocked step is reached
	released chan struct{} // close to let the blocked step finish
}

func (b *blockingRunner) RunStep(ctx context.Context, req AgentStepRequest) (AgentStepResult, error) {
	if req.AgentSlug == b.blockOn {
		close(b.entered)
		select {
		case <-b.released:
		case <-ctx.Done():
			return AgentStepResult{}, ctx.Err()
		}
	}
	return b.inner.RunStep(ctx, req)
}

// TestExecutor_PersistsStepStateMidRun pins the kill-state shape: at
// the moment step b is in flight, the run row must already carry
// status=running, current_step_id=b, and step a's output — exactly
// what RecoverInterruptedAtBoot-era rows lacked and what resume reads.
func TestExecutor_PersistsStepStateMidRun(t *testing.T) {
	db := openResumeTestDB(t)
	defer db.Close()
	ctx := context.Background()

	store := NewStore(db)
	runStore := NewRunStore(db)
	p := saveResumePipeline(t, store, "persist-steps", resumeLinearDSL)

	inner := newMockRunner()
	inner.outputsBySlug["s_a"] = []string{"live-a"}
	inner.outputsBySlug["s_b"] = []string{"live-b"}
	inner.outputsBySlug["s_c"] = []string{"live-c"}
	runner := &blockingRunner{
		inner:    inner,
		blockOn:  "s_b",
		entered:  make(chan struct{}),
		released: make(chan struct{}),
	}
	em := &captureEmitter{}
	exec := NewExecutor(store, NewResolver(db), runner, em).WithRunStore(runStore)

	done := make(chan *RunResult, 1)
	go func() {
		res, err := exec.Run(ctx, RunInput{
			PipelineID:  p.ID,
			WorkspaceID: "ws_test",
			Mode:        ModeRun,
		})
		if err != nil {
			t.Errorf("run: %v", err)
		}
		done <- res
	}()

	select {
	case <-runner.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("step b never started")
	}

	// Mid-run, parked inside step b: the row must already show the
	// crash-recoverable shape.
	var rec *RunRecord
	deadline := time.Now().Add(2 * time.Second)
	for {
		runs, err := runStore.ListActive(ctx, "ws_test")
		if err == nil && len(runs) == 1 {
			rec = runs[0]
			if rec.CurrentStepID == "b" {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("mid-run row never reached current_step_id=b (got %+v)", rec)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if rec.Status != RunStatusRunning {
		t.Errorf("mid-run status: %q, want running", rec.Status)
	}
	var midOutputs map[string]string
	if err := json.Unmarshal([]byte(rec.StepOutputsJSON), &midOutputs); err != nil {
		t.Fatalf("unmarshal mid-run outputs: %v", err)
	}
	if midOutputs["a"] != "live-a" {
		t.Errorf("step a output not persisted before step b ran: %#v", midOutputs)
	}

	close(runner.released)
	res := <-done
	if res == nil || res.Status != "COMPLETED" {
		t.Fatalf("run did not complete: %+v", res)
	}
	final := waitForRunStatus(t, runStore, res.RunID, RunStatusCompleted, 2*time.Second)
	var outputs map[string]string
	_ = json.Unmarshal([]byte(final.StepOutputsJSON), &outputs)
	if outputs["a"] != "live-a" || outputs["b"] != "live-b" || outputs["c"] != "live-c" {
		t.Errorf("final outputs: %#v", outputs)
	}
}

// TestRunStore_ListInFlightAndMarkInterrupted covers the two new
// store primitives the resume scan is built on.
func TestRunStore_ListInFlightAndMarkInterrupted(t *testing.T) {
	store, db := openRunsTestDB(t)
	defer db.Close()
	ctx := context.Background()

	seed := []*RunRecord{
		{ID: "run_q", WorkspaceID: "ws_runs", PipelineID: "pln_a", PipelineSlug: "s", Status: RunStatusQueued},
		{ID: "run_r", WorkspaceID: "ws_runs", PipelineID: "pln_a", PipelineSlug: "s", Status: RunStatusRunning},
		{ID: "run_done", WorkspaceID: "ws_runs", PipelineID: "pln_b", PipelineSlug: "s", Status: RunStatusCompleted},
	}
	for _, r := range seed {
		if err := store.Insert(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	// Completed rows need ended_at for realism but Insert leaves it
	// NULL — irrelevant to this test's filter assertions.

	inflight, err := store.ListInFlight(ctx)
	if err != nil {
		t.Fatalf("list in-flight: %v", err)
	}
	if len(inflight) != 2 {
		t.Fatalf("in-flight count: got %d, want 2", len(inflight))
	}

	if err := store.MarkInterrupted(ctx, "run_r", "test reason"); err != nil {
		t.Fatalf("mark interrupted: %v", err)
	}
	rec, _ := store.Get(ctx, "run_r")
	if rec.Status != RunStatusInterrupted || rec.ErrorMessage != "test reason" || rec.EndedAt == nil {
		t.Errorf("interrupted row: %+v", rec)
	}
	// Terminal rows must be untouchable.
	if err := store.MarkInterrupted(ctx, "run_done", "should not apply"); err != nil {
		t.Fatalf("mark interrupted terminal: %v", err)
	}
	rec, _ = store.Get(ctx, "run_done")
	if rec.Status != RunStatusCompleted {
		t.Errorf("terminal row mutated: %+v", rec)
	}
}

const resumeCostCapLinearDSL = `{
  "dsl_version": "1.0",
  "name": "resume-costcap-linear",
  "max_cost_usd": 0.005,
  "steps": [
    {"id": "a", "type": "agent_run", "agent_slug": "s_a", "prompt": "do a"},
    {"id": "b", "type": "agent_run", "agent_slug": "s_b", "prompt": "use {{ steps.a.output }}"},
    {"id": "c", "type": "agent_run", "agent_slug": "s_c", "prompt": "finish"}
  ]
}`

const resumeCostCapDAGDSL = `{
  "dsl_version": "1.0",
  "name": "resume-costcap-dag",
  "max_cost_usd": 0.005,
  "steps": [
    {"id": "a", "type": "agent_run", "agent_slug": "s_a", "prompt": "do a"},
    {"id": "b", "type": "agent_run", "agent_slug": "s_b", "prompt": "use {{ steps.a.output }}", "needs": ["a"]},
    {"id": "c", "type": "agent_run", "agent_slug": "s_c", "prompt": "finish", "needs": ["b"]}
  ]
}`

// TestResume_RestoredCostBreachesCap_FailsBeforeAnyStep pins the
// resume-time cost-cap gate: when the previous lifetime's
// step-boundary flush persisted a CostUSD already at or over
// max_cost_usd (the process died after the flush but before the
// post-step cap gate ran), the resumed run must terminate through
// the same cap-failure path a live breach uses — WITHOUT scheduling
// a single further step. Covers both the linear loop and the DAG
// scheduler entrypoints, plus the >= boundary (budget fully consumed
// = nothing left to spend on another step).
func TestResume_RestoredCostBreachesCap_FailsBeforeAnyStep(t *testing.T) {
	cases := []struct {
		name         string
		slug         string
		dsl          string
		restoredCost float64
	}{
		{"linear over cap", "resume-costcap-linear", resumeCostCapLinearDSL, 0.009},
		{"linear exactly at cap", "resume-costcap-linear", resumeCostCapLinearDSL, 0.005},
		{"dag over cap", "resume-costcap-dag", resumeCostCapDAGDSL, 0.009},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := openResumeTestDB(t)
			defer db.Close()
			ctx := context.Background()

			store := NewStore(db)
			runStore := NewRunStore(db)
			p := saveResumePipeline(t, store, tc.slug, tc.dsl)

			insertInFlightRun(t, runStore, &RunRecord{
				ID:              "run_costcap_" + tc.slug,
				WorkspaceID:     "ws_test",
				PipelineID:      p.ID,
				PipelineSlug:    p.Slug,
				Status:          RunStatusRunning,
				Mode:            ModeRun,
				CurrentStepID:   "b",
				StepOutputsJSON: `{"a":"restored-output-a"}`,
				CostUSD:         tc.restoredCost,
			})

			runner := newMockRunner()
			em := &captureEmitter{}
			exec := NewExecutor(store, NewResolver(db), runner, em).WithRunStore(runStore)

			resumed, interrupted, err := exec.ResumeInterruptedRuns(ctx, slog.Default())
			if err != nil {
				t.Fatalf("resume scan: %v", err)
			}
			if resumed != 1 || interrupted != 0 {
				t.Fatalf("resumed=%d interrupted=%d, want 1/0", resumed, interrupted)
			}

			rec := waitForRunStatus(t, runStore, "run_costcap_"+tc.slug, RunStatusFailed, 5*time.Second)
			if !strings.Contains(rec.ErrorMessage, "cost cap exceeded") {
				t.Errorf("error message: got %q, want cost cap wording", rec.ErrorMessage)
			}

			// The whole point: NO further step may execute once the
			// restored cost already breaches the budget.
			runner.mu.Lock()
			defer runner.mu.Unlock()
			if len(runner.calls) != 0 {
				t.Errorf("expected 0 runner calls after resume of over-budget run, got %d: %+v", len(runner.calls), runner.calls)
			}
		})
	}
}

// TestResume_RestoredCostUnderCap_LiveCapStillTrips is the regression
// guard for the gate above: a restored cost just UNDER the cap must
// still resume normally, and the existing live post-step cap check
// must still trip once the next step pushes the total over budget.
func TestResume_RestoredCostUnderCap_LiveCapStillTrips(t *testing.T) {
	db := openResumeTestDB(t)
	defer db.Close()
	ctx := context.Background()

	store := NewStore(db)
	runStore := NewRunStore(db)
	// Cap 0.005, restored 0.0045: under the cap, so the run resumes.
	// Mock runner charges 0.001/step → step b lands at 0.0055 > cap.
	p := saveResumePipeline(t, store, "resume-costcap-linear", resumeCostCapLinearDSL)

	insertInFlightRun(t, runStore, &RunRecord{
		ID:              "run_costcap_under",
		WorkspaceID:     "ws_test",
		PipelineID:      p.ID,
		PipelineSlug:    p.Slug,
		Status:          RunStatusRunning,
		Mode:            ModeRun,
		CurrentStepID:   "b",
		StepOutputsJSON: `{"a":"restored-output-a"}`,
		CostUSD:         0.0045,
	})

	runner := newMockRunner()
	runner.outputsBySlug["s_b"] = []string{"output-b"}
	em := &captureEmitter{}
	exec := NewExecutor(store, NewResolver(db), runner, em).WithRunStore(runStore)

	resumed, interrupted, err := exec.ResumeInterruptedRuns(ctx, slog.Default())
	if err != nil {
		t.Fatalf("resume scan: %v", err)
	}
	if resumed != 1 || interrupted != 0 {
		t.Fatalf("resumed=%d interrupted=%d, want 1/0", resumed, interrupted)
	}

	rec := waitForRunStatus(t, runStore, "run_costcap_under", RunStatusFailed, 5*time.Second)
	if !strings.Contains(rec.ErrorMessage, "cost cap exceeded") {
		t.Errorf("error message: got %q, want cost cap wording", rec.ErrorMessage)
	}
	if !strings.Contains(rec.ErrorMessage, `after step "b"`) {
		t.Errorf("error message should attribute the live breach to step b: %q", rec.ErrorMessage)
	}

	// Exactly one step (b) ran: a was restored, c never started.
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if len(runner.calls) != 1 || runner.calls[0].AgentSlug != "s_b" {
		t.Errorf("expected exactly one runner call (s_b), got %+v", runner.calls)
	}
}
