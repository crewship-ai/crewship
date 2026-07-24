package pipeline

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/crewship-ai/crewship/internal/tsformat"
)

// SignalWaitStore persists wait(event) step arm/delivery state
// (pipeline_signal_waits, migration v154) so a signal survives a
// process restart (#1409). The in-memory SignalRegistry alone loses
// any signal delivered while nothing is registered to receive it —
// because the process is down, or because the run simply hasn't
// reached the wait step yet. This store is the durable source of
// truth; SignalRegistry stays as the fast in-process wake-up path for
// the common case where the waiting goroutine is live.
type SignalWaitStore interface {
	// Arm records that (runID, stepID) is now waiting on eventType, in
	// workspaceID. Idempotent: re-arming the same (run, step) — e.g. a
	// resume that re-executes the wait step before the original arm's
	// transaction is even visible — is a no-op, not an error.
	Arm(ctx context.Context, workspaceID, runID, stepID, eventType string) error

	// Deliver persists payload against the oldest PENDING wait matching
	// (runID, eventType) and marks it delivered. Returns armed=false
	// when no pending row matches — the caller's signal (wrong
	// event_type, run never armed one, or it was already delivered)
	// should be treated as "no run waiting on that event", same
	// contract the in-memory registry offered.
	Deliver(ctx context.Context, runID, eventType, payload string) (armed bool, err error)

	// ConsumeDelivered returns the payload for a delivered-but-not-yet-
	// consumed wait at (runID, stepID) and marks it consumed in the same
	// call (so two concurrent consumers — a live goroutine racing a
	// resume — can't both claim it). ok=false means nothing has been
	// delivered yet; the caller should fall back to blocking/registering
	// for a live signal.
	ConsumeDelivered(ctx context.Context, runID, stepID string) (payload string, ok bool, err error)
}

// SQLSignalWaitStore is the production SignalWaitStore backed by
// pipeline_signal_waits.
type SQLSignalWaitStore struct {
	db *sql.DB
}

// NewSQLSignalWaitStore returns a store backed by the given DB handle.
// The handle must already be migrated to v154+.
func NewSQLSignalWaitStore(db *sql.DB) *SQLSignalWaitStore {
	return &SQLSignalWaitStore{db: db}
}

func (s *SQLSignalWaitStore) Arm(ctx context.Context, workspaceID, runID, stepID, eventType string) error {
	now := tsformat.Format(time.Now().UTC()) // fixed-width so created_at ORDER BY sorts correctly (#990)
	id := "sigwait_" + runID + "_" + stepID
	_, err := s.db.ExecContext(ctx, `
INSERT INTO pipeline_signal_waits (id, workspace_id, run_id, step_id, event_type, status, created_at)
VALUES (?, ?, ?, ?, ?, 'pending', ?)
ON CONFLICT (run_id, step_id) DO NOTHING`,
		id, workspaceID, runID, stepID, eventType, now,
	)
	if err != nil {
		return fmt.Errorf("signal_waits: arm: %w", err)
	}
	return nil
}

func (s *SQLSignalWaitStore) Deliver(ctx context.Context, runID, eventType, payload string) (bool, error) {
	now := tsformat.Format(time.Now().UTC()) // fixed-width for consistency with created_at (#990)
	res, err := s.db.ExecContext(ctx, `
UPDATE pipeline_signal_waits
SET status = 'delivered', payload = ?, delivered_at = ?
WHERE id = (
    SELECT id FROM pipeline_signal_waits
    WHERE run_id = ? AND event_type = ? AND status = 'pending'
    ORDER BY created_at ASC LIMIT 1
)`,
		payload, now, runID, eventType,
	)
	if err != nil {
		return false, fmt.Errorf("signal_waits: deliver: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("signal_waits: deliver rows: %w", err)
	}
	return n > 0, nil
}

func (s *SQLSignalWaitStore) ConsumeDelivered(ctx context.Context, runID, stepID string) (string, bool, error) {
	var payload sql.NullString
	err := s.db.QueryRowContext(ctx, `
SELECT payload FROM pipeline_signal_waits
WHERE run_id = ? AND step_id = ? AND status = 'delivered'`,
		runID, stepID,
	).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("signal_waits: consume read: %w", err)
	}
	now := tsformat.Format(time.Now().UTC()) // fixed-width for consistency with created_at (#990)
	res, err := s.db.ExecContext(ctx, `
UPDATE pipeline_signal_waits SET status = 'consumed', consumed_at = ?
WHERE run_id = ? AND step_id = ? AND status = 'delivered'`,
		now, runID, stepID,
	)
	if err != nil {
		return "", false, fmt.Errorf("signal_waits: consume update: %w", err)
	}
	// A concurrent consumer could have claimed it between our read and
	// this update (live goroutine racing a resume) — in that race only
	// one caller sees RowsAffected==1 and a non-empty payload; the loser
	// reports ok=false rather than double-delivering the same payload.
	n, _ := res.RowsAffected()
	if n == 0 {
		return "", false, nil
	}
	return payload.String, true, nil
}
