package work

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/crewship-ai/crewship/internal/tsformat"
)

// TransitionRequest moves a work item. RunID and Generation are the fencing
// terms: a worker whose lease was taken over cannot report a result for the
// attempt that replaced it (I5).
type TransitionRequest struct {
	WorkID     string
	RunID      string
	Generation int64
	To         State
	Reason     string
	// ExitEvidence is recorded on the attempt: an exit code, a signal, the
	// provider's own status. Never credentials.
	ExitEvidence string
	CostUSD      float64
}

// Transition applies a fenced state change and its event in one transaction.
//
// It refuses three things, each with its own error, because the caller must
// react differently to each: a stale generation is not retryable (someone else
// owns the work), a terminal item is not an error to report to the user as a
// failure (the API answers with the real finished state), and an illegal edge
// is a bug in the caller.
func (s *Store) Transition(ctx context.Context, req TransitionRequest) error {
	if !req.To.valid() {
		return fmt.Errorf("%w: unknown state %q", ErrIllegalTransition, req.To)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("work: begin transition: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	it, err := getItemTx(ctx, tx, req.WorkID)
	if err != nil {
		return err
	}
	if it.State.Terminal() {
		return fmt.Errorf("%w: %s is %s", ErrTerminal, it.ID, it.State)
	}
	if req.Generation != 0 && req.Generation != it.Generation {
		return fmt.Errorf("%w: attempt generation %d, live generation %d", ErrStaleGeneration, req.Generation, it.Generation)
	}
	if !CanTransition(it.State, req.To) {
		return fmt.Errorf("%w: %s -> %s", ErrIllegalTransition, it.State, req.To)
	}

	now := s.now().UTC()
	to := req.To
	reason := req.Reason

	// A retry that has no attempts left is a failure, not a retry. Deciding it
	// here rather than at claim time keeps the reason honest in the history.
	if to == StateRetryWait && it.Attempts >= MaxAttempts {
		to = StateFailed
		reason = fmt.Sprintf("%s (no attempts left after %d)", req.Reason, it.Attempts)
	}

	if to == StateRetryWait {
		eligible := now.Add(Backoff(it.Attempts, s.rnd))
		if _, err := tx.ExecContext(ctx,
			`UPDATE work_items SET eligible_at = ? WHERE id = ?`,
			tsformat.Format(eligible), it.ID); err != nil {
			return fmt.Errorf("work: schedule retry: %w", err)
		}
	}

	if err := s.setStateTx(ctx, tx, it, to, req.RunID, it.Generation, reason, now); err != nil {
		return err
	}

	// Close the attempt whenever the work stops occupying a slot. `waiting`
	// deliberately does not close it: the runtime is parked, not finished, and
	// §4 requires the slot to be released only once that parking is confirmed —
	// which the caller does with ReleaseSlot.
	if req.RunID != "" && (to.Terminal() || to == StateRetryWait || to == StateQueued || to == StateNeedsReconciliation) {
		if _, err := tx.ExecContext(ctx, `
			UPDATE work_attempts
			SET ended_at = ?, end_reason = ?, exit_evidence = ?, cost_usd = ?
			WHERE run_id = ? AND ended_at IS NULL`,
			tsformat.Format(now), reason, req.ExitEvidence, req.CostUSD, req.RunID); err != nil {
			return fmt.Errorf("work: close attempt: %w", err)
		}
	}

	return tx.Commit()
}

// StartRunning records that the runtime is confirmed live, together with where
// it is. The locator is what recovery consults before deciding a process is
// gone: §4 forbids treating an expired lease alone as permission to start a
// second one.
func (s *Store) StartRunning(ctx context.Context, workID, runID string, generation int64, locator string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("work: begin start-running: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	it, err := getItemTx(ctx, tx, workID)
	if err != nil {
		return err
	}
	if generation != it.Generation {
		return fmt.Errorf("%w: attempt generation %d, live generation %d", ErrStaleGeneration, generation, it.Generation)
	}
	if !CanTransition(it.State, StateRunning) {
		return fmt.Errorf("%w: %s -> %s", ErrIllegalTransition, it.State, StateRunning)
	}
	now := s.now().UTC()
	if _, err := tx.ExecContext(ctx,
		`UPDATE work_attempts SET runtime_locator = ? WHERE run_id = ? AND generation = ?`,
		locator, runID, generation); err != nil {
		return fmt.Errorf("work: record locator: %w", err)
	}
	if err := s.setStateTx(ctx, tx, it, StateRunning, runID, generation, "runtime confirmed", now); err != nil {
		return err
	}
	return tx.Commit()
}

// Heartbeat extends the lease of a live attempt. It is fenced: an attempt that
// was superseded gets ErrStaleGeneration and must stop, not keep renewing a
// lease it no longer holds.
func (s *Store) Heartbeat(ctx context.Context, runID string, generation int64) error {
	now := s.now().UTC()
	res, err := s.db.ExecContext(ctx, `
		UPDATE work_attempts
		SET heartbeat_at = ?, lease_expires_at = ?
		WHERE run_id = ? AND generation = ? AND ended_at IS NULL
		  AND EXISTS (SELECT 1 FROM work_items w WHERE w.id = work_attempts.work_id AND w.generation = ?)`,
		tsformat.Format(now), tsformat.Format(now.Add(LeaseDuration)), runID, generation, generation)
	if err != nil {
		return fmt.Errorf("work: heartbeat: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("%w: run %s generation %d no longer holds the lease", ErrStaleGeneration, runID, generation)
	}
	return nil
}

// RecoveryOutcome is what one recovery pass did.
type RecoveryOutcome struct {
	Requeued       []string // work ids returned to the queue
	Reconciliation []string // work ids parked for reconciliation
}

// RecoverExpiredLeases handles attempts whose lease ran out.
//
// The decision rule is §4's, and it is deliberately conservative. An expired
// lease is evidence that a worker stopped reporting — not that its runtime
// stopped running. So:
//
//   - No runtime locator was ever recorded: the attempt died before it started
//     anything outside the database. Safe to return to the queue.
//   - A locator exists: something may still be executing under it. The work goes
//     to needs_reconciliation and keeps its conflicting capacity until someone
//     verifies the locator, stops or adopts that runtime, and revokes the old
//     capability. Starting a second process here is exactly the "two live
//     runtimes for one work item" that T08 exists to catch.
//
// Fencing in the database does not stop an arbitrary shell inside a shared
// container. That limit is E0's, and it is stated rather than fixed here.
func (s *Store) RecoverExpiredLeases(ctx context.Context) (RecoveryOutcome, error) {
	var out RecoveryOutcome

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return out, fmt.Errorf("work: begin recovery: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	now := s.now().UTC()
	rows, err := tx.QueryContext(ctx, `
		SELECT a.run_id, a.work_id, a.generation, a.runtime_locator
		FROM work_attempts a
		JOIN work_items w ON w.id = a.work_id
		WHERE a.ended_at IS NULL
		  AND a.lease_expires_at < ?
		  AND w.state IN ('starting','running')
		  AND w.generation = a.generation`,
		tsformat.Format(now))
	if err != nil {
		return out, fmt.Errorf("work: scan expired leases: %w", err)
	}
	type expired struct {
		runID, workID, locator string
		generation             int64
	}
	var list []expired
	for rows.Next() {
		var e expired
		if err := rows.Scan(&e.runID, &e.workID, &e.generation, &e.locator); err != nil {
			rows.Close()
			return out, err
		}
		list = append(list, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return out, err
	}

	for _, e := range list {
		it, err := getItemTx(ctx, tx, e.workID)
		if err != nil {
			return out, err
		}
		to, reason := StateQueued, "lease expired before any runtime started"
		if e.locator != "" {
			to, reason = StateNeedsReconciliation, "lease expired with a runtime locator recorded: "+e.locator
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE work_attempts SET ended_at = ?, end_reason = ? WHERE run_id = ? AND ended_at IS NULL`,
			tsformat.Format(now), reason, e.runID); err != nil {
			return out, fmt.Errorf("work: close expired attempt: %w", err)
		}
		if err := s.setStateTx(ctx, tx, it, to, e.runID, e.generation, reason, now); err != nil {
			return out, err
		}
		if to == StateQueued {
			out.Requeued = append(out.Requeued, e.workID)
		} else {
			out.Reconciliation = append(out.Reconciliation, e.workID)
		}
	}

	if err := tx.Commit(); err != nil {
		return RecoveryOutcome{}, fmt.Errorf("work: commit recovery: %w", err)
	}
	return out, nil
}

func getItemTx(ctx context.Context, tx *sql.Tx, workID string) (*Item, error) {
	row := tx.QueryRowContext(ctx, `SELECT `+itemColumns+` FROM work_items WHERE id = ?`, workID)
	it, err := scanItem(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: work %s", ErrNotFound, workID)
	}
	return it, err
}

// Event is one row of a work item's history.
type Event struct {
	Seq        int64
	At         time.Time
	FromState  State
	ToState    State
	RunID      string
	Generation int64
	Reason     string
}

// History returns a work item's events in sequence order.
func (s *Store) History(ctx context.Context, workID string) ([]Event, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT seq, at, from_state, to_state, run_id, generation, reason
		 FROM work_events WHERE work_id = ? ORDER BY seq`, workID)
	if err != nil {
		return nil, fmt.Errorf("work: read history: %w", err)
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var e Event
		var at, from, to string
		if err := rows.Scan(&e.Seq, &at, &from, &to, &e.RunID, &e.Generation, &e.Reason); err != nil {
			return nil, err
		}
		e.At, e.FromState, e.ToState = parseTime(at), State(from), State(to)
		out = append(out, e)
	}
	return out, rows.Err()
}
