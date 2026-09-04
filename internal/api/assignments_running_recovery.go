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
//     the stuck-QUEUED sweeper header explicitly punts on). Its
//     staleness bound is per-assignment — max(the target agent's
//     configured timeout_seconds + a grace margin, a generous floor)
//     — so it can never race a healthy run, even one configured to
//     run longer than the floor.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// defaultRunningSweepInterval is the gap between stuck-RUNNING sweeper
// ticks. Same 5-minute grain as the stuck-QUEUED sweeper's default:
// the scan is one cheap indexed query, and a leaked slot surviving up
// to one extra interval is irrelevant next to the staleness bound
// below.
const defaultRunningSweepInterval = 5 * time.Minute

// defaultRunningStaleAfter is the FLOOR on how long a row may sit
// RUNNING before the sweeper declares it leaked and fails it. This is
// deliberately generous — 2 hours — because unlike QUEUED (where any
// dwell beyond the pump cadence is pathological), RUNNING rows are
// backed by real agent executions whose duration is bounded only by
// the per-agent timeout_seconds plus provisioning overhead (image
// build, container boot). A too-small value is the dangerous
// direction: sweeping a live run marks it FAILED, frees the crew slot
// for a second concurrent exec, and sets up a FAILED-vs-COMPLETED
// terminal collision when the still-live driver finishes (the
// finishAssignment CAS makes the driver lose, but the run's real
// result is discarded).
//
// The floor alone is NOT the whole bound: agents can legitimately be
// configured with timeout_seconds at or above 2h, so the sweeper's
// per-row staleness bound is max(agent timeout_seconds +
// runningSweepGraceMargin, this floor) — see scanRunningStuck. The
// dwell is measured against COALESCE(running_at, started_at,
// created_at) — running_at is the dispatcher-side claim stamp,
// started_at the runAssignment stamp, created_at the NOT NULL
// fallback.
const defaultRunningStaleAfter = 2 * time.Hour

// runningSweepGraceMargin is added on top of an agent's configured
// timeout_seconds when computing the sweeper's per-row staleness
// bound. It absorbs the overhead a legitimate run pays outside the
// timed agent exec — image build, container boot, credential loading
// — so a run that uses its full configured timeout is never swept
// mid-flight just because provisioning ate a few extra minutes.
const runningSweepGraceMargin = 15 * time.Minute

// failInterruptedAssignment transitions one RUNNING assignment to
// FAILED with the given reason via the normal failure completion path.
// Returns handled=false (no error) when the row was not RUNNING
// anymore — a live driver or a concurrent recovery path finished it
// first; losing that race is the designed outcome. Also returns
// handled=false when a live lease renewal is discovered at the last
// instant (see claimLeaseStillExpired) — a run a heartbeat is still
// actively renewing is never reaped, no matter how stale the caller's
// own SELECT that led here was.
//
// The terminal CAS lives inside finishAssignment (WHERE status is not
// already terminal) and is the whole concurrency story: boot recovery,
// the ticker sweeper, and a still-live driver can all aim at the same
// row, and exactly one transition wins. Only the winner emits the
// completion signals (WS assignment_failed on the session channel +
// assignment.updated on the workspace channel, the queue pump for the
// freed slot, the mission callback, and the mission completion
// comment). runID="" skips the terminal run.* emit — recovery has no
// run trace of its own (that is recoverOrphanedRuns' jurisdiction).
func (h *AssignmentHandler) failInterruptedAssignment(ctx context.Context, assignmentID, reason string) (bool, error) {
	// B4 (#2343, CodeRabbit review): re-verify — atomically, in the SAME
	// statement — that the row is still RUNNING with no live lease, right
	// before claiming it. Every caller of this function (boot recovery,
	// both tickers) decided this row was reapable from a SELECT that ran
	// some time ago; without this re-check, a heartbeat renewal landing in
	// that window would be invisible to the plain "not already terminal"
	// CAS below, and a run that is, in fact, still alive would be failed
	// out from under it. A legacy row (lease_expires_at IS NULL) always
	// passes this check, so the pre-B4 heuristic paths are unaffected.
	claimed, cerr := claimLeaseStillExpired(ctx, h.db, assignmentID)
	if cerr != nil {
		return false, fmt.Errorf("recheck lease before reaping %s: %w", assignmentID, cerr)
	}
	if !claimed {
		// Either the row already left RUNNING (another path is handling
		// it) or its lease was renewed after the caller's own SELECT —
		// either way, this is not this call's row to reap.
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
		if errors.Is(err, sql.ErrNoRows) {
			// Row vanished between scan and recovery — nothing to free.
			return false, nil
		}
		return false, fmt.Errorf("load routing fields for recovered assignment %s: %w", assignmentID, err)
	}

	// nil accumulator: recovery never ran the agent, so there is no session
	// provenance to record and none is invented.
	if !h.finishAssignment(ctx, assignmentID, "", chatID, targetSlug.String, workspaceID, "", reason, nil) {
		// Lost the terminal CAS — a live driver (or another recovery
		// pass) finished the row first and owns the completion signals.
		return false, nil
	}

	// The terminal assignment.failed journal entry is emitted inside
	// finishAssignment (unconditionally, independent of runID), so the
	// recovery path no longer writes its own — a second emit here would
	// duplicate the row in the activity feed. finishAssignment's entry
	// carries the same assignment_id/chat_id refs plus crew/agent routing;
	// the recovery reason travels through the errMsg argument above and
	// lands in that entry's error_message payload.
	return true, nil
}

