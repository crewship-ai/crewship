package api

// Interrupted-RUNNING assignment recovery. Sibling of the stuck-QUEUED
// sweeper in assignments_stuck_sweeper.go; read that file's header for
// the queue-mechanism context.
//
// The gap this closes: assignments.status='RUNNING' has exactly one
// legitimate driver — the in-process runAssignment goroutine that will
// eventually call finishAssignment. That goroutine is never persisted,
// so a process crash orphans the row: recoverOrphanedRuns (server
// lifecycle) only rewrites journal_entries/agents, the stuck-QUEUED
// sweeper deliberately skips RUNNING rows, and no other code path ever
// transitions them. Because claimCrewSlot counts status='RUNNING'
// against the crew budget, each orphan permanently leaks a concurrency
// slot — every later delegation for that crew queues forever, and the
// lead's "[Assignment] @X is working on the task…" chat line never
// resolves.
//
// Two recovery paths, both funnelling into failInterruptedAssignment
// so a recovered row emits the SAME completion signals a normal
// failure emits (WS assignment_failed + assignment.updated, queue
// pump, mission callback, mission comment — all via finishAssignment):
//
//  1. RecoverInterruptedRunning — boot-time, wired into Server.Start
//     next to recoverOrphanedRuns. At boot every RUNNING row stamped
//     before process start is an orphan by construction (its driver
//     died with the previous process), so recovery is exact, not
//     heuristic.
//
//  2. SweepStuckRunning / StartStuckRunningSweeper — belt-and-braces
//     ticker for in-process leaks (e.g. a dispatch goroutine that
//     panicked between claimCrewSlot and runAssignment, the "case 3"
//     the stuck-QUEUED sweeper header explicitly punts on). Uses a
//     generous staleness bound so it can never race a healthy run.

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
)

// defaultRunningSweepInterval is the gap between stuck-RUNNING sweeper
// ticks. Same 5-minute grain as the stuck-QUEUED sweeper's default:
// the scan is one cheap indexed query, and a leaked slot surviving up
// to one extra interval is irrelevant next to the staleness bound
// below.
const defaultRunningSweepInterval = 5 * time.Minute

// defaultRunningStaleAfter is how long a row may sit RUNNING before
// the sweeper declares it leaked and fails it. This is deliberately
// generous — 2 hours — because unlike QUEUED (where any dwell beyond
// the pump cadence is pathological), RUNNING rows are backed by real
// agent executions whose duration is bounded only by the per-agent
// timeout_seconds plus provisioning overhead (image build, container
// boot). The bound must comfortably exceed the longest legitimate run
// an operator configures; if an installation runs agents with
// timeouts near or above 2h, this constant is the knob to raise
// (promote to config if that ever happens in practice). A too-small
// value is the dangerous direction: sweeping a live run marks it
// FAILED while the driver later overwrites it COMPLETED, confusing
// the timeline. Measured against COALESCE(running_at, started_at,
// created_at) — running_at is the dispatcher-side claim stamp,
// started_at the runAssignment stamp, created_at the NOT NULL
// fallback.
const defaultRunningStaleAfter = 2 * time.Hour

