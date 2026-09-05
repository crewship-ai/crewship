package pipeline

// pipeline_runs persistence — the dedicated run state introduced by
// migration v83. Replaces the journal-LIKE-scan path the list-runs UI
// used to take and gives boot recovery somewhere to mark interrupted
// in-flight runs (PIPELINES.md §17.6 / 17.7 production gap).
//
// Two writes per state change: this table + journal_entries. Journal
// stays the audit firehose + WS event source; this table is the
// query-optimized projection. Drift between the two is tolerated for
// readability — journal is canonical.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/tsformat"
)

// RunStatus is the closed set of pipeline_runs.status values. The DB
// column is unconstrained TEXT so we can add states without a
// migration; the Go layer enforces validity.
type RunStatus string

const (
	RunStatusQueued      RunStatus = "queued"
	RunStatusRunning     RunStatus = "running"
	RunStatusCompleted   RunStatus = "completed"
	RunStatusFailed      RunStatus = "failed"
	RunStatusCancelled   RunStatus = "cancelled"
	RunStatusDryRunOK    RunStatus = "dry_run"
	RunStatusInterrupted RunStatus = "interrupted" // boot-recovery marker for runs the previous lifetime didn't terminate
	RunStatusWaiting     RunStatus = "waiting"     // NON-terminal: parked on a human approval (wait step); resumes on approve
)

// RunMode is defined in types.go (ModeRun / ModeDryRun) and reused
// here. We don't redeclare to keep one source of truth for the mode
// set across the executor + the store.

// TriggeredVia documents how the run started. Used by the analytics
// page + the run-detail header so users see "fired by schedule X" vs
// "fired by webhook Y" without inferring from the parent_run_id.
type TriggeredVia string

const (
	TriggeredViaManual       TriggeredVia = "manual"
	TriggeredViaSchedule     TriggeredVia = "schedule"
	TriggeredViaWebhook      TriggeredVia = "webhook"
	TriggeredViaCallPipeline TriggeredVia = "call_pipeline"
	// TriggeredViaWakeCheck marks probe runs the scheduler fired to
	// evaluate a schedule's wake gate (pipeline_schedules.
	// wake_pipeline_id). Always an agentless routine; high-frequency
	// crons produce many of these, so run lists can filter them out
	// by this marker.
	TriggeredViaWakeCheck TriggeredVia = "wake_check"

	// TriggeredViaAutomation marks a run an automation rule started, so a
	// reader can tell a rule from a cron. Both arrive through pending_runs;
	// before this existed the dispatcher labelled every deferred run
	// "schedule" and the rule was only recoverable out of metadata_json.
	// TriggeredByID carries the automations.id.
	TriggeredViaAutomation TriggeredVia = "automation"
	// TriggeredViaIssue marks runs fired from an issue's "Run routine"
	// button. TriggeredByID carries the issue identifier (e.g. ENG-15)
	// so the runs list can JOIN back to missions for the source pill.
	TriggeredViaIssue TriggeredVia = "issue"
)

