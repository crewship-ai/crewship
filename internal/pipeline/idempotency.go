package pipeline

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// DefaultIdempotencyTTL is the window during which a duplicate
// idempotency_key resolves to the original run. Picked at 24h to
// match Stripe's posted limit (most webhook redelivery happens
// within minutes, but daily cron-driven retries can stretch out;
// 24h is the comfortable upper bound).
const DefaultIdempotencyTTL = 24 * time.Hour

// IdempotencyStore is a thin DB wrapper that turns
// "I want to run this pipeline, here's my idempotency key" into
// "use this run id" — either freshly reserved or recovered from a
// prior request with the same key.
//
// The contract is atomic: two concurrent calls with the same
// (workspace_id, idempotency_key) cannot both come back with
// IsNew=true. SQLite's INSERT OR IGNORE semantics give us that
// guarantee for free as long as the PK is the composite of those
// two columns.
type IdempotencyStore struct {
	db *sql.DB
}

// NewIdempotencyStore wires a store against a DB at v81+.
func NewIdempotencyStore(db *sql.DB) *IdempotencyStore {
	return &IdempotencyStore{db: db}
}

// LookupOrReserve atomically resolves an idempotency key.
//
// On a fresh key: inserts the row pointing at runID, returns
// (runID, isNew=true). The caller proceeds to actually run the
// pipeline.
//
// On a duplicate key: returns the previously-reserved run id and
// isNew=false. The caller should NOT execute again — the original
// run is the authoritative result.
//
// Expired rows (expires_at <= now) are swept lazily before the
// insert, so a key reused after 24h is treated as a fresh request.
func (s *IdempotencyStore) LookupOrReserve(
	ctx context.Context,
	workspaceID, idempotencyKey, runID, pipelineID string,
	ttl time.Duration,
) (resolvedRunID string, isNew bool, err error) {
	if workspaceID == "" || idempotencyKey == "" || runID == "" {
		return "", false, errors.New("idempotency: workspace_id + idempotency_key + run_id required")
	}
	if ttl <= 0 {
		ttl = DefaultIdempotencyTTL
	}
	now := time.Now().UTC()
	expires := now.Add(ttl).Format(time.RFC3339Nano)

	// Lazy sweep — keeps the table small without a dedicated
	// background worker. The DELETE is bounded by the partial index
	// on expires_at so it's O(expired_rows), not a full scan.
	if _, sweepErr := s.db.ExecContext(ctx,
		`DELETE FROM pipeline_run_idempotency WHERE expires_at <= ?`,
		now.Format(time.RFC3339Nano),
	); sweepErr != nil {
		// Sweep failure is non-fatal — we still want to attempt the
		// reservation. A persistent sweep error will accumulate dead
		// rows but won't break correctness.
		_ = sweepErr
	}

	res, err := s.db.ExecContext(ctx, `
INSERT OR IGNORE INTO pipeline_run_idempotency
  (workspace_id, idempotency_key, run_id, pipeline_id, expires_at)
VALUES (?, ?, ?, ?, ?)`,
		workspaceID, idempotencyKey, runID, pipelineID, expires,
	)
	if err != nil {
		return "", false, fmt.Errorf("idempotency: insert: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 1 {
		return runID, true, nil
	}

	// Conflict — read the existing row.
	var existing string
	err = s.db.QueryRowContext(ctx, `
SELECT run_id FROM pipeline_run_idempotency
WHERE workspace_id = ? AND idempotency_key = ?`,
		workspaceID, idempotencyKey,
	).Scan(&existing)
	if err != nil {
		// Row went away between INSERT OR IGNORE and SELECT (sweep
		// raced us). Best caller experience is to retry once; we
		// surface the error so the caller can decide.
		return "", false, fmt.Errorf("idempotency: read after conflict: %w", err)
	}
	return existing, false, nil
}

// Forget removes an idempotency reservation. Called when a run
// failed early enough that a retry with the same key should be
// treated as a fresh request (e.g. concurrency-limit reject before
// any side effects). Without this, a 429 would poison the key for
// 24h and the caller couldn't legitimately retry.
//
// No-op if the key is already gone.
func (s *IdempotencyStore) Forget(ctx context.Context, workspaceID, idempotencyKey string) error {
	if workspaceID == "" || idempotencyKey == "" {
		return nil
	}
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM pipeline_run_idempotency WHERE workspace_id = ? AND idempotency_key = ?`,
		workspaceID, idempotencyKey,
	)
	return err
}
