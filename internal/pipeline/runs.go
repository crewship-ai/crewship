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
)

// RunMode is defined in types.go (ModeRun / ModeTestRun / ModeDryRun)
// and reused here. We don't redeclare to keep one source of truth for
// the mode set across the executor + the store.

// TriggeredVia documents how the run started. Used by the analytics
// page + the run-detail header so users see "fired by schedule X" vs
// "fired by webhook Y" without inferring from the parent_run_id.
type TriggeredVia string

const (
	TriggeredViaManual       TriggeredVia = "manual"
	TriggeredViaSchedule     TriggeredVia = "schedule"
	TriggeredViaWebhook      TriggeredVia = "webhook"
	TriggeredViaCallPipeline TriggeredVia = "call_pipeline"
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
	// NULL = unknown/HEAD. Note it is NOT a reliable drift signal: the
	// version store dedupes by content hash, so an edit cycle A→B→A
	// leaves head_version pointing at B's row while the live
	// definition is A. DefinitionHash below is the drift gate.
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
	InvokingCrewID   string
	InvokingAgentID  string
	InvokingUserID   string
	TriggeredVia     TriggeredVia
	TriggeredByID    string
	IdempotencyKey   string
	InputsJSON       string
	ConcurrencyKey   string
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// RunStore is the thin DB access layer. Keep methods small and
// composable so the executor can wire them inline at step boundaries
// without inventing a higher-level transaction abstraction.
type RunStore struct {
	db *sql.DB
}

// NewRunStore wraps a DB handle.
func NewRunStore(db *sql.DB) *RunStore {
	return &RunStore{db: db}
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
    inputs_json, concurrency_key, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.WorkspaceID, r.PipelineID, r.PipelineSlug, nullableIntPtr(r.PipelineVersion), nullableStr(r.DefinitionHash),
		string(r.Status), string(r.Mode), formatRFC3339(r.StartedAt), nullableTime(r.EndedAt), nullableStr(r.CurrentStepID),
		r.StepOutputsJSON, nullableStr(r.Output), r.CostUSD, r.DurationMs,
		nullableStr(r.ErrorMessage), nullableStr(r.FailedAtStep), nullableStr(r.ErrorFingerprint),
		nullableStr(r.InvokingCrewID), nullableStr(r.InvokingAgentID), nullableStr(r.InvokingUserID),
		string(r.TriggeredVia), nullableStr(r.TriggeredByID), nullableStr(r.IdempotencyKey),
		r.InputsJSON, nullableStr(r.ConcurrencyKey), formatRFC3339(r.CreatedAt), formatRFC3339(r.UpdatedAt))
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

// AppendStepOutput rewrites step_outputs_json with the supplied map.
// We don't try to merge JSON in SQL — the caller has the full map in
// memory anyway, and serializing once is cheaper than parsing+merging
// in SQLite. Cost + duration are accumulated by caller (Executor).
func (s *RunStore) AppendStepOutput(ctx context.Context, runID string, stepOutputs map[string]string, costUSD float64, durationMs int64) error {
	raw, err := json.Marshal(stepOutputs)
	if err != nil {
		return fmt.Errorf("pipeline_runs: marshal step outputs: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `
UPDATE pipeline_runs
SET step_outputs_json = ?, cost_usd = ?, duration_ms = ?, updated_at = datetime('now','subsec')
WHERE id = ?`, string(raw), costUSD, durationMs, runID)
	return err
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
	_, err := s.db.ExecContext(ctx, `
UPDATE pipeline_runs
SET status = ?, output = ?, error_message = ?, failed_at_step = ?,
    cost_usd = ?, duration_ms = ?, ended_at = ?,
    updated_at = datetime('now','subsec')
WHERE id = ?`,
		string(in.Status), nullableStr(in.Output), nullableStr(in.ErrorMessage), nullableStr(in.FailedAtStep),
		in.CostUSD, in.DurationMs, formatRFC3339(in.EndedAt), in.RunID)
	return err
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
		runSelectColumns+` WHERE workspace_id = ? AND status IN ('queued','running') ORDER BY started_at DESC`,
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
		runSelectColumns+` WHERE status IN ('queued','running') ORDER BY started_at ASC`)
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
	_, err := s.db.ExecContext(ctx, `
UPDATE pipeline_runs
SET status = 'interrupted',
    ended_at = COALESCE(ended_at, datetime('now','subsec')),
    error_message = ?,
    updated_at = datetime('now','subsec')
WHERE id = ? AND status IN ('queued','running')`, reason, runID)
	if err != nil {
		return fmt.Errorf("pipeline_runs: mark interrupted: %w", err)
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
	res, err := s.db.ExecContext(ctx, `
UPDATE pipeline_runs
SET status = 'interrupted',
    ended_at = COALESCE(ended_at, datetime('now','subsec')),
    error_message = COALESCE(NULLIF(error_message, ''), 'process restarted with run in flight'),
    updated_at = datetime('now','subsec')
WHERE status IN ('queued','running')`)
	if err != nil {
		return 0, fmt.Errorf("pipeline_runs: recover: %w", err)
	}
	n, _ := res.RowsAffected()
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
       inputs_json, COALESCE(concurrency_key,''), created_at, updated_at
FROM pipeline_runs`

// scanRunRow is the row-scanner contract — both sql.Row and sql.Rows
// satisfy it through Scan. Lets the same scanner serve Get and List
// without a copy.
type scanRunRow interface {
	Scan(dest ...any) error
}

func scanRun(row scanRunRow) (*RunRecord, error) {
	var r RunRecord
	var version sql.NullInt64
	var endedAt sql.NullString
	var startedAt, createdAt, updatedAt string
	var status, mode, triggeredVia string
	if err := row.Scan(
		&r.ID, &r.WorkspaceID, &r.PipelineID, &r.PipelineSlug, &version,
		&r.DefinitionHash,
		&status, &mode, &startedAt, &endedAt, &r.CurrentStepID,
		&r.StepOutputsJSON, &r.Output, &r.CostUSD, &r.DurationMs,
		&r.ErrorMessage, &r.FailedAtStep, &r.ErrorFingerprint,
		&r.InvokingCrewID, &r.InvokingAgentID, &r.InvokingUserID,
		&triggeredVia, &r.TriggeredByID, &r.IdempotencyKey,
		&r.InputsJSON, &r.ConcurrencyKey, &createdAt, &updatedAt,
	); err != nil {
		return nil, err
	}
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

func nullableIntPtr(p *int) any {
	if p == nil {
		return nil
	}
	return *p
}

func nullableTime(p *time.Time) any {
	if p == nil || p.IsZero() {
		return nil
	}
	return formatRFC3339(*p)
}

func formatRFC3339(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}

func parseRFC3339Opt(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t, nil
	}
	return time.Parse(time.RFC3339, s)
}
