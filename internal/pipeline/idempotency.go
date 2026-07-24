package pipeline

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	mathrand "math/rand/v2"
	"time"
)

// idempotencySweepOneInN throttles the lazy sweep (see LookupOrReserve) to
// roughly 1 in N calls instead of every call. Every keyed run start
// previously took a DB write lock TWICE — once for this DELETE, once for
// the INSERT OR IGNORE reservation right after — on the run hot path. The
// sweep is a pure housekeeping cleanup: correctness never depends on it
// running (the duplicate-lookup SELECT below always re-checks expires_at >
// now regardless of whether a sweep just ran), so sampling it costs nothing
// but a slightly larger table between sweeps. See issue #1411.
const idempotencySweepOneInN = 20

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
// Expired rows (expires_at <= now) are swept lazily — a sampled bulk
// sweep for table hygiene, plus a per-key force-delete-and-retry when
// THIS call's own conflict check finds its key specifically expired —
// so a key reused after 24h is always treated as a fresh request in one
// call, regardless of when the bulk sweep last ran.
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
	expires := now.Add(ttl).Format(time.RFC3339Nano) // tsformat:allow: pipeline_run_idempotency is a self-contained 24h-TTL dedup cache — expires_at is written and compared only here in RFC3339Nano, never against a tsformat column; pre-existing format, converting would mix formats across the deploy TTL window

	// Lazy sweep — keeps the table small without a dedicated
	// background worker. The DELETE is bounded by the partial index
	// on expires_at so it's O(expired_rows), not a full scan.
	//
	// Sampled to ~1-in-N calls (idempotencySweepOneInN): this DELETE was
	// previously unconditional on every keyed run start, taking a second
	// write-lock acquisition on the run hot path for a cleanup that doesn't
	// affect correctness (the SELECT below always filters expires_at > now
	// itself). See issue #1411.
	if mathrand.IntN(idempotencySweepOneInN) == 0 {
		if _, sweepErr := s.db.ExecContext(ctx,
			`DELETE FROM pipeline_run_idempotency WHERE expires_at <= ?`,
			now.Format(time.RFC3339Nano), // tsformat:allow: same-format comparison against expires_at (RFC3339Nano throughout this store)
		); sweepErr != nil {
			// Sweep failure is non-fatal — we still want to attempt the
			// reservation. A persistent sweep error will accumulate dead
			// rows but won't break correctness.
			_ = sweepErr
		}
	}

	nowStr := now.Format(time.RFC3339Nano) // tsformat:allow: created_at/expires_at inserts stay in this store's RFC3339Nano format for parity with the sweep comparison above

	// At most 2 attempts: the second only happens when the first hits a
	// conflict against a row THIS call discovers is expired and force-
	// deletes itself. That keeps "an expired key resolves as fresh" a
	// single-call guarantee independent of whether the sampled bulk sweep
	// above happened to run this time (#1411) — the bulk sweep is pure
	// housekeeping; this per-key self-heal is what correctness relies on.
	for attempt := 0; attempt < 2; attempt++ {
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

		// Conflict — read the existing row, but only if it is NOT
		// expired. Without the expires_at filter, a matched expired row
		// would silently return a dead run_id as if it were live, and the
		// caller (a webhook redelivery, say) would resolve to a zombie run.
		var existing string
		err = s.db.QueryRowContext(ctx, `
SELECT run_id FROM pipeline_run_idempotency
WHERE workspace_id = ? AND idempotency_key = ? AND expires_at > ?`,
			workspaceID, idempotencyKey, nowStr,
		).Scan(&existing)
		if err == nil {
			return existing, false, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return "", false, fmt.Errorf("idempotency: read after conflict: %w", err)
		}

		// The matching row exists but has expired and the bulk sweep
		// hasn't reached it yet. Force-delete this exact key and loop
		// once to retry the insert — a genuinely concurrent caller
		// reserving the SAME key in the gap makes the retry conflict
		// again, but against a live row this time, resolved by the
		// isNew=false read path above.
		if _, delErr := s.db.ExecContext(ctx, `
DELETE FROM pipeline_run_idempotency
WHERE workspace_id = ? AND idempotency_key = ? AND expires_at <= ?`,
			workspaceID, idempotencyKey, nowStr,
		); delErr != nil {
			return "", false, fmt.Errorf("idempotency: stale row force-delete: %w", delErr)
		}
	}
	// Reached only if two consecutive attempts both hit a freshly-expired
	// row for this exact key — pathologically unlikely (would need the row
	// to keep re-expiring between our own delete and our own insert).
	// Surface a clean error rather than looping unboundedly.
	return "", false, errStaleRowDeleted
}

// errStaleRowDeleted signals to the caller (via Reserve) that an
// expired row was present and has been force-deleted. The caller can
// retry Reserve once and expect to succeed. Sentinel error so the
// HTTP handler can map it to 409 with a "retry once" hint instead of
// crashing with a confused state.
var errStaleRowDeleted = errors.New("idempotency: stale row force-deleted; retry")

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