// RunRecord is the persisted shape. Pointer-typed timestamps are NULL
// in the DB until the run ends. step_outputs_json is opaque to the
// store; callers marshal/unmarshal as needed (typically map[string]string).
type RunRecord struct {
	ID           string
	WorkspaceID  string
	PipelineID   string
	PipelineSlug string
	// PipelineVersion mirrors pipelines.head_version at insert time.
	// NULL = unknown/HEAD. DefinitionHash below is the drift gate:
	// it compares content directly, independent of version
	// bookkeeping, and stays valid against pre-#996 rows where a
	// dedup'd A→B→A save left head_version stale.
	PipelineVersion *int
	// DefinitionHash is sha256(definition_json) of the pipeline AS IT
	// WAS when the run started (migration v114). Boot-time resume
	// compares it against the pipeline's current hash: any in-place
	// edit — even one that keeps every step id — makes the persisted
	// step outputs unsafe to replay against the changed definition.
	// Empty on rows from before v114; those fall back to the weaker
	// step-id-existence gate.
	DefinitionHash   string
	Status           RunStatus
	Mode             RunMode
	StartedAt        time.Time
	EndedAt          *time.Time
	CurrentStepID    string
	StepOutputsJSON  string
	Output           string
	CostUSD          float64
	DurationMs       int64
	ErrorMessage     string
	FailedAtStep     string
	ErrorFingerprint string
	// Outcome is the §9.6 routing decision (work package B6, #2349) —
	// NO_CHANGE | SUCCEEDED | WORK_CREATED | PARTIAL | NEEDS_HUMAN | FAILED
	// | CANCELLED, set once by MarkTerminal via orchestrator.DeriveOutcome
	// and never touched again. Empty on a run that predates this column.
	Outcome         string
	InvokingCrewID  string
	InvokingAgentID string
	InvokingUserID  string
	TriggeredVia    TriggeredVia
	TriggeredByID   string
	IdempotencyKey  string
	InputsJSON      string
	ConcurrencyKey  string
	// MetadataJSON is a typed scratchpad threaded through the run
	// (trigger.dev parity). Defaults to "{}"; readable from steps as
	// {{ run.metadata.X }}.
	MetadataJSON string
	// IsReplay is true when this run was created by replaying a prior
	// run; ReplayOf is that prior run's id. Injected into the render
	// context as {{ run.is_replay }} so steps can short-circuit side
	// effects on replay.
	IsReplay bool
	ReplayOf string
	// ChainDepth is how many COMPOSED hops separate this run from whatever a
	// human did (v20260807160100). A run a person started is 0; a routine that
	// run called is 1; an automation fired by an event that run emitted is 2.
	// Distinct from the executor's in-process `depth` argument, which resets
	// per top-level run and is not persisted — a chain can leave the process
	// through the journal and come back, and this is what survives that hop.
	// Bounded by MaxChainDepth; see GuardChainDepth.
	ChainDepth int
	// ChainOrigin is the id of the run or journal entry that started the
	// chain.
	//
	// It is ALWAYS set on a row this package writes: chainOriginForRun stamps
	// the run's own id when the run is itself the root, so a root row names
	// itself rather than saying nothing. The comment here used to claim the
	// opposite ("empty on a chain root"), which cost a reader a defensive
	// `chain_origin != ''` they did not need and sent them looking for a
	// convention the table does not use.
	//
	// Empty therefore means one of two things, and neither is "this is a
	// root": the row predates the column, or something other than
	// persistRunStart wrote it. RunChainReader.ChainOf treats empty as "no
	// answer" and lets the caller name the run — which reaches the same
	// result, so the two readings never disagreed about behaviour, only about
	// what the data means.
	ChainOrigin string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	// WarningsJSON is a JSON array of RunWarning entries — non-fatal
	// issues attached to the run (currently: failed after_all /
	// on_failure lifecycle hooks) that don't flip the terminal status
	// but must not be silently dropped. Defaults to '[]'. Use
	// Warnings() to decode.
	WarningsJSON string
}

// RunWarning is one non-fatal, run-scoped warning surfaced on the run
// record. Distinct from ErrorMessage: a run can carry warnings while
// still finishing COMPLETED — the main body succeeded, only a
// best-effort side channel (e.g. a teardown hook releasing a
// credential or closing a cost meter) failed. Structured (not just a
// log line) so the API/CLI can render it without scraping slog output.
type RunWarning struct {
	// Stage identifies where the warning originated, e.g. "hook after_all"
	// or "hook on_failure" — matches the `stage` label persistWarn already
	// logs, so a warning here can be cross-referenced with server logs.
	Stage   string    `json:"stage"`
	Message string    `json:"message"`
	At      time.Time `json:"at"`
}

// Warnings decodes WarningsJSON. Empty/invalid JSON decodes to nil
// (no warnings) rather than erroring — a run detail read should never
// fail just because the warnings side-channel is malformed.
func (r *RunRecord) Warnings() []RunWarning {
	if r == nil || r.WarningsJSON == "" || r.WarningsJSON == "[]" {
		return nil
	}
	var out []RunWarning
	if err := json.Unmarshal([]byte(r.WarningsJSON), &out); err != nil {
		return nil
	}
	return out
}

// RunStore is the thin DB access layer. Keep methods small and
// composable so the executor can wire them inline at step boundaries
// without inventing a higher-level transaction abstraction.
type RunStore struct {
	db *sql.DB
	// terminalNotifier, if set, fires after a run is committed to a
	// completed/failed terminal state. It runs on the finalize path so
	// scheduled runs (no connected client) still emit outbound
	// notifications; the callback must return promptly (fan out on its
	// own goroutine) so it never slows the terminal write. Optional.
	terminalNotifier TerminalNotifier
	// waitpointCanceller, if set, settles a run's pending waitpoints when the
	// run is marked interrupted. Optional, and a type assertion away from the
	// waitpoint store, so a wiring without one (or a test) still works.
	//
	// It hangs off the STATUS TRANSITION rather than off its callers because
	// resume.go marks runs interrupted from five places and the boot fallback
	// from two more; an invariant enforced at seven call sites is one the
	// eighth will miss. See MarkInterrupted for what goes wrong when it does.
	waitpointCanceller WaitpointCanceller
}

// TerminalNotifier is invoked once a run reaches a completed/failed
// terminal state. Wired via SetTerminalNotifier; the concrete
// implementation (internal/notify) loads the full record and dispatches
// to the workspace's outbound channels. Kept as a func so the pipeline
// package has no dependency on the notify subsystem (wiring lives in
// cmd_start.go).
type TerminalNotifier func(ctx context.Context, runID string, status RunStatus)

// NewRunStore wraps a DB handle.
func NewRunStore(db *sql.DB) *RunStore {
	return &RunStore{db: db}
}

