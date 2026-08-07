package pipeline

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// PendingRun is a deferred trigger parked in pending_runs (v122) — the
// backing for delay / ttl / debounce / priority. The dispatcher fires
// due rows (FireAt <= now), highest Priority first, and expires rows
// past ExpiresAt.
type PendingRun struct {
	ID            string
	WorkspaceID   string
	PipelineID    string
	PipelineSlug  string
	InputsJSON    string
	TagsJSON      string
	MetadataJSON  string
	TierOverride  string
	Priority      int
	DebounceKey   string
	FireAt        time.Time
	ExpiresAt     *time.Time
	DebounceMaxAt *time.Time
	// InvokingUserID is the workspace user who enqueued this deferred run,
	// threaded through to the fired run so a notify step can resolve
	// `to: trigger` to a real recipient (issue #842 Phase 1). Empty for
	// service/token triggers → `to: trigger` falls back to a workspace notice.
	InvokingUserID string
	// TriggeredVia / TriggeredByID are what actually started this deferred
	// run. Empty means "did not say" — effectivePendingTrigger applies the
	// dispatcher's documented default — which is a different fact from
	// claiming a schedule.
	TriggeredVia  TriggeredVia
	TriggeredByID string
	// ChainDepth is how many composed hops led here. Threaded so a cycle
	// that leaves the process through the journal and comes back still
	// spends from the same budget runCallPipelineStep spends from.
	ChainDepth int
}

// effectivePendingTrigger resolves what a fired deferred run should claim.
//
// One function so the default lives in one place: the dispatcher used to
// inline it, which is why an attributed producer had nowhere to put its
// answer even once it had one.
func effectivePendingTrigger(pr PendingRun) (TriggeredVia, string) {
	if pr.TriggeredVia != "" {
		return pr.TriggeredVia, pr.TriggeredByID
	}
	return TriggeredViaSchedule, pr.ID
}

// PendingRunStore is the DB access layer for deferred dispatch.
type PendingRunStore struct {
	db *sql.DB
}

// NewPendingRunStore wraps a DB handle.
func NewPendingRunStore(db *sql.DB) *PendingRunStore {
	return &PendingRunStore{db: db}
}

