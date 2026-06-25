package pipeline

// Boot-time resume-from-step (Release 1.0 hardening W6).
//
// The executor persists current_step_id + step_outputs_json at every
// step boundary (executor.go persistStepEntry / persistStepOutputs),
// so after a hard kill the pipeline_runs row carries enough state to
// re-enter the run instead of stamping it "interrupted":
//
//   - completed steps are restored from step_outputs_json and skipped
//   - the in-flight step (current_step_id) re-executes from scratch —
//     at-least-once semantics for the step that was mid-flight
//   - runs parked on a `wait` approval step re-register their
//     listener on the ORIGINAL waitpoint token (WaitpointResumer), so
//     the approval card in the inbox stays answerable across restarts
//
// "Interrupted" remains the fallback whenever persisted state is
// insufficient to resume safely: missing/undecodable pipeline,
// definition drift (persisted step ids that no longer exist), or a
// non-resumable mode. Honesty over heroics — a wrong resume is worse
// than a clean interruption.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

// ErrResumeDefinitionChanged is returned by Run when a boot-time
// resume's re-validation (see resumeDefinitionDrift) detects that the
// pipeline definition changed between the scan-time gate and the
// actual re-entry — e.g. an edit landing while the resumed run waited
// on its concurrency slot.
var ErrResumeDefinitionChanged = errors.New("definition changed while waiting to resume")

// resumePlan is the validated state needed to re-enter one run.
type resumePlan struct {
	rec      *RunRecord
	inputs   map[string]any
	restored map[string]string
}

// resumeDefinitionDrift is the shared drift gate for resume-from-step:
// the persisted run state must still line up with the definition that
// would execute. Returns "" when consistent, otherwise a human-readable
// reason. Called once at scan time (buildResumePlan) and again inside
// Run on every resume re-entry — the concurrency-slot retry loop can
// wait arbitrarily long, and each retry reloads the pipeline fresh, so
// the scan-time check alone leaves a TOCTOU window.
func resumeDefinitionDrift(stampedHash, currentHash string, dsl *DSL, restored map[string]string, currentStepID string) string {
	// Content-hash gate (stamped at persistRunStart, v114): any edit
	// since the run started — including an in-place edit that keeps
	// every step id — means the persisted outputs were produced by
	// different steps than the ones that would execute now. Rows from
	// before v114 have no stamp and fall through to the weaker
	// step-id-existence gate below.
	if stampedHash != "" && stampedHash != currentHash {
		return "definition changed since run started (content hash mismatch)"
	}
	known := make(map[string]struct{}, len(dsl.Steps))
	for i := range dsl.Steps {
		known[dsl.Steps[i].ID] = struct{}{}
	}
	// Every persisted step id must still exist in the definition. A
	// renamed/removed step means the restored outputs no longer line
	// up with the steps that would execute — resuming could skip the
	// wrong work or double the right work.
	for stepID := range restored {
		if _, ok := known[stepID]; !ok {
			return fmt.Sprintf("definition drifted: persisted output for unknown step %q", stepID)
		}
	}
	if currentStepID != "" {
		if _, ok := known[currentStepID]; !ok {
			return fmt.Sprintf("definition drifted: current step %q no longer exists", currentStepID)
		}
	}
	return ""
}

// ResumeInterruptedRuns is the boot-time recovery scan. It walks every
// queued/running pipeline_runs row left over from the previous process
// lifetime and either re-enters the run from its last persisted step
// (own goroutine per run — a resumed run parked on a wait step may
// block for hours) or stamps it interrupted when state is
// insufficient.
//
// Returns (resumed, interrupted) counts; resumed counts runs whose
// re-entry was INITIATED — their eventual completion lands in
// pipeline_runs like any live run. The caller (server boot) logs both
// so abnormal interruption accumulation stays observable.
//
// The supplied ctx should be the server's lifetime context: a resumed
// run cancelled by shutdown finishes through the executor's normal
// cancellation path.
func (e *Executor) ResumeInterruptedRuns(ctx context.Context, logger *slog.Logger) (resumed, interrupted int, err error) {
	if e.runStore == nil {
		return 0, 0, errors.New("pipeline: resume requires a wired RunStore")
	}
	if logger == nil {
		logger = slog.Default()
	}
	// Lifetime fence (defense in depth against boot-ordering races):
	// the scan must only touch runs left over from a PREVIOUS process
	// lifetime. A row started at-or-after the cutoff, or whose id is
	// live in the RunRegistry, was started by THIS process (scheduler
	// tick or HTTP trigger firing before the scan) — "resuming" it
	// would launch a second concurrent execution under the same run
	// id. Such rows are skipped outright: not resumed, not stamped
	// interrupted (they are healthy and already running).
	cutoff := e.resumeCutoff
	if cutoff.IsZero() {
		cutoff = time.Now()
	}
	recs, err := e.runStore.ListInFlight(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("pipeline: resume scan: %w", err)
	}
	for _, rec := range recs {
		if !rec.StartedAt.Before(cutoff) {
			logger.Info("pipeline resume: skipping run started in current lifetime",
				"run_id", rec.ID, "started_at", rec.StartedAt, "boot_cutoff", cutoff)
			continue
		}
		if e.runs != nil && e.runs.IsLive(rec.ID) {
			logger.Info("pipeline resume: skipping run live in this process's registry",
				"run_id", rec.ID)
			continue
		}
		plan, reason := e.buildResumePlan(ctx, rec)
		if plan == nil {
			if markErr := e.runStore.MarkInterrupted(ctx, rec.ID, "not resumable after restart: "+reason); markErr != nil {
				logger.Warn("pipeline resume: interrupted fallback write failed",
					"run_id", rec.ID, "error", markErr)
			}
			logger.Info("pipeline run marked interrupted (state insufficient to resume)",
				"run_id", rec.ID, "pipeline_slug", rec.PipelineSlug, "reason", reason)
			interrupted++
			continue
		}
		logger.Info("resuming pipeline run from persisted step state",
			"run_id", rec.ID, "pipeline_slug", rec.PipelineSlug,
			"current_step_id", rec.CurrentStepID, "restored_steps", len(plan.restored))
		resumed++
		go e.runResumedRun(ctx, plan, logger)
	}
	return resumed, interrupted, nil
}

