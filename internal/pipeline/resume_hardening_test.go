package pipeline

// Hardening tests for the boot-time resume scan (PR #646 follow-up).
//
// F1 — lifetime fence: the resume scan must never pick up a run that
//      belongs to THIS process lifetime (scheduler fired before the
//      scan, boot-ordering regression) — neither via the started_at
//      cutoff nor when the run id is live in the RunRegistry. And
//      RunRegistry.Acquire must refuse a duplicate run id instead of
//      silently overwriting the live entry's cancel func.
// F2 — transient resume failures: a resumed run that loses the
//      concurrency-slot race is retried with backoff, not permanently
//      stamped interrupted.
// F3 — definition drift: the run row carries the definition hash from
//      run start; an in-place edit that keeps step ids but changes
//      content must fall back to interrupted, not resume old outputs
//      against new steps.
// F6 — a waitpoint that timed out during downtime surfaces as "timed
//      out" in the run error, not as "denied".

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// F1 (c) — RunRegistry.Acquire duplicate run id
// ---------------------------------------------------------------------------

func TestRunRegistry_AcquireDuplicateRunID_Errors(t *testing.T) {
	reg := NewRunRegistry()
	ctx := context.Background()

	_, release1, err := reg.Acquire(ctx, AcquireOpts{RunID: "run_dup", WorkspaceID: "ws"})
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	defer release1()

	_, release2, err := reg.Acquire(ctx, AcquireOpts{RunID: "run_dup", WorkspaceID: "ws"})
	if !errors.Is(err, ErrDuplicateRunID) {
		t.Fatalf("second acquire of same run id: err=%v, want ErrDuplicateRunID", err)
	}
	release2() // must be a safe no-op

	// The duplicate Acquire must NOT have clobbered the live entry.
	if !reg.IsLive("run_dup") {
		t.Fatal("live entry lost after duplicate Acquire")
	}
	if err := reg.Cancel("run_dup"); err != nil {
		t.Fatalf("cancel original entry after duplicate Acquire: %v", err)
	}
}

// ---------------------------------------------------------------------------
// F1 (b) — lifetime fence in the resume scan
// ---------------------------------------------------------------------------

