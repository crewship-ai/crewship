package api

// Per-crew admission queue for assignment dispatch. Lives in
// assignments_queue.go so it composes with the existing
// AssignmentHandler in assignments.go and assignments_run.go without a
// package split. The design is captured in
// .claude/context/prd/QUEUE-MECHANISM-2026.md — read that first if
// you're touching this code.
//
// Two primitives:
//
//   - claimCrewSlot(ctx, db, assignmentID, crewID, budget) — atomic
//     CAS. Tries to flip a PENDING/QUEUED row to RUNNING iff the
//     crew's current RUNNING count is under budget. The WHERE
//     subquery + UPDATE evaluate under the same SQLite write lock so
//     two callers cannot both win.
//
//   - pumpCrewQueue(ctx, db, crewID, budget) — called from the
//     completion path. Claims the oldest QUEUED row for this crew via
//     the same CAS pattern. Returns the claimed assignment ID(s) for
//     the caller to dispatch; returns empty when budget is full or
//     no QUEUED rows remain.
//
// Neither primitive does the actual agent spawn — they only own the
// status transition. The caller (DispatchAssignment / completion
// hook) is responsible for invoking runAssignment when claimed=true.
// Keeps the database concerns out of the goroutine-spawning concerns.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// defaultAgentMemoryEstimateMB is the per-agent memory cost we assume
// when deriving a budget from container_memory_mb. Claude / Gemini
// CLIs warm up to 1.5-2 GiB once they've loaded their model token
// caches; rounding to 2 GiB gives a budget that holds up under real
// workload without forcing operators to think about it. Override per
// crew via crews.max_concurrent_agents.
//
// Lives as a package-level const (not a config knob yet) because
// every dispatcher today wants the same number. When per-agent
// observed RSS diverges enough to matter, promote this to a config
// field and read from cfg.Container.AgentMemoryEstimateMB.
const defaultAgentMemoryEstimateMB = 2048

// computeCrewBudget returns the maximum concurrent agent runs for a
// crew. The order of precedence:
//
//  1. crews.max_concurrent_agents if non-NULL — operator override.
//  2. floor(crews.container_memory_mb / defaultAgentMemoryEstimateMB)
//     — derived from the container's memory ceiling. Minimum of 1
//     so a misconfigured tiny container is still dispatchable (a
//     budget of 0 would deadlock the queue).
//
// Returns 1 (not an error) if the crew row is missing or scan fails;
// the caller can still attempt dispatch and SQLite will surface the
// real failure with a clearer error than "budget=0".
func computeCrewBudget(ctx context.Context, db *sql.DB, crewID string) (int, error) {
	var override sql.NullInt64
	var memMB sql.NullInt64
	err := db.QueryRowContext(ctx, `
		SELECT max_concurrent_agents, container_memory_mb
		FROM crews
		WHERE id = ?`, crewID).Scan(&override, &memMB)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 1, nil
		}
		return 1, fmt.Errorf("load crew budget: %w", err)
	}
	if override.Valid {
		// Schema CHECK guarantees > 0; trust it.
		return int(override.Int64), nil
	}
	if memMB.Valid && memMB.Int64 > 0 {
		budget := int(memMB.Int64) / defaultAgentMemoryEstimateMB
		if budget < 1 {
			return 1, nil
		}
		return budget, nil
	}
	return 1, nil
}

