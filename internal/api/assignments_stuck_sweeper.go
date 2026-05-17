package api

// Stuck-QUEUED sweeper — Phase 2 of the queue mechanism described in
// .claude/context/prd/QUEUE-MECHANISM-2026.md. The completion-path
// pump (Phase 1B) drains the queue under normal operation, but
// crashes between "set status QUEUED" and "next completion fires"
// can leave rows queued forever. The sweeper catches these.
//
// Conditions a QUEUED row can be stuck in:
//
//   1. crewshipd crashed after markAssignmentQueued but before any
//      inflight run completed. The pump never fired; the queue
//      survives the restart but nobody wakes it.
//
//   2. A crew's only RUNNING assignment was force-killed (Docker OOM
//      from outside this code, host reboot, ...) and never wrote a
//      terminal status. The crew has free budget but no completion
//      event will fire a pump.
//
//   3. A pump attempt itself crashed between claimCrewSlot setting
//      RUNNING and dispatchByID actually invoking runAssignment.
//      That row is now RUNNING-but-not-actually-running and blocks
//      a slot until the timeoutSecs sweeper catches it. (We don't
//      handle case 3 directly — RUNNING-but-stuck is the assignment
//      timeout's job; the harbormaster gate has the same pattern at
//      gate.go:238.)
//
// The sweeper handles cases 1 and 2 by periodically scanning for
// QUEUED rows whose queued_at is older than `staleAfter`, finding
// the distinct crew_ids they belong to, and calling pumpAndDispatch
// on each. pumpAndDispatch is idempotent — it claims iff budget
// permits, so a sweeper run that races a healthy pump does no harm.
//
// Pattern mirrors internal/harbormaster/gate.go:238 (StartTimeoutSweeper).

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
)

// defaultSweeperInterval is the gap between sweeper ticks when the
// caller doesn't supply one. 5 minutes balances "catch a crash
// within a reasonable window" against "don't hammer the DB on idle
// systems". The harbormaster timeout sweeper uses 30s; we run less
// often because stuck QUEUED is a much rarer condition than stuck
// pending-approval (approvals time out by design; queue rows stick
// only on real failures).
const defaultSweeperInterval = 5 * time.Minute

// defaultStaleAfter is how long a QUEUED row has to sit before the
// sweeper considers it stuck. 1 minute is long enough that the
// normal pump path always pre-empts (which runs on every completion,
// typically every 10-30s under load) and short enough that an
// operator-visible delay after a crash is < 6 minutes (1 min stale
// + 5 min sweeper interval).
const defaultStaleAfter = 1 * time.Minute