// TestResume_SkipsRegistryLiveRun pins that an in-flight row whose run
// id is currently live in the RunRegistry (i.e. started by THIS
// process — e.g. the scheduler fired before the boot scan) is neither
// resumed (no concurrent double-execution under the same id) nor
// stamped interrupted (it is healthy and running).
func TestResume_SkipsRegistryLiveRun(t *testing.T) {
	db := openResumeTestDB(t)
	defer db.Close()
	ctx := context.Background()

	store := NewStore(db)
	runStore := NewRunStore(db)
	p := saveResumePipeline(t, store, "resume-live", resumeLinearDSL)

	reg := NewRunRegistry()
	_, release, err := reg.Acquire(ctx, AcquireOpts{
		RunID: "run_live", WorkspaceID: "ws_test", PipelineID: p.ID, PipelineSlug: p.Slug,
	})
	if err != nil {
		t.Fatalf("acquire live run: %v", err)
	}
	defer release()

	insertInFlightRun(t, runStore, &RunRecord{
		ID: "run_live", WorkspaceID: "ws_test",
		PipelineID: p.ID, PipelineSlug: p.Slug,
		Status: RunStatusRunning, Mode: ModeRun,
		CurrentStepID: "b", StepOutputsJSON: `{"a":"x"}`,
	})

	runner := newMockRunner()
	em := &captureEmitter{}
	exec := NewExecutor(store, NewResolver(db), runner, em).
		WithRunStore(runStore).WithRunRegistry(reg)

	resumed, interrupted, err := exec.ResumeInterruptedRuns(ctx, slog.Default())
	if err != nil {
		t.Fatalf("resume scan: %v", err)
	}
	if resumed != 0 || interrupted != 0 {
		t.Fatalf("resumed=%d interrupted=%d, want 0/0 (registry-live run must be left alone)", resumed, interrupted)
	}
	rec, err := runStore.Get(ctx, "run_live")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Status != RunStatusRunning {
		t.Errorf("registry-live run status mutated to %q, want running", rec.Status)
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if len(runner.calls) != 0 {
		t.Errorf("registry-live run was re-entered: %d runner calls", len(runner.calls))
	}
}

// TestResume_SkipsRunsYoungerThanBoot pins the started_at fence: a row
// started at-or-after the process boot cutoff can only belong to this
// lifetime and must be skipped — never resumed a second time, never
// stamped interrupted.
func TestResume_SkipsRunsYoungerThanBoot(t *testing.T) {
	db := openResumeTestDB(t)
	defer db.Close()
	ctx := context.Background()

	store := NewStore(db)
	runStore := NewRunStore(db)
	p := saveResumePipeline(t, store, "resume-young", resumeLinearDSL)

	insertInFlightRun(t, runStore, &RunRecord{
		ID: "run_young", WorkspaceID: "ws_test",
		PipelineID: p.ID, PipelineSlug: p.Slug,
		Status: RunStatusRunning, Mode: ModeRun,
		CurrentStepID: "b", StepOutputsJSON: `{"a":"x"}`,
	})

	runner := newMockRunner()
	em := &captureEmitter{}
	// Boot cutoff in the past — the row above (started "now") is
	// younger than boot, i.e. owned by the current lifetime.
	exec := NewExecutor(store, NewResolver(db), runner, em).
		WithRunStore(runStore).
		WithResumeCutoff(time.Now().Add(-time.Minute))

	resumed, interrupted, err := exec.ResumeInterruptedRuns(ctx, slog.Default())
	if err != nil {
		t.Fatalf("resume scan: %v", err)
	}
	if resumed != 0 || interrupted != 0 {
		t.Fatalf("resumed=%d interrupted=%d, want 0/0 (run younger than boot must be skipped)", resumed, interrupted)
	}
	rec, err := runStore.Get(ctx, "run_young")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Status != RunStatusRunning {
		t.Errorf("young run status mutated to %q, want running", rec.Status)
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if len(runner.calls) != 0 {
		t.Errorf("young run was re-entered: %d runner calls", len(runner.calls))
	}
}

// ---------------------------------------------------------------------------
// F2 — concurrency-slot loss is retried, not permanently interrupted
// ---------------------------------------------------------------------------

const resumeConcurrencyDSL = `{
  "dsl_version": "1.0",
  "name": "resume-cc",
  "concurrency_key": "cc-gate",
  "steps": [
    {"id": "a", "type": "agent_run", "agent_slug": "s_a", "prompt": "do a"},
    {"id": "b", "type": "agent_run", "agent_slug": "s_b", "prompt": "use {{ steps.a.output }}"}
  ]
}`

func TestResume_ConcurrencyLimit_RetriedNotInterrupted(t *testing.T) {
	db := openResumeTestDB(t)
	defer db.Close()
	ctx := context.Background()

	store := NewStore(db)
	runStore := NewRunStore(db)
	p := saveResumePipeline(t, store, "resume-cc", resumeConcurrencyDSL)

	reg := NewRunRegistry()
	// Occupy the only slot for the key so the resumed run's Acquire
	// hits ErrConcurrencyLimitReached.
	_, releaseBlocker, err := reg.Acquire(ctx, AcquireOpts{
		RunID: "run_blocker", WorkspaceID: "ws_test", ConcurrencyKey: "cc-gate",
	})
	if err != nil {
		t.Fatalf("acquire blocker: %v", err)
	}
	// Guard the slot release so a failed assertion before the explicit
	// release doesn't leave the resume retry loop spinning against a
	// permanently held slot during test teardown.
	releasedBlocker := false
	t.Cleanup(func() {
		if !releasedBlocker {
			releaseBlocker()
		}
	})

	insertInFlightRun(t, runStore, &RunRecord{
		ID: "run_resume_cc", WorkspaceID: "ws_test",
		PipelineID: p.ID, PipelineSlug: p.Slug,
		Status: RunStatusRunning, Mode: ModeRun,
		CurrentStepID: "b", StepOutputsJSON: `{"a":"restored-a"}`,
	})

	runner := newMockRunner()
	runner.outputsBySlug["s_b"] = []string{"out-b"}
	em := &captureEmitter{}
	exec := NewExecutor(store, NewResolver(db), runner, em).
		WithRunStore(runStore).
		WithRunRegistry(reg).
		WithResumeRetryBackoff(10*time.Millisecond, 50*time.Millisecond)

	resumed, interrupted, err := exec.ResumeInterruptedRuns(ctx, slog.Default())
	if err != nil {
		t.Fatalf("resume scan: %v", err)
	}
	if resumed != 1 || interrupted != 0 {
		t.Fatalf("resumed=%d interrupted=%d, want 1/0", resumed, interrupted)
	}

	// While the slot is held, the run must stay in-flight — the old
	// behaviour stamped it interrupted on the first rejection.
	time.Sleep(150 * time.Millisecond)
	rec, err := runStore.Get(ctx, "run_resume_cc")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Status != RunStatusRunning {
		t.Fatalf("run stamped %q while waiting for slot (error=%q), want running",
			rec.Status, rec.ErrorMessage)
	}

	// Free the slot — the retry loop must pick it up and finish.
	releaseBlocker()
	releasedBlocker = true
	final := waitForRunStatus(t, runStore, "run_resume_cc", RunStatusCompleted, 5*time.Second)
	if !strings.Contains(final.StepOutputsJSON, "out-b") {
		t.Errorf("resumed run did not execute step b: %s", final.StepOutputsJSON)
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	for _, call := range runner.calls {
		if call.AgentSlug == "s_a" {
			t.Errorf("step a was re-executed on retried resume")
		}
	}
}

// ---------------------------------------------------------------------------
// F3 — definition-hash drift gate
// ---------------------------------------------------------------------------

// TestResume_DefinitionHashMismatch_Interrupted pins the in-place-edit
// gap: an edit that keeps every step id but changes content must NOT
// resume old outputs against the changed definition.
func TestResume_DefinitionHashMismatch_Interrupted(t *testing.T) {
	db := openResumeTestDB(t)
	defer db.Close()
	ctx := context.Background()

	store := NewStore(db)
	runStore := NewRunStore(db)
	p := saveResumePipeline(t, store, "resume-hash", resumeLinearDSL)
	oldHash := p.DefinitionHash

	// In-place edit: same slug, same step ids, changed prompt content.
	edited := strings.Replace(resumeLinearDSL, `"prompt": "finish"`, `"prompt": "finish differently"`, 1)
	if edited == resumeLinearDSL {
		t.Fatal("test setup: edit did not change the definition")
	}
	p2 := saveResumePipeline(t, store, "resume-hash", edited)
	if p2.ID != p.ID {
		t.Fatalf("re-save changed pipeline id: %s -> %s", p.ID, p2.ID)
	}
	if p2.DefinitionHash == oldHash {
		t.Fatal("test setup: definition hash did not change")
	}

	// Run from the previous lifetime, stamped with the OLD hash.
	insertInFlightRun(t, runStore, &RunRecord{
		ID: "run_hash_drift", WorkspaceID: "ws_test",
		PipelineID: p.ID, PipelineSlug: p.Slug,
		Status: RunStatusRunning, Mode: ModeRun,
		CurrentStepID: "b", StepOutputsJSON: `{"a":"stale-a"}`,
		DefinitionHash: oldHash,
	})
	// Control: a run stamped with the CURRENT hash must still resume.
	insertInFlightRun(t, runStore, &RunRecord{
		ID: "run_hash_match", WorkspaceID: "ws_test",
		PipelineID: p.ID, PipelineSlug: p.Slug,
		Status: RunStatusRunning, Mode: ModeRun,
		CurrentStepID: "b", StepOutputsJSON: `{"a":"fresh-a"}`,
		DefinitionHash: p2.DefinitionHash,
	})

	runner := newMockRunner()
	em := &captureEmitter{}
	exec := NewExecutor(store, NewResolver(db), runner, em).WithRunStore(runStore)

	resumed, interrupted, err := exec.ResumeInterruptedRuns(ctx, slog.Default())
	if err != nil {
		t.Fatalf("resume scan: %v", err)
	}
	if resumed != 1 || interrupted != 1 {
		t.Fatalf("resumed=%d interrupted=%d, want 1/1", resumed, interrupted)
	}

	drift, err := runStore.Get(ctx, "run_hash_drift")
	if err != nil {
		t.Fatal(err)
	}
	if drift.Status != RunStatusInterrupted {
		t.Fatalf("hash-drift run status=%q, want interrupted", drift.Status)
	}
	if !strings.Contains(drift.ErrorMessage, "definition changed") {
		t.Errorf("hash-drift reason %q must mention the definition change", drift.ErrorMessage)
	}

	waitForRunStatus(t, runStore, "run_hash_match", RunStatusCompleted, 5*time.Second)
}

// TestExecutor_StampsDefinitionHashAtRunStart pins the write side of
// the gate: persistRunStart must stamp the pipeline's current
// definition hash onto the run row.
func TestExecutor_StampsDefinitionHashAtRunStart(t *testing.T) {
	db := openResumeTestDB(t)
	defer db.Close()
	ctx := context.Background()

	store := NewStore(db)
	runStore := NewRunStore(db)
	p := saveResumePipeline(t, store, "stamp-hash", resumeLinearDSL)
	if p.DefinitionHash == "" {
		t.Fatal("test setup: saved pipeline has no definition hash")
	}

	runner := newMockRunner()
	em := &captureEmitter{}
	exec := NewExecutor(store, NewResolver(db), runner, em).WithRunStore(runStore)

	res, err := exec.Run(ctx, RunInput{PipelineID: p.ID, WorkspaceID: "ws_test", Mode: ModeRun})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	rec, err := runStore.Get(ctx, res.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if rec.DefinitionHash != p.DefinitionHash {
		t.Errorf("run row definition_hash=%q, want %q", rec.DefinitionHash, p.DefinitionHash)
	}
}

// ---------------------------------------------------------------------------
// F6 — waitpoint timeout surfaces as "timed out", not "denied"
// ---------------------------------------------------------------------------

func TestResume_WaitTimeout_SurfacesTimedOutNotDenied(t *testing.T) {
	db := openResumeTestDB(t)
	defer db.Close()
	ctx := context.Background()

	store := NewStore(db)
	runStore := NewRunStore(db)
	p := saveResumePipeline(t, store, "resume-wait-timeout", resumeWaitDSL)

	insertInFlightRun(t, runStore, &RunRecord{
		ID: "run_wait_timeout", WorkspaceID: "ws_test",
		PipelineID: p.ID, PipelineSlug: p.Slug,
		Status: RunStatusRunning, Mode: ModeRun,
		CurrentStepID: "gate", StepOutputsJSON: `{"a":"out-a"}`,
	})
	// The waitpoint timed out while the process was down (the boot
	// sweep in RecoverPending would have stamped exactly this state).
	decidedAt := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.ExecContext(ctx, `
INSERT INTO pipeline_waitpoints (token, workspace_id, pipeline_run_id, step_id, kind, prompt, status, timeout_at, decided_at)
VALUES ('tok_timed_out', 'ws_test', 'run_wait_timeout', 'gate', 'approval', 'ok?', 'timed_out', ?, ?)`,
		decidedAt, decidedAt); err != nil {
		t.Fatalf("seed timed-out waitpoint: %v", err)
	}

	wpStore := NewSQLWaitpointStore(db)
	defer wpStore.Close()

	runner := newMockRunner()
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

	rec := waitForRunStatus(t, runStore, "run_wait_timeout", RunStatusFailed, 5*time.Second)
	if !strings.Contains(rec.ErrorMessage, "timed out") {
		t.Errorf("error %q must surface the timeout", rec.ErrorMessage)
	}
	if strings.Contains(rec.ErrorMessage, "denied") {
		t.Errorf("error %q must not claim the approval was denied", rec.ErrorMessage)
	}
}

// ---------------------------------------------------------------------------
// R1 — TOCTOU re-validation: definition edited while waiting for slot
// ---------------------------------------------------------------------------

// TestResume_DefinitionEditedWhileWaitingForSlot_Interrupted pins the
// TOCTOU window between the boot scan's drift gate and the actual
// re-entry: buildResumePlan validates the definition hash AS OF the
// scan, but the concurrency-slot retry loop in runResumedRun can wait
// arbitrarily long, and every retry's e.Run() reloads the pipeline
// fresh. An edit landing in that window must interrupt the run — not
// resume the old restored outputs against the changed definition.
func TestResume_DefinitionEditedWhileWaitingForSlot_Interrupted(t *testing.T) {
	db := openResumeTestDB(t)
	defer db.Close()
	ctx := context.Background()

	store := NewStore(db)
	runStore := NewRunStore(db)
	p := saveResumePipeline(t, store, "resume-toctou", resumeConcurrencyDSL)

	reg := NewRunRegistry()
	// Occupy the only slot for the key so the resumed run parks in the
	// retry loop instead of executing immediately.
	_, releaseBlocker, err := reg.Acquire(ctx, AcquireOpts{
		RunID: "run_blocker", WorkspaceID: "ws_test", ConcurrencyKey: "cc-gate",
	})
	if err != nil {
		t.Fatalf("acquire blocker: %v", err)
	}
	// Guard the slot release so a failed assertion before the explicit
	// release doesn't leave the resume retry loop spinning against a
	// permanently held slot during test teardown.
	releasedBlocker := false
	t.Cleanup(func() {
		if !releasedBlocker {
			releaseBlocker()
		}
	})

	insertInFlightRun(t, runStore, &RunRecord{
		ID: "run_toctou", WorkspaceID: "ws_test",
		PipelineID: p.ID, PipelineSlug: p.Slug,
		Status: RunStatusRunning, Mode: ModeRun,
		CurrentStepID: "b", StepOutputsJSON: `{"a":"stale-a"}`,
		DefinitionHash: p.DefinitionHash,
	})

	runner := newMockRunner()
	runner.outputsBySlug["s_b"] = []string{"out-b"}
	em := &captureEmitter{}
	exec := NewExecutor(store, NewResolver(db), runner, em).
		WithRunStore(runStore).
		WithRunRegistry(reg).
		WithResumeRetryBackoff(10*time.Millisecond, 50*time.Millisecond)

	// Scan-time gate passes: the stamped hash matches the definition.
	resumed, interrupted, err := exec.ResumeInterruptedRuns(ctx, slog.Default())
	if err != nil {
		t.Fatalf("resume scan: %v", err)
	}
	if resumed != 1 || interrupted != 0 {
		t.Fatalf("resumed=%d interrupted=%d, want 1/0", resumed, interrupted)
	}

	// Edit the pipeline WHILE the resumed run is parked on the slot —
	// same step ids, changed content, so only the hash catches it.
	edited := strings.Replace(resumeConcurrencyDSL, `"prompt": "do a"`, `"prompt": "do a differently"`, 1)
	if edited == resumeConcurrencyDSL {
		t.Fatal("test setup: edit did not change the definition")
	}
	p2 := saveResumePipeline(t, store, "resume-toctou", edited)
	if p2.ID != p.ID {
		t.Fatalf("re-save changed pipeline id: %s -> %s", p.ID, p2.ID)
	}
	if p2.DefinitionHash == p.DefinitionHash {
		t.Fatal("test setup: definition hash did not change")
	}

	// Free the slot — the retry's reload must now detect the drift.
	releaseBlocker()
	releasedBlocker = true

	final := waitForRunStatus(t, runStore, "run_toctou", RunStatusInterrupted, 5*time.Second)
	if !strings.Contains(final.ErrorMessage, "definition changed while waiting to resume") {
		t.Errorf("reason %q must say the definition changed while waiting to resume", final.ErrorMessage)
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if len(runner.calls) != 0 {
		t.Errorf("run executed %d steps against the changed definition, want 0", len(runner.calls))
	}
}

// TestResume_MatchingHashAfterSlotWait_StillResumes is the regression
// guard for the re-validation: a stamped hash that still matches the
// reloaded definition after the slot wait must resume and complete,
// not trip the new gate.
func TestResume_MatchingHashAfterSlotWait_StillResumes(t *testing.T) {
	db := openResumeTestDB(t)
	defer db.Close()
	ctx := context.Background()

	store := NewStore(db)
	runStore := NewRunStore(db)
	p := saveResumePipeline(t, store, "resume-toctou-ok", resumeConcurrencyDSL)

	reg := NewRunRegistry()
	_, releaseBlocker, err := reg.Acquire(ctx, AcquireOpts{
		RunID: "run_blocker", WorkspaceID: "ws_test", ConcurrencyKey: "cc-gate",
	})
	if err != nil {
		t.Fatalf("acquire blocker: %v", err)
	}
	// Guard the slot release so a failed assertion before the explicit
	// release doesn't leave the resume retry loop spinning against a
	// permanently held slot during test teardown.
	releasedBlocker := false
	t.Cleanup(func() {
		if !releasedBlocker {
			releaseBlocker()
		}
	})

	insertInFlightRun(t, runStore, &RunRecord{
		ID: "run_toctou_ok", WorkspaceID: "ws_test",
		PipelineID: p.ID, PipelineSlug: p.Slug,
		Status: RunStatusRunning, Mode: ModeRun,
		CurrentStepID: "b", StepOutputsJSON: `{"a":"restored-a"}`,
		DefinitionHash: p.DefinitionHash,
	})

	runner := newMockRunner()
	runner.outputsBySlug["s_b"] = []string{"out-b"}
	em := &captureEmitter{}
	exec := NewExecutor(store, NewResolver(db), runner, em).
		WithRunStore(runStore).
		WithRunRegistry(reg).
		WithResumeRetryBackoff(10*time.Millisecond, 50*time.Millisecond)

	resumed, interrupted, err := exec.ResumeInterruptedRuns(ctx, slog.Default())
	if err != nil {
		t.Fatalf("resume scan: %v", err)
	}
	if resumed != 1 || interrupted != 0 {
		t.Fatalf("resumed=%d interrupted=%d, want 1/0", resumed, interrupted)
	}

	// Let the run hit the busy slot at least once, then release.
	time.Sleep(50 * time.Millisecond)
	releaseBlocker()
	releasedBlocker = true

	final := waitForRunStatus(t, runStore, "run_toctou_ok", RunStatusCompleted, 5*time.Second)
	if !strings.Contains(final.StepOutputsJSON, "out-b") {
		t.Errorf("resumed run did not execute step b: %s", final.StepOutputsJSON)
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	for _, call := range runner.calls {
		if call.AgentSlug == "s_a" {
			t.Errorf("step a was re-executed on resume with matching hash")
		}
	}
}