// claimCrewSlot performs the atomic "is there a slot free, and if so
// claim it" CAS for one assignment. Returns claimed=true iff the
// caller now owns a RUNNING slot and is expected to dispatch the
// agent. claimed=false means the budget was full — caller should
// transition the row to QUEUED.
//
// The single UPDATE is the entire atomicity story. SQLite evaluates
// the COUNT subquery + the UPDATE under one write transaction; two
// callers racing on the same crew will serialise, the second one's
// COUNT subquery will see the first's transition, and only one will
// hit RowsAffected=1.
//
// status IN ('PENDING','QUEUED') is the predicate because both the
// initial-dispatch path and the pump path need this CAS — a pump
// must flip a QUEUED row to RUNNING when a slot frees, and the
// initial dispatch flips a PENDING row directly. Terminal-status
// rows (COMPLETED / FAILED / CANCELLED) are excluded so a re-
// dispatch attempt on a finished assignment is a no-op.
func claimCrewSlot(ctx context.Context, db *sql.DB, assignmentID, crewID string, budget int) (bool, error) {
	if budget < 1 {
		// Defence in depth: even if the CHECK constraint somehow
		// admitted a 0, the CAS below would always fail and queue
		// every dispatch forever. Treat as "no slot" so the caller
		// queues but logs the misconfig.
		return false, nil
	}
	// Constrain the UPDATE to the (assignment, crew) pair via the
	// EXISTS subquery on agents. The previous version trusted the
	// caller to pass matching ids — if a caller ever passes
	// assignmentID owned by crew B but crewID=A, the inflight
	// subquery would count A's RUNNING and the row in crew B would
	// flip to RUNNING based on A's budget. Tenant-isolation regression
	// guard from CodeRabbit on PR #395. Bind crewID twice: once for
	// the EXISTS constraint on the target row, once for the inflight
	// count.
	res, err := db.ExecContext(ctx, `
		UPDATE assignments
		   SET status = 'RUNNING',
		       running_at = datetime('now','subsec'),
		       started_at = COALESCE(started_at, datetime('now','subsec'))
		 WHERE id = ?
		   AND status IN ('PENDING', 'QUEUED')
		   AND EXISTS (
		     SELECT 1 FROM agents ag_target
		      WHERE ag_target.id = assignments.assigned_to_id
		        AND ag_target.crew_id = ?
		   )
		   AND ? > (
		     SELECT COUNT(*) FROM assignments inflight
		       JOIN agents ag ON ag.id = inflight.assigned_to_id
		      WHERE inflight.status = 'RUNNING'
		        AND ag.crew_id = ?
		   )`,
		assignmentID, crewID, budget, crewID,
	)
	if err != nil {
		return false, fmt.Errorf("claim crew slot: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("claim crew slot rows affected: %w", err)
	}
	return n == 1, nil
}

// markAssignmentQueued stamps a PENDING assignment as QUEUED. Used by
// the dispatch path when claimCrewSlot returned false. queued_at is
// also set so the pump's ORDER BY is meaningful.
//
// WHERE status='PENDING' is the guard: a row that's already QUEUED
// (perhaps the pump tried to claim it but the budget filled between
// CAS attempts) must not have its queued_at re-stamped. The pump
// relies on queued_at being monotonic-per-row to provide FIFO
// fairness.
func markAssignmentQueued(ctx context.Context, db *sql.DB, assignmentID string) error {
	_, err := db.ExecContext(ctx, `
		UPDATE assignments
		   SET status = 'QUEUED',
		       queued_at = datetime('now','subsec')
		 WHERE id = ? AND status = 'PENDING'`, assignmentID)
	if err != nil {
		return fmt.Errorf("mark assignment queued: %w", err)
	}
	return nil
}

// pumpCrewQueue claims the oldest QUEUED assignment for the crew via
// the same CAS pattern as claimCrewSlot. Returns the claimed IDs in
// the order they were taken; an empty slice means "queue empty OR
// budget full" — both are healthy steady states.
//
// Loops until either the CAS fails (budget full or no QUEUED rows
// for this crew) or maxClaim is reached. maxClaim is a runaway
// guard: a buggy completion loop could otherwise call pumpCrewQueue
// indefinitely on a crew with 10000 queued rows. In practice budget
// is the natural bound (5-10 for normal hosts) so the loop runs few
// iterations; the explicit cap is defensive.
func pumpCrewQueue(ctx context.Context, db *sql.DB, crewID string, budget int) ([]string, error) {
	if budget < 1 {
		return nil, nil
	}
	const maxClaim = 64
	claimed := make([]string, 0, 4)
	for i := 0; i < maxClaim; i++ {
		// Single statement: pick the oldest QUEUED row for this
		// crew, flip to RUNNING, return the id — but only if the
		// budget isn't already saturated. RETURNING is SQLite 3.35+
		// (we already use it elsewhere in the codebase).
		row := db.QueryRowContext(ctx, `
			UPDATE assignments
			   SET status = 'RUNNING',
			       running_at = datetime('now','subsec'),
			       started_at = COALESCE(started_at, datetime('now','subsec'))
			 WHERE id = (
			   SELECT a.id FROM assignments a
			     JOIN agents ag ON ag.id = a.assigned_to_id
			    WHERE a.status = 'QUEUED' AND ag.crew_id = ?
			    ORDER BY a.queued_at ASC
			    LIMIT 1
			 )
			   AND ? > (
			   SELECT COUNT(*) FROM assignments inflight
			     JOIN agents ag2 ON ag2.id = inflight.assigned_to_id
			    WHERE inflight.status = 'RUNNING' AND ag2.crew_id = ?
			 )
			 RETURNING id`,
			crewID, budget, crewID,
		)
		var id string
		if err := row.Scan(&id); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				// No QUEUED row OR budget saturated — both terminate
				// the pump cleanly. The two cases are operationally
				// distinct but semantically identical for the pump:
				// nothing more to do this turn.
				return claimed, nil
			}
			return claimed, fmt.Errorf("pump crew queue: %w", err)
		}
		claimed = append(claimed, id)
	}
	return claimed, nil
}

// queueDepth counts QUEUED assignments for a crew. Used for the
// "X ahead of you" hint that the journal emits when a dispatch is
// queued. Read-only; no contention concerns.
func queueDepth(ctx context.Context, db *sql.DB, crewID string) (int, error) {
	var n int
	err := db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM assignments a
		  JOIN agents ag ON ag.id = a.assigned_to_id
		 WHERE a.status = 'QUEUED' AND ag.crew_id = ?`,
		crewID,
	).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("queue depth: %w", err)
	}
	return n, nil
}