// RecoverInterruptedRunning fails every assignment still marked RUNNING
// that this process cannot possibly be driving. Called once from
// Server.Start, after recoverOrphanedRuns.
//
// B4 (§9.4/§17, #2343, F8): a row is recoverable one of two ways, and which
// way applies is decided by whether it carries a lease, NOT by comparing
// timestamps against this process's own boot time:
//
//   - lease_expires_at IS NOT NULL — the row has a lease. It is touched
//     ONLY if that lease has already expired (< now()). A row whose lease a
//     DIFFERENT, still-live process is actively renewing is never touched
//     here, no matter how long ago the recovering process itself booted —
//     this is the direct fix for F8's two-replica case ("nothing records
//     which process owns a run, so a second replica's boot would fail the
//     first replica's live runs").
//   - lease_expires_at IS NULL — a legacy row: dispatched before this
//     migration shipped, or caught in the (sub-millisecond) window between
//     claimCrewSlot's CAS and stampInitialLease's own write. Falls back to
//     the pre-B4 heuristic this function always used: dispatch stamp
//     predates startedBefore (the server process start time) — sound for
//     exactly this case, because a legacy row genuinely predates the lease
//     mechanism and boot-time is the only signal available for it.
//
// The startedBefore cutoff on the legacy branch (rather than "any NULL-
// lease RUNNING row") protects the same boot-ordering edge it always did:
// the HTTP listener is already accepting requests when recovery runs, so a
// freshly dispatched (and not-yet-leased) assignment could legitimately be
// RUNNING by the time the scan executes — its stamp is post-boot and it is
// skipped.
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
	now := time.Now().UTC().Format(timeFmtRFC3339)
	ids, err := h.scanRunningOlderThan(ctx, cutoff, now)
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