// SetTerminalNotifier registers a callback fired after a run commits to
// a completed/failed terminal state. Idempotent to overwrite; pass nil
// to clear.
func (s *RunStore) SetTerminalNotifier(fn TerminalNotifier) {
	s.terminalNotifier = fn
}

// SetWaitpointCanceller registers the store used to settle a run's pending
// waitpoints when it is marked interrupted. Idempotent to overwrite; pass nil
// to clear.
func (s *RunStore) SetWaitpointCanceller(c WaitpointCanceller) {
	s.waitpointCanceller = c
}

// cancelWaitpointsFor settles every pending waitpoint belonging to runID.
//
// Best-effort by design, and deliberately not fatal: the run's status has
// already been written by the time this runs, and failing the caller would
// turn "the gate is still listed" into "the run is still marked in-flight",
// which is the worse of the two. The error is returned for logging.
func (s *RunStore) cancelWaitpointsFor(ctx context.Context, runID string) error {
	if s.waitpointCanceller == nil {
		return nil
	}
	_, err := s.waitpointCanceller.CancelWaitpointsForRun(ctx, runID)
	return err
}

// ErrRunNotFoundInStore signals that a Get-by-id (or any lookup)
// returned no row. Distinct from run_registry.ErrRunNotFound
// (in-memory cancel registry); the persistence layer needs its own
// sentinel because the in-memory store can be empty without it being
// an error condition (e.g., after restart, before fresh runs).
var ErrRunNotFoundInStore = errors.New("pipeline_runs: not found")

// Insert creates a fresh run row. Status defaults to "queued" if zero;
// CreatedAt + UpdatedAt are server-stamped if zero so callers can pass
// a partially-filled struct without remembering boilerplate.
func (s *RunStore) Insert(ctx context.Context, r *RunRecord) error {
	if r.ID == "" {
		return errors.New("pipeline_runs: id required")
	}
	if r.WorkspaceID == "" || r.PipelineID == "" {
		return errors.New("pipeline_runs: workspace_id + pipeline_id required")
	}
	if r.Status == "" {
		r.Status = RunStatusQueued
	}
	if r.Mode == "" {
		r.Mode = ModeRun
	}
	if r.TriggeredVia == "" {
		r.TriggeredVia = TriggeredViaManual
	}
	if r.StepOutputsJSON == "" {
		r.StepOutputsJSON = "{}"
	}
	if r.InputsJSON == "" {
		r.InputsJSON = "{}"
	}
	if r.MetadataJSON == "" {
		r.MetadataJSON = "{}"
	}
	if r.WarningsJSON == "" {
		r.WarningsJSON = "[]"
	}
	if r.StartedAt.IsZero() {
		r.StartedAt = time.Now().UTC()
	}
	now := time.Now().UTC()
	if r.CreatedAt.IsZero() {
		r.CreatedAt = now
	}
	if r.UpdatedAt.IsZero() {
		r.UpdatedAt = now
	}

	_, err := s.db.ExecContext(ctx, `
INSERT INTO pipeline_runs (
    id, workspace_id, pipeline_id, pipeline_slug, pipeline_version, definition_hash,
    status, mode, started_at, ended_at, current_step_id,
    step_outputs_json, output, cost_usd, duration_ms,
    error_message, failed_at_step, error_fingerprint,
    invoking_crew_id, invoking_agent_id, invoking_user_id,
    triggered_via, triggered_by_id, idempotency_key,
    inputs_json, concurrency_key, metadata_json, is_replay, replay_of, warnings_json,
    chain_depth, chain_origin, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.WorkspaceID, r.PipelineID, r.PipelineSlug, nullableIntPtr(r.PipelineVersion), nullableStr(r.DefinitionHash),
		string(r.Status), string(r.Mode), formatRFC3339(r.StartedAt), nullableTime(r.EndedAt), nullableStr(r.CurrentStepID),
		r.StepOutputsJSON, nullableStr(r.Output), r.CostUSD, r.DurationMs,
		nullableStr(r.ErrorMessage), nullableStr(r.FailedAtStep), nullableStr(r.ErrorFingerprint),
		nullableStr(r.InvokingCrewID), nullableStr(r.InvokingAgentID), nullableStr(r.InvokingUserID),
		string(r.TriggeredVia), nullableStr(r.TriggeredByID), nullableStr(r.IdempotencyKey),
		r.InputsJSON, nullableStr(r.ConcurrencyKey), r.MetadataJSON, boolToInt(r.IsReplay), nullableStr(r.ReplayOf), r.WarningsJSON,
		r.ChainDepth, nullableStr(r.ChainOrigin), formatRFC3339(r.CreatedAt), formatRFC3339(r.UpdatedAt))
	if err != nil {
		return fmt.Errorf("pipeline_runs: insert: %w", err)
	}
	return nil
}

// MarkRunning is the cheapest hot-path update — flips status to
// running and updates current_step_id without touching the heavier
// columns. Called at step entry.
func (s *RunStore) MarkRunning(ctx context.Context, runID, stepID string) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE pipeline_runs
SET status = 'running', current_step_id = ?, updated_at = datetime('now','subsec')
WHERE id = ?`, stepID, runID)
	return err
}