// failInterruptedAssignment CAS-transitions one RUNNING assignment to
// FAILED with the given reason, then replays the normal failure
// completion path. Returns handled=false (no error) when the row was
// not RUNNING anymore — a live driver or a concurrent recovery path
// finished it first; losing that race is the designed outcome.
//
// The CAS (WHERE status='RUNNING') is the whole concurrency story:
// boot recovery, the ticker sweeper, and a still-live driver can all
// aim at the same row, and exactly one transition wins. Only the
// winner emits the completion signals.
func (h *AssignmentHandler) failInterruptedAssignment(ctx context.Context, assignmentID, reason string) (bool, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := h.db.ExecContext(ctx, `
		UPDATE assignments
		   SET status = 'FAILED', error_message = ?, finished_at = ?
		 WHERE id = ? AND status = 'RUNNING'`, reason, now, assignmentID)
	if err != nil {
		return false, fmt.Errorf("fail interrupted assignment %s: %w", assignmentID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("fail interrupted assignment %s rows affected: %w", assignmentID, err)
	}
	if n == 0 {
		return false, nil
	}

	// Routing fields for the completion signals. LEFT JOIN because a
	// soft-deleted target agent must not block the recovery of its
	// assignment row.
	var chatID, workspaceID string
	var targetSlug sql.NullString
	if err := h.db.QueryRowContext(ctx, `
		SELECT asn.chat_id, asn.workspace_id, ag.slug
		  FROM assignments asn
		  LEFT JOIN agents ag ON ag.id = asn.assigned_to_id
		 WHERE asn.id = ?`, assignmentID).Scan(&chatID, &workspaceID, &targetSlug); err != nil {
		// The row exists (we just updated it); a scan failure here is a
		// real DB problem. The status transition already landed, so
		// report the error but don't pretend the row is still leaked.
		return true, fmt.Errorf("load routing fields for recovered assignment %s: %w", assignmentID, err)
	}

	// Audit trail: mirror the recovery into the journal. finishAssignment
	// below only writes the terminal run.* entry when it has a runID —
	// recovery has none (the run trace, if any, is recoverOrphanedRuns'
	// jurisdiction) — so without this emit the timeline would show an
	// assignment silently flipping FAILED.
	if _, jerr := h.journal.Emit(ctx, journal.Entry{
		WorkspaceID: workspaceID,
		Type:        journal.EntryAssignmentFail,
		Severity:    journal.SeverityWarn,
		ActorType:   journal.ActorSystem,
		ActorID:     "assignment_recovery",
		Summary:     fmt.Sprintf("assignment %s failed by recovery: %s", shortRunID(assignmentID), reason),
		Payload: map[string]any{
			"assignment_id": assignmentID,
			"reason":        reason,
		},
		Refs: map[string]any{"assignment_id": assignmentID, "chat_id": chatID},
	}); jerr != nil {
		h.logger.Warn("assignment recovery journal emit failed", "error", jerr, "assignment_id", assignmentID)
	}

	// Replay the normal failure path so the lead's chat resolves exactly
	// like an ordinary failed run: WS assignment_failed on the session
	// channel + assignment.updated on the workspace channel, the queue
	// pump for the freed slot, the mission callback, and the mission
	// completion comment. Its unguarded UPDATE re-writes the same
	// FAILED/reason values we just CAS'd in, which is idempotent.
	// runID="" skips the terminal run.* emit (see journal note above).
	h.finishAssignment(ctx, assignmentID, "", chatID, targetSlug.String, workspaceID, "", reason)
	return true, nil
}

// RecoverInterruptedRunning fails every assignment still marked
// RUNNING whose dispatch stamp predates startedBefore (the server
// process start time). Called once from Server.Start, after
// recoverOrphanedRuns: dispatch goroutines are process-local, so any
// RUNNING row older than the process cannot have a live driver.
//
// The startedBefore cutoff (rather than "all RUNNING rows") protects
// the boot-ordering edge: the HTTP listener is already accepting
// requests when recovery runs, so a freshly dispatched assignment
// could legitimately be RUNNING by the time the scan executes — its
// stamp is post-boot and it is skipped.
//
// julianday() normalises the two timestamp shapes the codebase writes
// (claimCrewSlot: 'YYYY-MM-DD HH:MM:SS.SSS'; runAssignment:
// RFC3339 with 'T'/'Z') — a lexicographic compare across the two
// formats would silently misorder them.
//
// Returns the number of assignments recovered. Per-row failures are
// logged and skipped so one bad row cannot strand the rest.
func (h *AssignmentHandler) RecoverInterruptedRunning(ctx context.Context, startedBefore time.Time) (int, error) {
	cutoff := startedBefore.UTC().Format("2006-01-02 15:04:05.000")
	ids, err := h.scanRunningOlderThan(ctx, cutoff)
	if err != nil {
		return 0, fmt.Errorf("recoverInterruptedRunning: %w", err)
	}

	recovered := 0
	for _, id := range ids {
		handled, ferr := h.failInterruptedAssignment(ctx, id, "interrupted by server restart — the run did not survive the previous process")
		if ferr != nil {
			h.logger.Error("recover interrupted assignment failed", "assignment_id", id, "error", ferr)
			continue
		}
		if handled {
			recovered++
		}
	}
	return recovered, nil
}

// SweepStuckRunning fails RUNNING assignments whose dispatch stamp is
// older than staleAfter — the in-process counterpart of the boot-time
// recovery, catching slots leaked without a restart (crashed dispatch
// goroutines, force-killed containers that never wrote a terminal
// status). staleAfter <= 0 falls back to defaultRunningStaleAfter;
// see that constant for why the bound is generous.
//
// Returns the number of assignments swept.
func (h *AssignmentHandler) SweepStuckRunning(ctx context.Context, staleAfter time.Duration) (int, error) {
	if staleAfter <= 0 {
		staleAfter = defaultRunningStaleAfter
	}
	cutoff := time.Now().UTC().Add(-staleAfter).Format("2006-01-02 15:04:05.000")
	ids, err := h.scanRunningOlderThan(ctx, cutoff)
	if err != nil {
		return 0, fmt.Errorf("sweepStuckRunning: %w", err)
	}

	swept := 0
	for _, id := range ids {
		handled, ferr := h.failInterruptedAssignment(ctx, id,
			fmt.Sprintf("assignment stuck in RUNNING for over %s with no completion — failed by the stuck-RUNNING sweeper", staleAfter))
		if ferr != nil {
			h.logger.Error("sweep stuck RUNNING assignment failed", "assignment_id", id, "error", ferr)
			continue
		}
		if handled {
			swept++
		}
	}
	return swept, nil
}

// scanRunningOlderThan returns the ids of RUNNING assignments whose
// best-available dispatch stamp parses older than the cutoff (a
// 'YYYY-MM-DD HH:MM:SS.SSS' UTC string). Shared by boot recovery and
// the sweeper — the two differ only in cutoff semantics and reason.
func (h *AssignmentHandler) scanRunningOlderThan(ctx context.Context, cutoff string) ([]string, error) {
	rows, err := h.db.QueryContext(ctx, `
		SELECT id FROM assignments
		 WHERE status = 'RUNNING'
		   AND julianday(COALESCE(running_at, started_at, created_at)) < julianday(?)
		 ORDER BY created_at ASC`, cutoff)
	if err != nil {
		return nil, fmt.Errorf("scan RUNNING assignments: %w", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if scanErr := rows.Scan(&id); scanErr != nil {
			return nil, fmt.Errorf("scan RUNNING assignment id: %w", scanErr)
		}
		ids = append(ids, id)
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return nil, fmt.Errorf("iterate RUNNING assignments: %w", rowsErr)
	}
	return ids, nil
}

// StartStuckRunningSweeper runs SweepStuckRunning on a ticker until
// ctx is cancelled. Mirrors StartStuckQueueSweeper: returns
// immediately, no immediate first tick (boot recovery already handled
// the restart case synchronously; the ticker exists for leaks that
// develop while the process is up).
//
// interval <= 0 falls back to defaultRunningSweepInterval (5 min).
// staleAfter <= 0 falls back to defaultRunningStaleAfter (2 h).
func (h *AssignmentHandler) StartStuckRunningSweeper(ctx context.Context, interval, staleAfter time.Duration) {
	if interval <= 0 {
		interval = defaultRunningSweepInterval
	}
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				n, err := h.SweepStuckRunning(ctx, staleAfter)
				if err != nil {
					h.logger.Warn("stuck-RUNNING sweeper: scan failed", "error", err)
					continue
				}
				if n > 0 {
					h.logger.Info("stuck-RUNNING sweeper: failed leaked assignments", "swept", n)
				}
			}
		}
	}()
}
