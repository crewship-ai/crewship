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
// This is the WORKER path and it demands the full binding: work id, run id and
// generation, all three verified against each other inside the transaction.
// Anything less was not enough, and the review proved it twice.
//
// Generation alone is not identity. Two work items claimed once each both sit
// at generation 1, so run B could report a result for work A — and the follow-up
// UPDATE would then close B's own attempt, losing the live run as well as
// corrupting the finished one. The attempt must belong to THIS work item, be
// the current one, and still be open.
//
// There is deliberately no generation == 0 escape hatch here. It existed as a
// convenience for callers that did not have an attempt, and what it actually
// did was disable the fence for anyone who omitted a field. Callers without an
// attempt want RequestCancel or Resolve, which say what they are.
func (s *Store) Transition(ctx context.Context, req TransitionRequest) error {
	if !req.To.valid() {
		return fmt.Errorf("%w: unknown state %q", ErrIllegalTransition, req.To)
	}
	if req.RunID == "" || req.Generation == 0 {
		return fmt.Errorf("%w: a worker transition needs run id and generation; "+
			"use RequestCancel or Resolve for an operation that owns no attempt", ErrNotBound)
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
	if err := verifyAttemptBindingTx(ctx, tx, req.WorkID, req.RunID, req.Generation, it.Generation); err != nil {
		return err
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
	// §4 requires the slot to be released only once that parking is confirmed.
	if to.Terminal() || to == StateRetryWait || to == StateQueued || to == StateNeedsReconciliation {
		res, err := tx.ExecContext(ctx, `
			UPDATE work_attempts
			SET ended_at = ?, end_reason = ?, exit_evidence = ?, cost_usd = ?
			WHERE run_id = ? AND work_id = ? AND generation = ? AND ended_at IS NULL`,
			tsformat.Format(now), reason, req.ExitEvidence, req.CostUSD,
			req.RunID, req.WorkID, req.Generation)
		if err != nil {
			return fmt.Errorf("work: close attempt: %w", err)
		}
		// The binding was verified above, so zero rows here means something
		// changed under us inside the transaction — which cannot happen, and if
		// it ever does the state change must not commit alone.
		if n, _ := res.RowsAffected(); n != 1 {
			return fmt.Errorf("%w: closing attempt %s of %s affected %d rows",
				ErrNotBound, req.RunID, req.WorkID, n)
		}
	}

	return tx.Commit()
}

// verifyAttemptBindingTx is the whole of the fence, in one place so that every
// worker-facing operation gets the same answer.
//
// It proves four things at once: the attempt exists, it belongs to this work
// item, it is the CURRENT attempt (its generation matches both the caller's and
// the item's), and it has not already ended. A caller that satisfies all four
// is the live worker; anything else is a stale one, a confused one, or a bug.
func verifyAttemptBindingTx(ctx context.Context, tx *sql.Tx, workID, runID string, reqGeneration, itemGeneration int64) error {
	if reqGeneration != itemGeneration {
		return fmt.Errorf("%w: attempt generation %d, live generation %d",
			ErrStaleGeneration, reqGeneration, itemGeneration)
	}
	var attemptWorkID string
	var attemptGeneration int64
	var endedAt sql.NullString
	err := tx.QueryRowContext(ctx,
		`SELECT work_id, generation, ended_at FROM work_attempts WHERE run_id = ?`, runID,
	).Scan(&attemptWorkID, &attemptGeneration, &endedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: no attempt %s exists", ErrNotBound, runID)
	}
	if err != nil {
		return fmt.Errorf("work: read attempt: %w", err)
	}
	if attemptWorkID != workID {
		return fmt.Errorf("%w: attempt %s belongs to work %s, not %s",
			ErrNotBound, runID, attemptWorkID, workID)
	}
	if attemptGeneration != reqGeneration {
		return fmt.Errorf("%w: attempt %s is generation %d, caller claims %d",
			ErrStaleGeneration, runID, attemptGeneration, reqGeneration)
	}
	if endedAt.Valid {
		return fmt.Errorf("%w: attempt %s already ended at %s", ErrStaleGeneration, runID, endedAt.String)
	}
	return nil
}

// MarkStarting records, durably, that we are ABOUT to create a runtime — and
// where it will be — before the external start happens.
//
// This is review finding R4. The previous shape wrote the locator only once the
// runtime answered, so recovery read "no locator" as proof that no process had
// been created. A crash between the real start and that write broke the proof:
// the work went back on the queue while its process kept running, and the next
// claim started a second one alongside it.
//
// The locator must therefore be DETERMINISTIC — derivable from the run id
// before anything exists — so that recovery can go looking for a runtime by an
// identity it knew in advance, instead of inferring absence from silence.
func (s *Store) MarkStarting(ctx context.Context, workID, runID string, generation int64, plannedLocator string) error {
	if plannedLocator == "" {
		return fmt.Errorf("%w: a start intent without a locator tells recovery nothing", ErrNotBound)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("work: begin mark-starting: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	it, err := getItemTx(ctx, tx, workID)
	if err != nil {
		return err
	}
	if err := verifyAttemptBindingTx(ctx, tx, workID, runID, generation, it.Generation); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `
		UPDATE work_attempts
		SET runtime_locator = ?, runtime_phase = 'starting'
		WHERE run_id = ? AND work_id = ? AND generation = ? AND ended_at IS NULL
		  AND runtime_phase = 'planned'`,
		plannedLocator, runID, workID, generation)
	if err != nil {
		return fmt.Errorf("work: record start intent: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return fmt.Errorf("%w: attempt %s of %s is not a planned attempt awaiting a start",
			ErrNotBound, runID, workID)
	}
	return tx.Commit()
}

// StartRunning records that the runtime is confirmed live.
//
// It REQUIRES a prior MarkStarting. There is no planned -> confirmed edge,
// deliberately: an attempt that reaches `running` without ever having declared
// where its runtime would be is an attempt recovery cannot reason about, and
// offering the protocol without enforcing it means the one caller that skips it
// is the one that crashes in the window the protocol exists to cover.
//
// The locator argument updates the planned one, for the case where the
// confirmed identity differs from what was predicted. It cannot ESTABLISH one.
//
// The UPDATE is also checked. It used to ignore how many rows it touched, so a
// run id that existed nowhere still moved the work item to `running`, leaving
// work marked live with no attempt behind it — one of the three defects the
// review reproduced.
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
	if err := verifyAttemptBindingTx(ctx, tx, workID, runID, generation, it.Generation); err != nil {
		return err
	}
	if !CanTransition(it.State, StateRunning) {
		return fmt.Errorf("%w: %s -> %s", ErrIllegalTransition, it.State, StateRunning)
	}
	now := s.now().UTC()
	res, err := tx.ExecContext(ctx, `
		UPDATE work_attempts
		SET runtime_locator = CASE WHEN ? != '' THEN ? ELSE runtime_locator END,
		    runtime_phase = 'confirmed'
		WHERE run_id = ? AND work_id = ? AND generation = ? AND ended_at IS NULL
		  AND runtime_phase IN ('starting', 'confirmed')`,
		locator, locator, runID, workID, generation)
	if err != nil {
		return fmt.Errorf("work: record locator: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return fmt.Errorf("%w: attempt %s of %s has no start intent to confirm; "+
			"call MarkStarting with the locator BEFORE creating the runtime, or recovery "+
			"cannot tell a process that was never started from one we failed to record",
			ErrNotBound, runID, workID)
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

// runtimePhasePlanned is the only attempt phase that proves nothing external
// was attempted. See the migration that introduced the column.
const (
	runtimePhasePlanned   = "planned"
	runtimePhaseStarting  = "starting"
	runtimePhaseConfirmed = "confirmed"
)

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
//   - The attempt is still `planned`: it was claimed, capacity was reserved, and
//     nothing outside the database was ever attempted. Safe to return to the
//     queue, because there is no process to collide with.
//   - It reached `starting` or `confirmed`: an external start was REQUESTED, so
//     something may be executing under the locator we wrote down first. The work goes
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
		SELECT a.run_id, a.work_id, a.generation, a.runtime_locator, a.runtime_phase
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
		runID, workID, locator, phase string
		generation                    int64
	}
	var list []expired
	for rows.Next() {
		var e expired
		if err := rows.Scan(&e.runID, &e.workID, &e.generation, &e.locator, &e.phase); err != nil {
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
		// The PHASE decides, not the presence of a locator. Reading an empty
		// locator as "nothing started" is exactly the inference R4 broke: it
		// cannot tell "never started" from "started, and we died before writing
		// it down". Only `planned` licenses a blind requeue.
		to, reason := StateQueued, "lease expired while the attempt was still planned; no runtime was ever requested"
		if e.phase != runtimePhasePlanned {
			to = StateNeedsReconciliation
			reason = fmt.Sprintf("lease expired in runtime phase %q; a runtime may exist at %q and must be checked",
				e.phase, e.locator)
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

// CancelOutcome is what a cancel request actually achieved, which is not always
// what the caller asked for.
type CancelOutcome string

const (
	// CancelOutcomeCancelled means the work is confirmed stopped. It is only
	// ever returned for work that had not started: nothing was running, so
	// there was nothing to stop.
	CancelOutcomeCancelled CancelOutcome = "cancelled"
	// CancelOutcomeRequested means a live runtime was asked to stop and the
	// request is durable. The work is still running until a worker confirms
	// otherwise, and the caller must not describe it as stopped.
	CancelOutcomeRequested CancelOutcome = "requested"
	// CancelOutcomeAlreadyTerminal means the work finished on its own before
	// the cancel arrived. The real finished state comes back with it, because
	// telling someone "cancelled" about work that succeeded is a lie in the one
	// direction that matters.
	CancelOutcomeAlreadyTerminal CancelOutcome = "already_terminal"
)

// CancelResult reports what happened and the state the work is actually in.
type CancelResult struct {
	Outcome CancelOutcome
	State   State
	// RunID is the attempt the request was recorded against, empty when the
	// work had not started. A dispatcher signals THIS run, not the agent.
	RunID string
	// Generation the request was recorded against, so a worker that has since
	// been superseded cannot satisfy a cancel meant for its replacement.
	Generation int64
}

// RequestCancel is the whole of cancellation, in one transaction.
//
// Review finding R1. The previous shape read the work item's state over here,
// decided what to do, and then asked for `cancelled` over there with no
// precondition at all. A claim landing in the gap turned a live run's row
// terminal while its process carried on, and the API answered "Cancelled before
// it started; no runtime was involved" — a sentence that was false about both
// halves.
//
// So the read and the decision are now the same transaction, and what is
// decided is decided from what the row says HERE, not from a snapshot a handler
// took earlier. Queued work is cancelled outright, because there is genuinely
// nothing running. Live work gets a durable request recorded against the
// attempt that is current at this instant — and stays running until a worker
// confirms the stop, which is the only thing that can honestly produce
// `cancelled`.
//
// Cancel is idempotent: asking twice records the request once and answers the
// same way, because a user pressing a button twice is not a state change.
func (s *Store) RequestCancel(ctx context.Context, workID, requestedBy, reason string) (CancelResult, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return CancelResult{}, fmt.Errorf("work: begin cancel: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	it, err := getItemTx(ctx, tx, workID)
	if err != nil {
		return CancelResult{}, err
	}
	now := s.now().UTC()

	if it.State.Terminal() {
		if err := tx.Commit(); err != nil {
			return CancelResult{}, fmt.Errorf("work: commit cancel: %w", err)
		}
		return CancelResult{Outcome: CancelOutcomeAlreadyTerminal, State: it.State}, nil
	}

	// Not started: cancel it outright, conditioned on the state and generation
	// this transaction just read. The condition is what makes it atomic — a
	// claim that beat us here changes both, and then this UPDATE matches
	// nothing and we fall through to treating it as live, which it now is.
	if it.State == StateQueued || it.State == StateRetryWait {
		res, err := tx.ExecContext(ctx, `
			UPDATE work_items
			SET state = 'cancelled', state_reason = ?, updated_at = ?, terminal_at = ?
			WHERE id = ? AND state = ? AND generation = ?`,
			reason, tsformat.Format(now), tsformat.Format(now), it.ID, string(it.State), it.Generation)
		if err != nil {
			return CancelResult{}, fmt.Errorf("work: cancel queued work: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 1 {
			if err := appendEventTx(ctx, tx, it.ID, string(it.State), StateCancelled, "", it.Generation, reason, now); err != nil {
				return CancelResult{}, err
			}
			if err := tx.Commit(); err != nil {
				return CancelResult{}, fmt.Errorf("work: commit cancel: %w", err)
			}
			return CancelResult{Outcome: CancelOutcomeCancelled, State: StateCancelled}, nil
		}
		// Lost the race inside our own transaction, which under an immediate
		// transaction should be unreachable. Re-read rather than assume, so
		// that a future reader who relaxes the isolation finds this line.
		if it, err = getItemTx(ctx, tx, workID); err != nil {
			return CancelResult{}, err
		}
		if it.State.Terminal() {
			if err := tx.Commit(); err != nil {
				return CancelResult{}, fmt.Errorf("work: commit cancel: %w", err)
			}
			return CancelResult{Outcome: CancelOutcomeAlreadyTerminal, State: it.State}, nil
		}
	}

	// Live work. Record the request against the attempt that is current NOW,
	// read inside this transaction rather than carried in from a caller's
	// earlier snapshot — a request stamped with a superseded generation would
	// let a worker that has already been replaced satisfy a cancel meant for
	// its replacement.
	var runID string
	var generation int64
	var alreadyRequested sql.NullString
	err = tx.QueryRowContext(ctx, `
		SELECT run_id, generation, cancel_requested_at FROM work_attempts
		WHERE work_id = ? AND generation = ? AND ended_at IS NULL`,
		it.ID, it.Generation).Scan(&runID, &generation, &alreadyRequested)
	if errors.Is(err, sql.ErrNoRows) {
		// Live state with no open attempt: the ledger disagrees with itself,
		// and a cancel is not the place to paper over that. Park it for
		// reconciliation rather than reporting a stop nobody performed —
		// once, so repeated asks do not grow the history.
		if it.State == StateNeedsReconciliation {
			if err := tx.Commit(); err != nil {
				return CancelResult{}, fmt.Errorf("work: commit cancel: %w", err)
			}
			return CancelResult{Outcome: CancelOutcomeRequested, State: it.State}, nil
		}
		if err := s.setStateTx(ctx, tx, it, StateNeedsReconciliation, "", it.Generation,
			"cancel requested for "+string(it.State)+" work with no open attempt", now); err != nil {
			return CancelResult{}, err
		}
		if err := tx.Commit(); err != nil {
			return CancelResult{}, fmt.Errorf("work: commit cancel: %w", err)
		}
		return CancelResult{Outcome: CancelOutcomeRequested, State: StateNeedsReconciliation}, nil
	}
	if err != nil {
		return CancelResult{}, fmt.Errorf("work: read live attempt: %w", err)
	}

	// Already asked. Idempotent means the SECOND call changes nothing — not the
	// row, and not the history either: three presses of a button are one
	// decision, and three events would read as three.
	if alreadyRequested.Valid {
		if err := tx.Commit(); err != nil {
			return CancelResult{}, fmt.Errorf("work: commit cancel: %w", err)
		}
		return CancelResult{
			Outcome: CancelOutcomeRequested, State: it.State, RunID: runID, Generation: generation,
		}, nil
	}

	res, err := tx.ExecContext(ctx, `
		UPDATE work_attempts
		SET cancel_requested_at = COALESCE(cancel_requested_at, ?),
		    cancel_requested_by = CASE WHEN cancel_requested_at IS NULL THEN ? ELSE cancel_requested_by END,
		    cancel_reason = CASE WHEN cancel_requested_at IS NULL THEN ? ELSE cancel_reason END
		WHERE run_id = ? AND work_id = ? AND generation = ? AND ended_at IS NULL`,
		tsformat.Format(now), requestedBy, reason, runID, it.ID, generation)
	if err != nil {
		return CancelResult{}, fmt.Errorf("work: record cancel request: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return CancelResult{}, fmt.Errorf("%w: recording a cancel for attempt %s affected %d rows",
			ErrNotBound, runID, n)
	}

	// The request is part of the history, recorded as a same-state event so the
	// timeline shows when someone asked — without claiming the work changed.
	if err := appendEventTx(ctx, tx, it.ID, string(it.State), it.State, runID, generation,
		"cancel requested: "+reason, now); err != nil {
		return CancelResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return CancelResult{}, fmt.Errorf("work: commit cancel: %w", err)
	}
	return CancelResult{
		Outcome: CancelOutcomeRequested, State: it.State, RunID: runID, Generation: generation,
	}, nil
}

// CancelRequested reports whether a stop has been asked of this attempt, so a
// worker can notice mid-run and a dispatcher can chase one across a restart.
func (s *Store) CancelRequested(ctx context.Context, runID string) (bool, error) {
	var at sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT cancel_requested_at FROM work_attempts WHERE run_id = ?`, runID).Scan(&at)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrNotFound
	}
	if err != nil {
		return false, fmt.Errorf("work: read cancel request: %w", err)
	}
	return at.Valid, nil
}

// Resolve is the ADMIN path out of needs_reconciliation, and it exists because
// the worker path cannot serve it.
//
// A work item lands in needs_reconciliation precisely when its attempt is over
// and nobody knows what its runtime did. There is no live attempt to present,
// so every check Transition makes — the attempt is open, its generation is
// current — is unsatisfiable by construction. Letting the worker path accept a
// missing attempt "just for this case" would reopen the hole the binding closes,
// because that is the same permission a stale worker needs.
//
// So resolution is its own operation with its own preconditions: the caller is
// a human or a reconciler who has established what actually happened, and says
// so. It records who decided and why, because "someone marked this failed" is
// the only evidence anyone will have later.
func (s *Store) Resolve(ctx context.Context, workID string, to State, resolvedBy, reason string) error {
	if resolvedBy == "" || reason == "" {
		return fmt.Errorf("%w: resolving a reconciliation needs an actor and a reason; "+
			"it is a judgement someone made, and the record is the only evidence of it", ErrIllegalTransition)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("work: begin resolve: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	it, err := getItemTx(ctx, tx, workID)
	if err != nil {
		return err
	}
	if it.State != StateNeedsReconciliation {
		return fmt.Errorf("%w: Resolve is only for needs_reconciliation, %s is %s",
			ErrIllegalTransition, it.ID, it.State)
	}
	if !CanTransition(it.State, to) {
		return fmt.Errorf("%w: %s -> %s", ErrIllegalTransition, it.State, to)
	}
	now := s.now().UTC()
	if err := s.setStateTx(ctx, tx, it, to, "", it.Generation,
		fmt.Sprintf("resolved by %s: %s", resolvedBy, reason), now); err != nil {
		return err
	}
	return tx.Commit()
}

// Defer returns a claimed attempt to the queue WITHOUT spending it, eligible
// again at `until`.
//
// It exists for one answer an authorizer has to be able to give and could not:
// "not this agent, not yet". An agent staged PENDING_REVIEW is not refused and
// is not broken — it is held until an operator approves it, and the approval
// may be minutes or hours away. Failing such work would repeat a mistake this
// repository already documents at length in refuseHeldAgent: the first version
// of that gate returned an ordinary error, the mission engine recorded a
// terminally FAILED task, and the operator's approval arrived at something that
// had given up minutes earlier. Retrying it as an ordinary failure is no better
// — five attempts of capped backoff is about twenty minutes, and then the same
// dead end.
//
// Giving the attempt back is the load-bearing part, and it is safe only under a
// condition this method CHECKS rather than assumes: the attempt must still be
// in runtime phase `planned`, meaning no start intent was ever recorded and
// therefore nothing can possibly have been created. An attempt that got as far
// as `starting` may have a runtime somewhere, and handing its budget back would
// let it be retried forever beside a process nobody stopped.
//
// The generation is NOT rolled back, and the deferred attempt row is closed
// rather than deleted. Those are the two things that keep the fence pointing
// forwards: a straggler still holding the deferred run id is refused because
// its attempt has ended, and the next claim moves the generation on as usual.
// Rolling either of them back to make the retry look like a first try would be
// handing a superseded worker a way to report a result.
//
// What bounds a deferral loop is the item's own deadline_at, not an attempt
// count — which is the right instrument, because "how long may this wait for a
// human" is a question about wall-clock patience and not about retries. Work
// with no deadline waits indefinitely, on purpose.
func (s *Store) Defer(ctx context.Context, workID, runID string, generation int64, until time.Time, reason string) error {
	if reason == "" {
		return fmt.Errorf("%w: a deferral without a reason is indistinguishable from a stall", ErrNotBound)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("work: begin defer: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	it, err := getItemTx(ctx, tx, workID)
	if err != nil {
		return err
	}
	if it.State.Terminal() {
		return fmt.Errorf("%w: %s is %s", ErrTerminal, workID, it.State)
	}
	if err := verifyAttemptBindingTx(ctx, tx, workID, runID, generation, it.Generation); err != nil {
		return err
	}

	now := s.now().UTC()

	// A cancel may have arrived since the dispatcher last looked — it checks
	// right after the claim, and the authorizer runs after that. The request is
	// recorded on THIS attempt, and closing the attempt would take it to the
	// grave: the next claim opens a fresh row with nothing on it, and a user who
	// was told "requested" watches the work run once the hold clears.
	//
	// So the cancel is decided here, inside the transaction that would have
	// discarded it. Any check outside this transaction is a second race of the
	// same shape. And it is a confirmed cancellation, not a request: the phase
	// is `planned`, nothing was ever created, so there is nothing to ask to
	// stop. (Review P1 on the deferral work.)
	var cancelRequested sql.NullString
	var cancelReason sql.NullString
	if err := tx.QueryRowContext(ctx, `
		SELECT cancel_requested_at, cancel_reason FROM work_attempts
		WHERE run_id = ? AND work_id = ? AND generation = ? AND ended_at IS NULL
		  AND runtime_phase = 'planned'`,
		runID, workID, generation).Scan(&cancelRequested, &cancelReason); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// Either the attempt is gone, or it declared a start intent. The
			// second is the one that matters: a runtime may exist, so this
			// attempt is not free to give back.
			return fmt.Errorf("%w: attempt %s of %s is not a planned attempt; a deferral may only "+
				"return work that provably started nothing", ErrNotBound, runID, workID)
		}
		return fmt.Errorf("work: read deferred attempt: %w", err)
	}

	if cancelRequested.Valid {
		why := "cancelled while held: " + cancelReason.String
		if _, err := tx.ExecContext(ctx, `
			UPDATE work_attempts SET ended_at = ?, end_reason = ?
			WHERE run_id = ? AND work_id = ? AND generation = ? AND ended_at IS NULL`,
			tsformat.Format(now), why, runID, workID, generation); err != nil {
			return fmt.Errorf("work: close cancelled attempt: %w", err)
		}
		if err := s.setStateTx(ctx, tx, it, StateCancelled, runID, generation, why, now); err != nil {
			return err
		}
		return tx.Commit()
	}

	res, err := tx.ExecContext(ctx, `
		UPDATE work_attempts SET ended_at = ?, end_reason = ?
		WHERE run_id = ? AND work_id = ? AND generation = ? AND ended_at IS NULL
		  AND runtime_phase = 'planned'`,
		tsformat.Format(now), "deferred: "+reason, runID, workID, generation)
	if err != nil {
		return fmt.Errorf("work: close deferred attempt: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		// The row was there a moment ago inside this same immediate
		// transaction; anything else is a relaxed isolation somebody added
		// later, and this is the line that tells them.
		return fmt.Errorf("%w: attempt %s of %s changed under the deferral", ErrNotBound, runID, workID)
	}

	if _, err := tx.ExecContext(ctx, `
		UPDATE work_items
		SET state = 'queued', state_reason = ?, attempts = MAX(attempts - 1, 0),
		    eligible_at = ?, updated_at = ?
		WHERE id = ? AND generation = ?`,
		reason, tsformat.Format(until.UTC()), tsformat.Format(now), workID, generation); err != nil {
		return fmt.Errorf("work: requeue deferred work: %w", err)
	}
	if err := appendEventTx(ctx, tx, workID, string(it.State), StateQueued, runID, generation, reason, now); err != nil {
		return err
	}
	return tx.Commit()
}