// MarkWaiting parks a run on a human approval (wait step): status=waiting,
// current_step_id=the wait step. NON-terminal — boot resume + ListActive
// include 'waiting' so the parked run survives a restart and shows in the UI.
// Approving the waitpoint triggers a resume that flips it onward.
func (s *RunStore) MarkWaiting(ctx context.Context, runID, stepID string) error {
	res, err := s.db.ExecContext(ctx, `
UPDATE pipeline_runs
SET status = 'waiting', current_step_id = ?, updated_at = datetime('now','subsec')
WHERE id = ? AND status IN ('queued','running','waiting')`, stepID, runID)
	if err != nil {
		return fmt.Errorf("pipeline_runs: mark waiting: %w", err)
	}
	// Durability + transition guard: the async WAITING contract requires a
	// persisted, still-live run row to resume from. The status filter ensures
	// we only park a queued/running/waiting run — 'waiting' is included so a
	// resume RE-PARK (#1428, 2.9) is idempotent — while a late/racing wait
	// update can never resurrect a terminal (completed/failed/cancelled/
	// interrupted) row back to 'waiting'. RowsAffected!=1 means no eligible row
	// matched, so the caller fails closed instead of surfacing a WAITING token
	// nothing can resume.
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("pipeline_runs: mark waiting rows: %w", err)
	}
	if n != 1 {
		return fmt.Errorf("pipeline_runs: mark waiting matched %d rows for run %q (no live row to park)", n, runID)
	}
	return nil
}