// ResumeAfterApproval re-enters a run that PARKED on a wait(approval) step
// (status=waiting) once the approval has been recorded. It is the in-process
// analogue of the boot-time scan: load the run row, build the same resume plan
// (restored step outputs + drift gate), and re-run via the normal Run path with
// resume=true. The resumed wait step finds the approval already decided and
// continues; completed steps are skipped from restored outputs.
//
// Call AFTER CompleteApproval has committed the decision (the approve HTTP
// handler does exactly this). Runs in its own goroutine on a detached context
// so it outlives the approve request; a shutdown mid-resume leaves the row
// in-flight for the next boot scan. Safe to call for non-resumable rows — it
// no-ops with a logged reason rather than erroring.
func (e *Executor) ResumeAfterApproval(runID string, logger *slog.Logger) {
	if e.runStore == nil {
		return
	}
	if logger == nil {
		logger = slog.Default()
	}
	ctx := context.Background()
	rec, err := e.runStore.Get(ctx, runID)
	if err != nil || rec == nil {
		logger.Warn("approval resume: run not found", "run_id", runID, "error", err)
		return
	}
	if rec.Status != RunStatusWaiting {
		// Not parked (already resumed, completed, or a non-suspend run) —
		// nothing to do. Avoids double-execution if approve fires twice.
		logger.Info("approval resume: run not in waiting state; skipping",
			"run_id", runID, "status", rec.Status)
		return
	}
	plan, reason := e.buildResumePlan(ctx, rec)
	if plan == nil {
		markCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = e.runStore.MarkInterrupted(markCtx, rec.ID, "not resumable after approval: "+reason)
		cancel()
		logger.Warn("approval resume: not resumable", "run_id", runID, "reason", reason)
		return
	}
	logger.Info("resuming pipeline run after approval", "run_id", runID, "pipeline_slug", rec.PipelineSlug)
	go e.runResumedRun(ctx, plan, logger)
}

// buildResumePlan validates that a run's persisted state is
// sufficient to resume. Returns (nil, reason) when it is not — the
// reason lands in the row's error_message so operators can see WHY a
// run fell back to interrupted.
func (e *Executor) buildResumePlan(ctx context.Context, rec *RunRecord) (*resumePlan, string) {
	if rec.Mode != ModeRun {
		// test_run is the save gate, dry_run never persists a row —
		// nobody is waiting on either after a restart; re-running
		// would only burn tokens.
		return nil, fmt.Sprintf("mode %q is not resumable (only live runs resume)", rec.Mode)
	}
	p, err := e.store.GetByID(ctx, rec.PipelineID)
	if err != nil {
		return nil, "pipeline not loadable: " + err.Error()
	}
	dsl, err := Parse([]byte(p.DefinitionJSON))
	if err != nil {
		return nil, "stored definition no longer parses: " + err.Error()
	}

	restored := map[string]string{}
	if rec.StepOutputsJSON != "" {
		if err := json.Unmarshal([]byte(rec.StepOutputsJSON), &restored); err != nil {
			return nil, "persisted step outputs unreadable: " + err.Error()
		}
	}
	// Drift gate — shared with Run's resume re-validation (see
	// resumeDefinitionDrift for the rationale behind each check).
	if reason := resumeDefinitionDrift(rec.DefinitionHash, p.DefinitionHash, dsl, restored, rec.CurrentStepID); reason != "" {
		return nil, reason
	}

	inputs := map[string]any{}
	if rec.InputsJSON != "" {
		if err := json.Unmarshal([]byte(rec.InputsJSON), &inputs); err != nil {
			return nil, "persisted inputs unreadable: " + err.Error()
		}
	}
	return &resumePlan{rec: rec, inputs: inputs, restored: restored}, ""
}