// Enqueue parks a deferred trigger. When DebounceKey is set and a
// pending row already exists for (pipeline_id, debounce_key), the
// existing row is COALESCED: its fire_at is pushed to the new FireAt
// (capped at the original debounce_max_at), inputs/tags/metadata are
// replaced, and the existing id is returned. Otherwise a fresh row is
// inserted. Returns (id, coalesced, error).
func (s *PendingRunStore) Enqueue(ctx context.Context, pr PendingRun) (string, bool, error) {
	if pr.ID == "" {
		return "", false, errors.New("pending_runs: id required")
	}
	if pr.DebounceKey != "" {
		// If a pending row for this key already exists, coalesce into it.
		var existing int
		if err := s.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM pending_runs WHERE pipeline_id = ? AND debounce_key = ? AND status = 'pending'`,
			pr.PipelineID, pr.DebounceKey).Scan(&existing); err != nil {
			return "", false, fmt.Errorf("pending_runs: debounce lookup: %w", err)
		}
		if existing > 0 {
			return s.coalesceDebounce(ctx, pr)
		}
	}

	_, err := s.db.ExecContext(ctx, `
INSERT INTO pending_runs (
    id, workspace_id, pipeline_id, pipeline_slug, inputs_json, tags_json, metadata_json,
    tier_override, priority, debounce_key, fire_at, expires_at, debounce_max_at,
    invoking_user_id, triggered_via, triggered_by_id, chain_depth, status, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'pending', datetime('now','subsec'), datetime('now','subsec'))`,
		pr.ID, pr.WorkspaceID, pr.PipelineID, pr.PipelineSlug,
		orJSON(pr.InputsJSON, "{}"), orJSON(pr.TagsJSON, "[]"), orJSON(pr.MetadataJSON, "{}"),
		nullableStr(pr.TierOverride), pr.Priority, nullableStr(pr.DebounceKey),
		pr.FireAt.UTC().Format(time.RFC3339Nano), nullableTime(pr.ExpiresAt), nullableTime(pr.DebounceMaxAt),
		nullableStr(pr.InvokingUserID),
		nullableStr(string(pr.TriggeredVia)), nullableStr(pr.TriggeredByID), pr.ChainDepth)
	if err != nil {
		// Debounce race: a concurrent trigger with the same key inserted
		// first, so the partial-unique index rejects this one. Both
		// requests SELECTed empty before either INSERTed — fall back to the
		// coalesce path (the row now exists) instead of surfacing a 500.
		if pr.DebounceKey != "" && isUniqueViolation(err) {
			return s.coalesceDebounce(ctx, pr)
		}
		return "", false, fmt.Errorf("pending_runs: insert: %w", err)
	}
	return pr.ID, false, nil
}

// coalesceDebounce updates the existing pending row for (pipeline,
// debounce_key) — the merge path shared by Enqueue's normal coalesce and
// its race fallback.
func (s *PendingRunStore) coalesceDebounce(ctx context.Context, pr PendingRun) (string, bool, error) {
	var existingID string
	var maxAt sql.NullString
	if err := s.db.QueryRowContext(ctx, `
SELECT id, COALESCE(debounce_max_at,'') FROM pending_runs
WHERE pipeline_id = ? AND debounce_key = ? AND status = 'pending'`,
		pr.PipelineID, pr.DebounceKey).Scan(&existingID, &maxAt); err != nil {
		return "", false, fmt.Errorf("pending_runs: coalesce lookup: %w", err)
	}
	fireAt := pr.FireAt
	if maxAt.String != "" {
		if cap, perr := time.Parse(time.RFC3339Nano, maxAt.String); perr == nil && fireAt.After(cap) {
			fireAt = cap
		}
	}
	// Coalescing adopts the LATEST trigger's payload (inputs/tags/metadata),
	// so it must also adopt its invoking user — otherwise a run that fires
	// with user B's inputs would notify user A (the original enqueuer) on a
	// `to: trigger` step. Attribution follows the payload it belongs to.
	//
	// triggered_via / triggered_by_id are attribution in the same sense and
	// move for the same reason. debounce_key is caller-supplied on the
	// deferred-run endpoint, so an automation's row and a user's defer can
	// meet on one row in either order; leaving the byline behind meant the
	// FIRST producer got credit for the LAST one's payload. Both directions
	// are wrong and only one of them is quiet: a user's inputs firing under a
	// rule's name is a forged audit trail, and a rule's run reading as a cron
	// is the exact confusion the columns were added to end.
	if _, err := s.db.ExecContext(ctx, `
UPDATE pending_runs
SET inputs_json = ?, tags_json = ?, metadata_json = ?, tier_override = ?,
    priority = ?, fire_at = ?, expires_at = ?, invoking_user_id = ?,
    triggered_via = ?, triggered_by_id = ?,
    updated_at = datetime('now','subsec')
WHERE id = ?`,
		orJSON(pr.InputsJSON, "{}"), orJSON(pr.TagsJSON, "[]"), orJSON(pr.MetadataJSON, "{}"),
		nullableStr(pr.TierOverride), pr.Priority, fireAt.UTC().Format(time.RFC3339Nano),
		nullableTime(pr.ExpiresAt), nullableStr(pr.InvokingUserID),
		nullableStr(string(pr.TriggeredVia)), nullableStr(pr.TriggeredByID), existingID); err != nil {
		return "", false, fmt.Errorf("pending_runs: coalesce: %w", err)
	}
	return existingID, true, nil
}

// ExpireDue marks pending rows past their ttl as expired. Returns the
// count. Run before DueRuns so an expired-but-due row never fires.
func (s *PendingRunStore) ExpireDue(ctx context.Context, now time.Time) (int, error) {
	res, err := s.db.ExecContext(ctx, `
UPDATE pending_runs SET status = 'expired', updated_at = datetime('now','subsec')
WHERE status = 'pending' AND expires_at IS NOT NULL AND expires_at <= ?`,
		now.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// DueRuns returns pending rows whose fire_at has arrived, highest
// priority first (FIFO within a priority). Caller fires each, then
// MarkFired/MarkFailed.
func (s *PendingRunStore) DueRuns(ctx context.Context, now time.Time, limit int) ([]PendingRun, error) {
	if limit <= 0 || limit > 100 {
		limit = 25
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, workspace_id, pipeline_id, pipeline_slug, inputs_json, tags_json, metadata_json,
       COALESCE(tier_override,''), priority, COALESCE(invoking_user_id,''),
       COALESCE(triggered_via,''), COALESCE(triggered_by_id,''), COALESCE(chain_depth,0)
FROM pending_runs
WHERE status = 'pending' AND fire_at <= ?
ORDER BY priority DESC, created_at ASC
LIMIT ?`, now.UTC().Format(time.RFC3339Nano), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PendingRun
	for rows.Next() {
		var pr PendingRun
		if err := rows.Scan(&pr.ID, &pr.WorkspaceID, &pr.PipelineID, &pr.PipelineSlug,
			&pr.InputsJSON, &pr.TagsJSON, &pr.MetadataJSON, &pr.TierOverride, &pr.Priority,
			&pr.InvokingUserID, &pr.TriggeredVia, &pr.TriggeredByID, &pr.ChainDepth); err != nil {
			return nil, err
		}
		out = append(out, pr)
	}
	return out, rows.Err()
}

// MarkFired records that a pending row dispatched into run runID. Scoped
// to status='pending' so a concurrent dispatcher can't double-fire.
func (s *PendingRunStore) MarkFired(ctx context.Context, id, runID string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `
UPDATE pending_runs SET status = 'fired', fired_run_id = ?, updated_at = datetime('now','subsec')
WHERE id = ? AND status = 'pending'`, runID, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// SetFiredRunID backfills the dispatched run id after a claim (which
// stamps status='fired' with an empty run id). No status guard — the
// row is already ours post-claim.
func (s *PendingRunStore) SetFiredRunID(ctx context.Context, id, runID string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE pending_runs SET fired_run_id = ?, updated_at = datetime('now','subsec') WHERE id = ?`,
		runID, id)
	return err
}

// Cancel removes a pending row before it fires.
func (s *PendingRunStore) Cancel(ctx context.Context, workspaceID, id string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `
UPDATE pending_runs SET status = 'cancelled', updated_at = datetime('now','subsec')
WHERE id = ? AND workspace_id = ? AND status = 'pending'`, id, workspaceID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ListPending returns a workspace's not-yet-fired deferred runs.
func (s *PendingRunStore) ListPending(ctx context.Context, workspaceID string, limit int) ([]PendingRun, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, workspace_id, pipeline_id, pipeline_slug, COALESCE(debounce_key,''), priority, fire_at
FROM pending_runs WHERE workspace_id = ? AND status = 'pending'
ORDER BY fire_at ASC LIMIT ?`, workspaceID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PendingRun
	for rows.Next() {
		var pr PendingRun
		var fireAt string
		if err := rows.Scan(&pr.ID, &pr.WorkspaceID, &pr.PipelineID, &pr.PipelineSlug,
			&pr.DebounceKey, &pr.Priority, &fireAt); err != nil {
			return nil, err
		}
		pr.FireAt, _ = time.Parse(time.RFC3339Nano, fireAt)
		out = append(out, pr)
	}
	return out, rows.Err()
}

// orJSON returns v, or fallback when v is empty — keeps the JSON columns
// non-NULL without the caller pre-filling defaults.
func orJSON(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

// RunDepthReader answers how deep a run already sat in a composed chain. It
// satisfies automation.DepthSource, so the registry can price a hop without
// importing a database handle or knowing the column's name.
//
// Lives here rather than in internal/automation because the row is this
// package's: a second query for pipeline_runs.chain_depth somewhere else is
// a second answer to "how deep are we", which is the failure the single
// GuardChainDepth exists to prevent.
type RunDepthReader struct{ db *sql.DB }

func NewRunDepthReader(db *sql.DB) *RunDepthReader { return &RunDepthReader{db: db} }

// ChainDepthOf returns the run's depth. The false return means "no such run
// in this workspace", which the caller reads as a human-caused root — an
// unknown run must not be treated as an ERROR, or a journal entry from any
// other producer would refuse every rule.
func (r *RunDepthReader) ChainDepthOf(ctx context.Context, workspaceID, runID string) (int, bool, error) {
	if r == nil || r.db == nil || workspaceID == "" || runID == "" {
		return 0, false, nil
	}
	var depth sql.NullInt64
	err := r.db.QueryRowContext(ctx,
		`SELECT chain_depth FROM pipeline_runs WHERE id = ? AND workspace_id = ?`,
		runID, workspaceID).Scan(&depth)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("pending_runs: chain depth of %q: %w", runID, err)
	}
	return int(depth.Int64), true, nil
}