// UpsertStepOutput records ONE step's output as a single-row upsert into
// pipeline_run_step_outputs (migration v156), plus the cheap fixed-column
// cost/duration update on pipeline_runs itself. This replaced the old
// AppendStepOutput, which rewrote the entire step_outputs_json blob on
// every step boundary — O(1) bytes written here per call vs. O(N) bytes
// per call (O(N²) over a run) before. See #1411 item 4.
func (s *RunStore) UpsertStepOutput(ctx context.Context, runID, stepID, output string, costUSD float64, durationMs int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("pipeline_run_step_outputs: begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after a successful Commit

	now := formatRFC3339(time.Now().UTC())
	if _, err := tx.ExecContext(ctx, `
INSERT INTO pipeline_run_step_outputs (run_id, step_id, output, updated_at)
VALUES (?, ?, ?, ?)
ON CONFLICT (run_id, step_id) DO UPDATE SET output = excluded.output, updated_at = excluded.updated_at`,
		runID, stepID, output, now); err != nil {
		return fmt.Errorf("pipeline_run_step_outputs: upsert: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE pipeline_runs
SET cost_usd = ?, duration_ms = ?, updated_at = datetime('now','subsec')
WHERE id = ?`, costUSD, durationMs, runID); err != nil {
		return fmt.Errorf("pipeline_runs: update cost/duration: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("pipeline_run_step_outputs: commit: %w", err)
	}
	return nil
}

// FlushStepOutputs upserts every entry of a full step-outputs map in one
// transaction. Used only at the terminal write (executor.go), where it
// serves two purposes: catching any step whose output was never
// incrementally persisted (the DAG/parallel scheduler doesn't call
// UpsertStepOutput per step today — only the linear path does) and
// re-affirming already-persisted steps (idempotent, harmless). Unlike the
// old whole-blob rewrite this happens O(N) times ONCE per run, not on
// every step boundary.
func (s *RunStore) FlushStepOutputs(ctx context.Context, runID string, stepOutputs map[string]string, costUSD float64, durationMs int64) error {
	if len(stepOutputs) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("pipeline_run_step_outputs: flush begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after a successful Commit

	stmt, err := tx.PrepareContext(ctx, `
INSERT INTO pipeline_run_step_outputs (run_id, step_id, output, updated_at)
VALUES (?, ?, ?, ?)
ON CONFLICT (run_id, step_id) DO UPDATE SET output = excluded.output, updated_at = excluded.updated_at`)
	if err != nil {
		return fmt.Errorf("pipeline_run_step_outputs: flush prepare: %w", err)
	}
	defer stmt.Close()

	now := formatRFC3339(time.Now().UTC())
	for stepID, output := range stepOutputs {
		if _, err := stmt.ExecContext(ctx, runID, stepID, output, now); err != nil {
			return fmt.Errorf("pipeline_run_step_outputs: flush upsert step %s: %w", stepID, err)
		}
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE pipeline_runs
SET cost_usd = ?, duration_ms = ?, updated_at = datetime('now','subsec')
WHERE id = ?`, costUSD, durationMs, runID); err != nil {
		return fmt.Errorf("pipeline_runs: flush update cost/duration: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("pipeline_run_step_outputs: flush commit: %w", err)
	}
	return nil
}

// GetStepOutputs loads every persisted step output for one run from
// pipeline_run_step_outputs (migration v156). Replaces parsing
// RunRecord.StepOutputsJSON, which stopped being written on the hot path
// (see UpsertStepOutput) — this is now the only current read path for a
// run's step outputs; pre-migration runs are covered by the v156
// backfill. Returns an empty, non-nil map when the run has no rows yet.
func (s *RunStore) GetStepOutputs(ctx context.Context, runID string) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT step_id, output FROM pipeline_run_step_outputs WHERE run_id = ?`, runID)
	if err != nil {
		return nil, fmt.Errorf("pipeline_run_step_outputs: get: %w", err)
	}
	defer rows.Close()

	out := map[string]string{}
	for rows.Next() {
		var stepID, output string
		if err := rows.Scan(&stepID, &output); err != nil {
			return nil, fmt.Errorf("pipeline_run_step_outputs: scan: %w", err)
		}
		out[stepID] = output
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("pipeline_run_step_outputs: iterate: %w", err)
	}
	return out, nil
}

// MarkTerminal flips the row to a terminal status. Output, error,
// failed-at-step are written in one shot so the post-run reads see a
// fully-formed record (no torn-state read where status=completed but
// output is still empty from the previous step).
type MarkTerminalInput struct {
	RunID        string
	Status       RunStatus // completed | failed | cancelled | interrupted
	Output       string
	ErrorMessage string
	FailedAtStep string
	CostUSD      float64
	DurationMs   int64
	EndedAt      time.Time
}

// deriveRunOutcome computes the §9.6 outcome for a terminal pipeline run
// (work package B6, #2349) — the same orchestrator.DeriveOutcome
// finishAssignment uses for assignments, so the two run tables share one
// routing decision rather than each growing its own. Output is the run's
// final output text; ReportedOutcome looks for either existing structured
// hand-off shape in it (CHECKPOINT or HANDOFF) — a routine's agent_run step
// can carry either, if its prompt asks for one. errorMessage is returned
// updated: a run that ended cleanly (no error) but reported no valid
// outcome gets ReasonNoOutcomeReported written into it, exactly as
// finishAssignment does for assignments — reusing the existing column
// rather than adding a dedicated outcome_reason one (§9.4/§9.6). A run
// that already has a real error message is never overwritten.
func deriveRunOutcome(status RunStatus, output, errorMessage string) (outcome, resolvedErrorMessage string) {
	if status == RunStatusDryRunOK {
		// A dry run is, by definition, one that made no real change — the
		// closest of the seven values to what actually happened, and
		// exempt from the "no outcome reported" default: nobody asks a
		// dry-run tool call to self-report a routing decision.
		return orchestrator.OutcomeNoChange, errorMessage
	}
	technical := "completed"
	switch status {
	case RunStatusFailed, RunStatusInterrupted:
		technical = "failed"
	case RunStatusCancelled:
		technical = "cancelled"
	}
	outcome, reason := orchestrator.DeriveOutcome(technical, orchestrator.ReportedOutcome(output))
	if reason != "" && errorMessage == "" {
		errorMessage = reason
	}
	return outcome, errorMessage
}

// MarkTerminal commits the final state. Validates the status is
// actually terminal so a programmer can't accidentally pass "running".
func (s *RunStore) MarkTerminal(ctx context.Context, in MarkTerminalInput) error {
	switch in.Status {
	case RunStatusCompleted, RunStatusFailed, RunStatusCancelled, RunStatusInterrupted, RunStatusDryRunOK:
	default:
		return fmt.Errorf("pipeline_runs: %q is not a terminal status", in.Status)
	}
	if in.EndedAt.IsZero() {
		in.EndedAt = time.Now().UTC()
	}
	// Populate error_fingerprint on failure so the errors view can group
	// like failures and bulk-replay them. Stable across runs of the same
	// bug (step id + normalized message), so a fix → bulk replay flow has
	// a grouping key. NULL for non-failed terminal states.
	// §9.6 outcome contract (work package B6, #2349): computed from the SAME
	// inputs this write already commits (status, output, and whatever
	// error_message the caller passed in) so the outcome landing here can
	// never disagree with the row it lands on. May rewrite ErrorMessage —
	// see deriveRunOutcome's doc comment for when and why.
	outcome, resolvedErrorMessage := deriveRunOutcome(in.Status, in.Output, in.ErrorMessage)
	in.ErrorMessage = resolvedErrorMessage

	var fp any
	if in.Status == RunStatusFailed {
		fp = ErrorFingerprint(in.FailedAtStep, in.ErrorMessage)
	}
	_, err := s.db.ExecContext(ctx, `
UPDATE pipeline_runs
SET status = ?, output = ?, error_message = ?, failed_at_step = ?,
    error_fingerprint = ?,
    cost_usd = ?, duration_ms = ?, ended_at = ?,
    outcome = ?,
    updated_at = datetime('now','subsec')
WHERE id = ?`,
		string(in.Status), nullableStr(in.Output), nullableStr(in.ErrorMessage), nullableStr(in.FailedAtStep),
		fp,
		in.CostUSD, in.DurationMs, formatRFC3339(in.EndedAt), outcome, in.RunID)
	if err != nil {
		return err
	}
	// Fan out to outbound notification channels once the terminal state
	// is durably committed. Only completed/failed notify — cancelled and
	// interrupted are operational states, not run outcomes worth paging
	// on. Best-effort: the callback owns its own async/error handling and
	// must not affect the run.
	if s.terminalNotifier != nil && (in.Status == RunStatusCompleted || in.Status == RunStatusFailed) {
		s.terminalNotifier(ctx, in.RunID, in.Status)
	}
	// §12/§9.6 (work package B6, #2349): NEEDS_HUMAN is the only outcome
	// that reaches the inbox — same routing table finishAssignment uses for
	// assignments (orchestrator.RouteForOutcome), applied to the OTHER run
	// table §9.6 names. Best-effort: a run's terminal write must land
	// regardless of whether the inbox projection does.
	if orchestrator.RouteForOutcome(outcome).CreatesInboxItem {
		s.createOutcomeInboxItem(ctx, in.RunID, in.Output)
	}
	return nil
}

// AppendWarning adds one non-fatal warning to the run's warnings_json
// array. Read-modify-write under a transaction: warnings are rare (at
// most a couple per run — currently only failed after_all/on_failure
// hooks), so the extra round trip is cheap and far simpler than a SQL
// JSON-array append expression. Never touches status/error_message/
// ended_at — a warning must not look like (or race) the terminal
// write those columns carry.
func (s *RunStore) AppendWarning(ctx context.Context, runID, stage, message string) error {
	if runID == "" {
		return errors.New("pipeline_runs: run id required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("pipeline_runs: append warning: begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after a successful Commit

	var raw string
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(warnings_json,'[]') FROM pipeline_runs WHERE id = ?`, runID,
	).Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrRunNotFoundInStore
		}
		return fmt.Errorf("pipeline_runs: append warning: read: %w", err)
	}
	var warnings []RunWarning
	if raw != "" {
		// A malformed existing array shouldn't block recording the new
		// warning — start fresh rather than fail the write.
		_ = json.Unmarshal([]byte(raw), &warnings)
	}
	warnings = append(warnings, RunWarning{Stage: stage, Message: message, At: time.Now().UTC()})
	out, err := json.Marshal(warnings)
	if err != nil {
		return fmt.Errorf("pipeline_runs: append warning: marshal: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE pipeline_runs SET warnings_json = ?, updated_at = datetime('now','subsec') WHERE id = ?`,
		string(out), runID,
	); err != nil {
		return fmt.Errorf("pipeline_runs: append warning: write: %w", err)
	}
	return tx.Commit()
}

