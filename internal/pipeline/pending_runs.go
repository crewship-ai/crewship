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
		var existingID string
		var maxAt sql.NullString
		err := s.db.QueryRowContext(ctx, `
SELECT id, COALESCE(debounce_max_at,'') FROM pending_runs
WHERE pipeline_id = ? AND debounce_key = ? AND status = 'pending'`,
			pr.PipelineID, pr.DebounceKey).Scan(&existingID, &maxAt)
		if err == nil {
			// Coalesce: push fire_at, but never past the original max.
			fireAt := pr.FireAt
			if maxAt.String != "" {
				if cap, perr := time.Parse(time.RFC3339Nano, maxAt.String); perr == nil && fireAt.After(cap) {
					fireAt = cap
				}
			}
			_, uerr := s.db.ExecContext(ctx, `
UPDATE pending_runs
SET inputs_json = ?, tags_json = ?, metadata_json = ?, tier_override = ?,
    priority = ?, fire_at = ?, expires_at = ?, updated_at = datetime('now','subsec')
WHERE id = ?`,
				orJSON(pr.InputsJSON, "{}"), orJSON(pr.TagsJSON, "[]"), orJSON(pr.MetadataJSON, "{}"),
				nullableStr(pr.TierOverride), pr.Priority, fireAt.UTC().Format(time.RFC3339Nano),
				nullableTime(pr.ExpiresAt), existingID)
			if uerr != nil {
				return "", false, fmt.Errorf("pending_runs: coalesce: %w", uerr)
			}
			return existingID, true, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return "", false, fmt.Errorf("pending_runs: debounce lookup: %w", err)
		}
	}

	_, err := s.db.ExecContext(ctx, `
INSERT INTO pending_runs (
    id, workspace_id, pipeline_id, pipeline_slug, inputs_json, tags_json, metadata_json,
    tier_override, priority, debounce_key, fire_at, expires_at, debounce_max_at,
    status, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'pending', datetime('now','subsec'), datetime('now','subsec'))`,
		pr.ID, pr.WorkspaceID, pr.PipelineID, pr.PipelineSlug,
		orJSON(pr.InputsJSON, "{}"), orJSON(pr.TagsJSON, "[]"), orJSON(pr.MetadataJSON, "{}"),
		nullableStr(pr.TierOverride), pr.Priority, nullableStr(pr.DebounceKey),
		pr.FireAt.UTC().Format(time.RFC3339Nano), nullableTime(pr.ExpiresAt), nullableTime(pr.DebounceMaxAt))
	if err != nil {
		return "", false, fmt.Errorf("pending_runs: insert: %w", err)
	}
	return pr.ID, false, nil
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
       COALESCE(tier_override,''), priority
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
			&pr.InputsJSON, &pr.TagsJSON, &pr.MetadataJSON, &pr.TierOverride, &pr.Priority); err != nil {
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