// SweepStuckRunning fails RUNNING assignments that outlived their
// per-row staleness bound — the in-process counterpart of the
// boot-time recovery, catching slots leaked without a restart
// (crashed dispatch goroutines, force-killed containers that never
// wrote a terminal status).
//
// Unlike boot recovery (where every pre-boot RUNNING row is an orphan
// by construction), the sweeper races live drivers, so the bound is
// per-assignment: max(target agent's configured timeout_seconds +
// runningSweepGraceMargin, staleAfter). An assignment whose configured
// timeout (plus grace) has not elapsed always has a potentially live
// driver and is never swept, no matter how far past staleAfter it is.
// staleAfter <= 0 falls back to defaultRunningStaleAfter; see that
// constant for why the floor is generous.
//
// Returns the number of assignments swept.
func (h *AssignmentHandler) SweepStuckRunning(ctx context.Context, staleAfter time.Duration) (int, error) {
	if staleAfter <= 0 {
		staleAfter = defaultRunningStaleAfter
	}
	ids, err := h.scanRunningStuck(ctx, time.Now(), staleAfter)
	if err != nil {
		return 0, fmt.Errorf("sweepStuckRunning: %w", err)
	}

	swept := 0
	for _, id := range ids {
		handled, ferr := h.failInterruptedAssignment(ctx, id,
			fmt.Sprintf("assignment stuck in RUNNING past its staleness bound (the agent's configured timeout + %s grace, floor %s) with no completion — failed by the stuck-RUNNING sweeper", runningSweepGraceMargin, staleAfter))
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

// scanRunningOlderThan returns the ids of RUNNING assignments boot
// recovery may touch — see RecoverInterruptedRunning's doc comment for the
// two branches this implements:
//
//   - a row WITH a lease (lease_expires_at IS NOT NULL) is included only if
//     that lease has already expired against nowRFC3339 — never against
//     cutoff, and never merely because it predates this process's boot.
//     That is the whole F8 fix: a live lease means some process (possibly
//     not this one) is still renewing it, and boot time is irrelevant to
//     that fact.
//   - a row WITHOUT a lease (lease_expires_at IS NULL — a legacy row,
//     dispatched before this migration or in the sub-millisecond window
//     before stampInitialLease's own write) falls back to the pre-B4
//     heuristic: its best-available dispatch stamp (a
//     'YYYY-MM-DD HH:MM:SS.SSS' UTC string, or runAssignment's RFC3339
//     shape — julianday() normalises both) parses older than cutoff.
func (h *AssignmentHandler) scanRunningOlderThan(ctx context.Context, cutoff, nowRFC3339 string) ([]string, error) {
	rows, err := h.db.QueryContext(ctx, `
		SELECT id FROM assignments
		 WHERE status = 'RUNNING'
		   AND (
		     (lease_expires_at IS NOT NULL AND lease_expires_at < ?)
		     OR
		     (lease_expires_at IS NULL AND julianday(COALESCE(running_at, started_at, created_at)) < julianday(?))
		   )
		 ORDER BY created_at ASC`, nowRFC3339, cutoff)
	if err != nil {
		return nil, fmt.Errorf("scan RUNNING assignments: %w", err)
	}
	defer rows.Close()
	return collectAssignmentIDs(rows)
}

// scanRunningStuck returns the ids of RUNNING assignments whose dwell
// (now minus best-available dispatch stamp) exceeds their per-row
// staleness bound: max(target agent's timeout_seconds +
// runningSweepGraceMargin, floor). Sweeper only — this is what lets an
// operator configure timeout_seconds >= the floor without the sweeper
// killing the run mid-flight.
//
// LEFT JOIN keeps rows whose target agent was hard-deleted sweepable
// (COALESCE treats a missing timeout as 0, so the floor governs).
// julianday() normalises the two timestamp shapes the codebase writes
// (claimCrewSlot: 'YYYY-MM-DD HH:MM:SS.SSS'; runAssignment: RFC3339
// with 'T'/'Z'); the day-difference is converted to seconds for the
// comparison against the seconds-grain bound.
func (h *AssignmentHandler) scanRunningStuck(ctx context.Context, now time.Time, floor time.Duration) ([]string, error) {
	nowStr := now.UTC().Format("2006-01-02 15:04:05.000")
	rows, err := h.db.QueryContext(ctx, `
		SELECT asn.id FROM assignments asn
		  LEFT JOIN agents ag ON ag.id = asn.assigned_to_id
		 WHERE asn.status = 'RUNNING'
		   AND (julianday(?) - julianday(COALESCE(asn.running_at, asn.started_at, asn.created_at))) * 86400.0
		       > MAX(COALESCE(ag.timeout_seconds, 0) + ?, ?)
		 ORDER BY asn.created_at ASC`,
		nowStr, runningSweepGraceMargin.Seconds(), floor.Seconds())
	if err != nil {
		return nil, fmt.Errorf("scan stuck RUNNING assignments: %w", err)
	}
	defer rows.Close()
	return collectAssignmentIDs(rows)
}

// collectAssignmentIDs drains a single-id-column result set. Shared by
// the two RUNNING scans above.
func collectAssignmentIDs(rows *sql.Rows) ([]string, error) {
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