// Get fetches a single run by id. Returns ErrRunNotFound on miss.
func (s *RunStore) Get(ctx context.Context, runID string) (*RunRecord, error) {
	row := s.db.QueryRowContext(ctx, runSelectColumns+` WHERE id = ?`, runID)
	r, err := scanRun(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrRunNotFoundInStore
	}
	return r, err
}

// ListByPipeline returns runs for a pipeline ordered newest-first.
// Limit caps payload size; status filter optional.
func (s *RunStore) ListByPipeline(ctx context.Context, pipelineID string, status RunStatus, limit int) ([]*RunRecord, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	q := runSelectColumns + ` WHERE pipeline_id = ?`
	args := []any{pipelineID}
	if status != "" {
		q += ` AND status = ?`
		args = append(args, string(status))
	}
	q += ` ORDER BY started_at DESC LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("pipeline_runs: list: %w", err)
	}
	defer rows.Close()
	var out []*RunRecord
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ListActive returns all currently in-flight runs in a workspace.
// Used by the orchestration UI active-runs panel and by the boot
// recovery scan.
func (s *RunStore) ListActive(ctx context.Context, workspaceID string) ([]*RunRecord, error) {
	rows, err := s.db.QueryContext(ctx,
		runSelectColumns+` WHERE workspace_id = ? AND status IN ('queued','running','waiting') ORDER BY started_at DESC`,
		workspaceID)
	if err != nil {
		return nil, fmt.Errorf("pipeline_runs: list active: %w", err)
	}
	defer rows.Close()
	var out []*RunRecord
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ListInFlight returns every queued/running run across ALL
// workspaces. Used by the boot-time resume scan, which runs before
// any workspace context exists — the per-workspace variant
// (ListActive) serves the UI panels.
func (s *RunStore) ListInFlight(ctx context.Context) ([]*RunRecord, error) {
	rows, err := s.db.QueryContext(ctx,
		runSelectColumns+` WHERE status IN ('queued','running','waiting') ORDER BY started_at ASC`)
	if err != nil {
		return nil, fmt.Errorf("pipeline_runs: list in-flight: %w", err)
	}
	defer rows.Close()
	var out []*RunRecord
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// MarkInterrupted is the single-row fallback used by the boot resume
// scan when a run's persisted state is insufficient to resume safely
// (missing pipeline, schema drift, non-resumable mode). Guarded on
// status so a run that resumed and finished between the scan's read
// and this write is never clobbered back to interrupted.
func (s *RunStore) MarkInterrupted(ctx context.Context, runID, reason string) error {
	if reason == "" {
		reason = "process restarted with run in flight"
	}
	// outcome (§9.6, work package B6, #2349): an interrupted run never gets a
	// chance to report one — the process that would have run its agent step
	// and parsed a hand-off is gone — so it goes straight to FAILED rather
	// than through DeriveOutcome's "no outcome reported" default, which
	// exists for a run that COMPLETED without saying anything, not one the
	// server itself declared abandoned.
	res, err := s.db.ExecContext(ctx, `
UPDATE pipeline_runs
SET status = 'interrupted',
    ended_at = COALESCE(ended_at, datetime('now','subsec')),
    error_message = ?,
    outcome = COALESCE(outcome, ?),
    updated_at = datetime('now','subsec')
WHERE id = ? AND status IN ('queued','running','waiting')`, reason, orchestrator.OutcomeFailed, runID)
	if err != nil {
		return fmt.Errorf("pipeline_runs: mark interrupted: %w", err)
	}
	// Only cascade if the guard let the write through. A run that finished
	// between the resume scan's read and this write was not interrupted, and
	// cancelling its waitpoints would settle a gate that is still legitimately
	// someone's to decide.
	if n, _ := res.RowsAffected(); n > 0 {
		if cerr := s.cancelWaitpointsFor(ctx, runID); cerr != nil {
			return fmt.Errorf("pipeline_runs: mark interrupted: cancel waitpoints for %s: %w", runID, cerr)
		}
	}
	return nil
}

// RecoverInterruptedAtBoot is the boot-time scan that promotes any
// run still in queued/running from a previous process lifetime to
// "interrupted". Counterpart to the waitpoint recovery scan added in
// the stabilization commit. Kept as the bulk fallback path for when
// resume is disabled (CREWSHIP_PIPELINE_RESUME=off) or no executor
// can be wired at boot; the default boot path is
// Executor.ResumeInterruptedRuns (resume.go), which re-enters runs
// from their last persisted step and only stamps "interrupted" when
// state is insufficient.
//
// Returns how many rows were promoted. The boot wireup logs the
// count so abnormal accumulation is observable.
func (s *RunStore) RecoverInterruptedAtBoot(ctx context.Context) (int, error) {
	// Ids first: a bulk UPDATE cannot say which rows it moved, and the
	// waitpoint cascade below is per-run. Anything that finishes between this
	// read and the write simply fails the guarded re-check afterwards.
	var candidates []string
	if s.waitpointCanceller != nil {
		rows, qerr := s.db.QueryContext(ctx,
			`SELECT id FROM pipeline_runs WHERE status IN ('queued','running')`)
		if qerr != nil {
			return 0, fmt.Errorf("pipeline_runs: recover scan: %w", qerr)
		}
		for rows.Next() {
			var id string
			if rows.Scan(&id) == nil {
				candidates = append(candidates, id)
			}
		}
		rows.Close()
		if rerr := rows.Err(); rerr != nil {
			return 0, fmt.Errorf("pipeline_runs: recover scan: %w", rerr)
		}
	}

	res, err := s.db.ExecContext(ctx, `
UPDATE pipeline_runs
SET status = 'interrupted',
    ended_at = COALESCE(ended_at, datetime('now','subsec')),
    error_message = COALESCE(NULLIF(error_message, ''), 'process restarted with run in flight'),
    outcome = COALESCE(outcome, ?),
    updated_at = datetime('now','subsec')
WHERE status IN ('queued','running')`, orchestrator.OutcomeFailed)
	if err != nil {
		return 0, fmt.Errorf("pipeline_runs: recover: %w", err)
	}
	n, _ := res.RowsAffected()

	// Same invariant as MarkInterrupted, and the same reason it is
	// best-effort: the statuses are already committed, so a cascade failure
	// must not make the caller think the recovery itself failed.
	for _, id := range candidates {
		var status string
		if serr := s.db.QueryRowContext(ctx,
			`SELECT status FROM pipeline_runs WHERE id = ?`, id).Scan(&status); serr != nil || status != "interrupted" {
			continue
		}
		_ = s.cancelWaitpointsFor(ctx, id)
	}
	return int(n), nil
}

// ResolveByIdempotencyKey returns the run_id of a prior run that
// matches (workspace_id, idempotency_key), or "" if no match. The
// idempotency layer (idempotency.go) stays the source of truth for
// dedupe; this is a convenience for diagnostic queries.
func (s *RunStore) ResolveByIdempotencyKey(ctx context.Context, workspaceID, key string) (string, error) {
	var id string
	err := s.db.QueryRowContext(ctx,
		`SELECT id FROM pipeline_runs WHERE workspace_id = ? AND idempotency_key = ? ORDER BY started_at DESC LIMIT 1`,
		workspaceID, key,
	).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}

// ---- internals ----

const runSelectColumns = `
SELECT id, workspace_id, pipeline_id, pipeline_slug, pipeline_version,
       COALESCE(definition_hash,''),
       status, mode, started_at, ended_at, COALESCE(current_step_id,''),
       step_outputs_json, COALESCE(output,''), cost_usd, duration_ms,
       COALESCE(error_message,''), COALESCE(failed_at_step,''), COALESCE(error_fingerprint,''),
       COALESCE(invoking_crew_id,''), COALESCE(invoking_agent_id,''), COALESCE(invoking_user_id,''),
       triggered_via, COALESCE(triggered_by_id,''), COALESCE(idempotency_key,''),
       inputs_json, COALESCE(concurrency_key,''),
       COALESCE(metadata_json,'{}'), COALESCE(is_replay,0), COALESCE(replay_of,''),
       COALESCE(warnings_json,'[]'),
       COALESCE(chain_depth,0), COALESCE(chain_origin,''),
       created_at, updated_at, COALESCE(outcome,'')
FROM pipeline_runs`

// scanRunRow is the row-scanner contract — both sql.Row and sql.Rows
// satisfy it through Scan. Lets the same scanner serve Get and List
// without a copy.
type scanRunRow interface {
	Scan(dest ...any) error
}

// scanRun materializes a RunRecord from a row selected with
// runSelectColumns (shared by Get and the list paths).
func scanRun(row scanRunRow) (*RunRecord, error) {
	var r RunRecord
	var version sql.NullInt64
	var endedAt sql.NullString
	var startedAt, createdAt, updatedAt string
	var status, mode, triggeredVia string
	var isReplay int64
	if err := row.Scan(
		&r.ID, &r.WorkspaceID, &r.PipelineID, &r.PipelineSlug, &version,
		&r.DefinitionHash,
		&status, &mode, &startedAt, &endedAt, &r.CurrentStepID,
		&r.StepOutputsJSON, &r.Output, &r.CostUSD, &r.DurationMs,
		&r.ErrorMessage, &r.FailedAtStep, &r.ErrorFingerprint,
		&r.InvokingCrewID, &r.InvokingAgentID, &r.InvokingUserID,
		&triggeredVia, &r.TriggeredByID, &r.IdempotencyKey,
		&r.InputsJSON, &r.ConcurrencyKey,
		&r.MetadataJSON, &isReplay, &r.ReplayOf,
		&r.WarningsJSON,
		&r.ChainDepth, &r.ChainOrigin,
		&createdAt, &updatedAt, &r.Outcome,
	); err != nil {
		return nil, err
	}
	r.IsReplay = isReplay != 0
	r.Status = RunStatus(status)
	r.Mode = RunMode(mode)
	r.TriggeredVia = TriggeredVia(triggeredVia)
	if version.Valid {
		v := int(version.Int64)
		r.PipelineVersion = &v
	}
	var startedAtErr error
	r.StartedAt, startedAtErr = parseRFC3339Opt(startedAt)
	if startedAtErr != nil {
		// Zero-time StartedAt makes the row look older than any boot
		// cutoff (i.e. resumable) — keep that behaviour, but don't let
		// a corrupt timestamp pass silently.
		slog.Warn("pipeline runs: unparseable started_at on run row; treating as zero time",
			"run_id", r.ID, "started_at", startedAt, "error", startedAtErr)
	}
	if endedAt.Valid && endedAt.String != "" {
		t, _ := parseRFC3339Opt(endedAt.String)
		r.EndedAt = &t
	}
	r.CreatedAt, _ = parseRFC3339Opt(createdAt)
	r.UpdatedAt, _ = parseRFC3339Opt(updatedAt)
	return &r, nil
}

// nullableIntPtr returns the pointed-to int, or nil for a nil pointer —
// so an unset optional int binds as SQL NULL.
func nullableIntPtr(p *int) any {
	if p == nil {
		return nil
	}
	return *p
}

// nullableTime returns the fixed-width timestamp string for a non-zero
// time, or nil — so an unset/zero time binds as SQL NULL.
func nullableTime(p *time.Time) any {
	if p == nil || p.IsZero() {
		return nil
	}
	return formatRFC3339(*p)
}

// formatRFC3339 renders a time as the lex-sortable UTC timestamp string
// the pipeline_runs timestamp columns store. Fixed-width (tsformat, #990):
// RFC3339Nano truncates trailing-zero fractions, which breaks string
// comparison inside a shared second — the quartermaster sampler compares
// ended_at against its scan bounds lexicographically.
func formatRFC3339(t time.Time) string {
	return tsformat.Format(t)
}

// parseRFC3339Opt parses an optional RFC3339Nano string; empty → zero time.
func parseRFC3339Opt(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t, nil
	}
	return time.Parse(time.RFC3339, s)
}