// runResumedRun re-enters one run through the normal Run path with
// the resume markers set. Run re-acquires the concurrency slot and
// registry entry (so /runs/{id}/cancel works on resumed runs), runDSL
// seeds the restored outputs and skips completed steps, and the
// existing terminal persistence lands the final row.
//
// IdempotencyKey is deliberately NOT carried over: the original key
// already maps to this run id in the dedupe store, and re-presenting
// it would short-circuit to DEDUPED instead of executing.
//
// Transient failures are retried, permanent ones interrupt:
//
//   - ErrConcurrencyLimitReached just means the slot is busy right
//     now (another resumed run, a fresh scheduled run on the same
//     key). Hours of restored step work must not be abandoned over a
//     timing collision, so we wait and retry with capped exponential
//     backoff for as long as the server lives — behaviourally the
//     run is queued on the slot. A shutdown mid-wait leaves the row
//     in 'running' so the NEXT boot's scan picks it up again.
//   - ErrDuplicateRunID means this run id is already executing on
//     this process (lifetime-fence race). Never stamp the row — that
//     would clobber the live run's state. Log and stand down.
//   - Anything else (pipeline reload failure, broken inputs) is
//     permanent for this lifetime → interrupted with the reason.
func (e *Executor) runResumedRun(ctx context.Context, plan *resumePlan, logger *slog.Logger) {
	rec := plan.rec
	backoff := e.resumeRetryBase
	if backoff <= 0 {
		backoff = 2 * time.Second
	}
	maxBackoff := e.resumeRetryMax
	if maxBackoff <= 0 {
		maxBackoff = time.Minute
	}
	for {
		res, err := e.Run(ctx, RunInput{
			PipelineID:      rec.PipelineID,
			WorkspaceID:     rec.WorkspaceID,
			InvokingCrewID:  rec.InvokingCrewID,
			InvokingAgentID: rec.InvokingAgentID,
			Inputs:          plan.inputs,
			Mode:            ModeRun,
			RunIDOverride:   rec.ID,
			TriggeredVia:    rec.TriggeredVia,
			TriggeredByID:   rec.TriggeredByID,
			resume:          true,
			restoredOutputs: plan.restored,
			restoredCostUSD: rec.CostUSD,
			// Carried so Run can re-validate the definition against the
			// state this run actually persisted — see the
			// ErrResumeDefinitionChanged case below.
			resumeDefinitionHash: rec.DefinitionHash,
			resumeCurrentStepID:  rec.CurrentStepID,
		})
		switch {
		case err == nil:
			logger.Info("resumed pipeline run finished", "run_id", rec.ID, "status", res.Status)
			return
		case errors.Is(err, ErrDuplicateRunID):
			logger.Warn("pipeline resume: run id already live on this process; standing down",
				"run_id", rec.ID)
			return
		case errors.Is(err, ErrResumeDefinitionChanged):
			// TOCTOU gate fired: the pipeline was edited between the
			// scan-time validation and this re-entry (e.g. while the
			// run waited on its concurrency slot). Same fallback as the
			// scan-time gate — interrupted, never a wrong resume.
			markCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			if markErr := e.runStore.MarkInterrupted(markCtx, rec.ID, err.Error()); markErr != nil {
				logger.Warn("pipeline resume: terminal fallback write failed",
					"run_id", rec.ID, "error", markErr)
			}
			cancel()
			logger.Info("pipeline run marked interrupted (definition changed while waiting to resume)",
				"run_id", rec.ID, "pipeline_slug", rec.PipelineSlug, "error", err)
			return
		case errors.Is(err, ErrConcurrencyLimitReached):
			logger.Info("pipeline resume: concurrency slot busy; will retry",
				"run_id", rec.ID, "retry_in", backoff)
			select {
			case <-ctx.Done():
				// Shutdown while queued on the slot. Leave the row in
				// 'running' — the next boot scan re-enters it with the
				// same restored state.
				logger.Info("pipeline resume: shutdown while waiting for slot; run left in-flight for next boot",
					"run_id", rec.ID)
				return
			case <-time.After(backoff):
			}
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
		default:
			// Run() failed before runDSL's terminal defer could take
			// over (pipeline reload, broken state) — close the audit
			// story here so the row doesn't sit in 'running' forever.
			// Fresh context: the resume ctx may already be shutting down.
			markCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			if markErr := e.runStore.MarkInterrupted(markCtx, rec.ID, "resume failed: "+err.Error()); markErr != nil {
				logger.Warn("pipeline resume: terminal fallback write failed",
					"run_id", rec.ID, "error", markErr)
			}
			cancel()
			logger.Warn("pipeline run resume failed", "run_id", rec.ID, "error", err)
			return
		}
	}
}