// SweepStuckQueued scans for QUEUED assignments older than
// staleAfter, groups them by crew, and calls pumpAndDispatch on each
// distinct crew. Returns the total number of assignments the sweeper
// successfully pumped. Errors from individual crew pumps are logged
// but do not abort the whole sweep — one bad crew must not block
// the others from draining.
//
// staleAfter <= 0 falls back to defaultStaleAfter.
func (h *AssignmentHandler) SweepStuckQueued(ctx context.Context, staleAfter time.Duration) (int, error) {
	if staleAfter <= 0 {
		staleAfter = defaultStaleAfter
	}
	// Compute cutoff in Go and pass as a parameter. queued_at is
	// written by the dispatcher via SQLite's datetime('now','subsec')
	// which produces 'YYYY-MM-DD HH:MM:SS.SSS' (NOT RFC3339).
	// time.Now().UTC().Format(...) with the matching layout produces
	// the same shape, so lexicographic < works on both. Computing
	// the cutoff Go-side avoids the trap that SQLite's 'subsec'
	// modifier requires 3.42.0+ — older builds would silently drop
	// subseconds from the cutoff side only, breaking the comparison
	// for any row whose queued_at has subseconds.
	cutoff := time.Now().UTC().Add(-staleAfter).Format("2006-01-02 15:04:05.000")

	// Distinct crews with stale QUEUED rows. The JOIN to agents
	// resolves assigned_to_id → crew_id; the GROUP BY collapses
	// multiple stale rows in the same crew into one pump call.
	rows, err := h.db.QueryContext(ctx, `
		SELECT DISTINCT ag.crew_id
		  FROM assignments a
		  JOIN agents ag ON ag.id = a.assigned_to_id
		 WHERE a.status = 'QUEUED'
		   AND a.queued_at < ?
		   AND ag.crew_id IS NOT NULL`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("sweepStuckQueued: scan stale crews: %w", err)
	}
	defer rows.Close()

	var crewIDs []string
	for rows.Next() {
		var crewID sql.NullString
		if scanErr := rows.Scan(&crewID); scanErr != nil {
			return 0, fmt.Errorf("sweepStuckQueued: scan crew: %w", scanErr)
		}
		if crewID.Valid && crewID.String != "" {
			crewIDs = append(crewIDs, crewID.String)
		}
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return 0, fmt.Errorf("sweepStuckQueued: iterate crews: %w", rowsErr)
	}
	if len(crewIDs) == 0 {
		return 0, nil
	}

	total := 0
	for _, crewID := range crewIDs {
		n, pumpErr := h.pumpAndDispatch(ctx, crewID)
		if pumpErr != nil {
			// Log + continue. The next sweep tick will retry. A
			// single bad crew (FK violation, db lock contention,
			// etc.) must not stop the rest from draining.
			h.logger.Warn("sweepStuckQueued: pump failed for crew",
				"crew_id", crewID,
				"error", pumpErr,
			)
			continue
		}
		total += n
	}

	if total > 0 {
		// Audit trail: operators tracking queue health (or post-
		// mortem-ing a crash) want to see when the sweeper kicked in
		// vs. when the normal pump path handled the drain. Severity
		// is notice because sweeper activity is informational on a
		// healthy system but a signal worth attention if it fires
		// often.
		if _, jerr := h.journal.Emit(ctx, journal.Entry{
			Type:      journal.EntryType("queue.sweeper_pumped"),
			Severity:  journal.SeverityNotice,
			ActorType: journal.ActorSystem,
			ActorID:   "queue_sweeper",
			Summary:   fmt.Sprintf("stuck-queued sweeper pumped %d assignment(s) across %d crew(s)", total, len(crewIDs)),
			Payload: map[string]any{
				"pumped_total": total,
				"crew_count":   len(crewIDs),
				"stale_after":  staleAfter.String(),
			},
		}); jerr != nil {
			// Journal failures don't block the sweep — the
			// assignments are already dispatched, which is the
			// outcome that matters. Log so the operator notices
			// journal-side breakage.
			h.logger.Warn("sweepStuckQueued: journal emit failed", "error", jerr)
		}
	}

	return total, nil
}

// StartStuckQueueSweeper runs SweepStuckQueued on a ticker until ctx
// is cancelled. Returns immediately; the goroutine exits on
// ctx.Done(). Intended to be wired up once at process start.
//
// interval <= 0 falls back to defaultSweeperInterval (5 min).
// staleAfter <= 0 falls back to defaultStaleAfter (1 min). See the
// constants for the rationale.
//
// The sweeper deliberately does NOT fire an immediate first tick
// (unlike some sweeper patterns). At startup the normal pump path
// hasn't had a chance to drain the queue yet; an immediate sweep
// would race the boot-time dispatcher. Waiting one full interval
// gives the system time to stabilise before the sweeper starts
// looking for stuck rows.
func (h *AssignmentHandler) StartStuckQueueSweeper(ctx context.Context, interval, staleAfter time.Duration) {
	if interval <= 0 {
		interval = defaultSweeperInterval
	}
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				n, err := h.SweepStuckQueued(ctx, staleAfter)
				if err != nil {
					h.logger.Warn("queue stuck sweeper: scan failed", "error", err)
					continue
				}
				if n > 0 {
					h.logger.Info("queue stuck sweeper: rescued stuck queued assignments", "pumped", n)
				}
			}
		}
	}()
}
