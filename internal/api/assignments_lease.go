package api

// assignments_lease.go — B4's answer to F8 (PRD-ISSUES-AND-ROUTINES-2026
// §9.4/§10.1/§17, work package B4 — #2343): a lease on every RUNNING
// assignment, renewed by the process actually driving it, and recovery
// keyed on that lease's expiry rather than on when the recovering process
// itself booted.
//
// Three pieces, wired together by runAssignment (assignments_run.go) and
// Server.Start (internal/server/server_lifecycle.go):
//
//  1. stampInitialLease — called from the unconditional "Mark assignment as
//     RUNNING" stamp every dispatch door reaches (claimCrewSlot,
//     pumpCrewQueue, and the LeadPlanning bypass that skips both). Sets
//     lease_owner to this process's identity and lease_expires_at to
//     now()+leaseTTL.
//  2. startLeaseHeartbeat — a ticker goroutine started immediately after
//     that stamp and stopped via `defer` in runAssignment, so it wraps
//     every path from "container provisioning begins" through
//     RunAgentForAssignment's return, regardless of which of runAssignment's
//     several early-return finishAssignment calls fires. Each tick renews
//     lease_expires_at; a renewal that fails (SQLITE_BUSY, a cancelled ctx)
//     is logged and retried on the NEXT tick rather than ending a healthy
//     run — the TTL is sized in multiples of the heartbeat interval
//     specifically so one missed renewal cannot expire the lease.
//  3. SweepExpiredLeases / StartLeaseSweeper — the sweeper "copied from
//     harbormaster.StartTimeoutSweeper" the PRD asks for (F48's shape):
//     reap status='RUNNING' rows whose lease_expires_at has passed, via the
//     existing failInterruptedAssignment (assignments_running_recovery.go)
//     so a lease-expired recovery emits the exact same completion signals a
//     stuck-RUNNING sweep does. Runs on its own short ticker, independent
//     of process boot time — the direct fix for F8's two-replica case: a
//     row whose lease a DIFFERENT, still-live process is renewing is never
//     touched, no matter how long ago THIS process started. Boot-time
//     RecoverInterruptedRunning (assignments_running_recovery.go) is
//     amended the same way for the immediate post-crash window before this
//     sweeper's first tick.
//
// What this file does NOT do: decide whether a run "needs a human" (§9.6
// outcome, B6) or hard-kill a container process (B7 Tier 2). A lease expiry
// only ever produces the same Tier-1-shaped FAILED transition
// failInterruptedAssignment already writes for a stuck-RUNNING sweep.

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strconv"
	"sync"
	"time"
)

const (
	// defaultLeaseTTL is how long a RUNNING assignment's lease is valid
	// after a stamp/renewal before the sweeper may reap it. Sized at
	// 4-5x defaultLeaseHeartbeatInterval (see that constant) so a single
	// missed renewal — a transient SQLITE_BUSY, a slow tick under load —
	// can never expire a healthy run's lease; only an owner that has
	// stopped renewing ENTIRELY (crashed, or its process exited) ever
	// crosses this deadline.
	defaultLeaseTTL = 90 * time.Second

	// defaultLeaseHeartbeatInterval is how often the running goroutine
	// renews its own lease while RunAgentForAssignment is in flight.
	// 20s gives 4 renewal attempts inside the 90s TTL before expiry is
	// even possible — the "several ticks of margin" the TTL's own
	// comment promises.
	defaultLeaseHeartbeatInterval = 20 * time.Second

	// defaultLeaseSweepInterval is how often SweepExpiredLeases runs.
	// Short relative to the stuck-RUNNING sweeper's 5-minute default
	// (assignments_running_recovery.go) on purpose: the whole point of a
	// lease is FASTER, owner-aware recovery than that sweeper's
	// per-agent-timeout heuristic can offer. A row can be reaped as soon
	// as ~defaultLeaseTTL + defaultLeaseSweepInterval after its owner
	// stops renewing, not after the old 2-hour floor.
	defaultLeaseSweepInterval = 15 * time.Second
)

// timeFmtRFC3339 mirrors the RFC3339 UTC convention runAssignment already
// writes started_at/finished_at in (assignments_run.go) — lease_expires_at
// has to sort correctly against those same-table timestamps under a plain
// string compare.
const timeFmtRFC3339 = time.RFC3339

// leaseOwnerID is this process's lease identity: hostname:pid. Diagnostic
// only — see the file header for why correctness never depends on two
// processes being able to tell each other apart, only on whether a
// deadline has passed — computed once and cached, matching
// backup.CurrentInstanceHostname's "resolve once, reuse" shape for the
// same os.Hostname() call. A hostname failure (containerised environments
// occasionally return one) degrades to "unknown", never a panic or an
// empty string that every process on a host would then share.
var (
	leaseOwnerOnce sync.Once
	leaseOwnerVal  string
)

func leaseOwnerID() string {
	leaseOwnerOnce.Do(func() {
		host, err := os.Hostname()
		if err != nil || host == "" {
			host = "unknown"
		}
		leaseOwnerVal = host + ":" + strconv.Itoa(os.Getpid())
	})
	return leaseOwnerVal
}

// stampInitialLease sets lease_owner/lease_expires_at on a RUNNING
// assignment. Called unconditionally from runAssignment's "Mark assignment
// as RUNNING" block — the one statement every dispatch door reaches
// (claimCrewSlot and pumpCrewQueue already flipped status='RUNNING' before
// calling runAssignment; the LeadPlanning bypass skips both and relies on
// this exact statement to do it) — so every RUNNING row gets a lease from
// the moment this ships, with no separate migration/backfill needed for
// new rows.
//
// WHERE status='RUNNING' guards against stamping a lease onto a row a
// concurrent recovery/sweep path has already moved off RUNNING in the
// instant between the two statements; RowsAffected==0 is not an error, the
// same "lost race, not a fault" contract every other CAS in this package
// uses.
//
// ttl is a parameter (rather than reading defaultLeaseTTL directly) purely
// for tests: production always passes defaultLeaseTTL (see runAssignment's
// call site); a test that needs a short-lived lease to observe expiry
// without a real 90s wait passes its own.
func stampInitialLease(ctx context.Context, db *sql.DB, assignmentID string, now time.Time, ttl time.Duration) error {
	expires := now.Add(ttl).UTC().Format(timeFmtRFC3339)
	_, err := db.ExecContext(ctx, `
		UPDATE assignments SET lease_owner = ?, lease_expires_at = ?
		 WHERE id = ? AND status = 'RUNNING'`,
		leaseOwnerID(), expires, assignmentID)
	if err != nil {
		return fmt.Errorf("stamp initial lease for %s: %w", assignmentID, err)
	}
	return nil
}

// renewLease extends lease_expires_at by ttl from now, for a row THIS
// process still owns and that is still RUNNING. Returns (false, nil) — not
// an error — when the row is no longer this owner's to renew: a
// sweeper/recovery path reaped it, a different process somehow holds it
// (should not happen given leaseOwnerID's uniqueness, but the guard costs
// nothing), or it already finished. The caller (the heartbeat goroutine)
// treats that as "stop renewing", not "fail the run" — the run itself is
// still executing; only the accounting for it changed underneath.
func renewLease(ctx context.Context, db *sql.DB, assignmentID, owner string, now time.Time, ttl time.Duration) (bool, error) {
	expires := now.Add(ttl).UTC().Format(timeFmtRFC3339)
	res, err := db.ExecContext(ctx, `
		UPDATE assignments SET lease_expires_at = ?
		 WHERE id = ? AND status = 'RUNNING' AND lease_owner = ?`,
		expires, assignmentID, owner)
	if err != nil {
		return false, fmt.Errorf("renew lease for %s: %w", assignmentID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("renew lease rows affected for %s: %w", assignmentID, err)
	}
	return n > 0, nil
}

// startLeaseHeartbeat starts a ticker goroutine that renews assignmentID's
// lease every heartbeatInterval (extending it by ttl each time) until the
// returned stop func is called. runAssignment calls this immediately after
// stampInitialLease and `defer`s the stop func, so the heartbeat covers
// container provisioning, the exec itself, and every one of runAssignment's
// early-return finishAssignment call sites automatically — a defer runs
// regardless of which return statement fires. Production always passes
// (defaultLeaseHeartbeatInterval, defaultLeaseTTL); tests pass shorter
// values so a -race test can observe several real renewals, or an expiry,
// without a 90s sleep.
//
// A renewal failure (context cancelled, a transient DB error) is logged and
// left for the next tick — see defaultLeaseTTL's comment for why one missed
// tick cannot expire a healthy run's lease. The goroutine uses
// context.Background() for its own DB calls rather than the run's ctx: a
// cancelled run ctx (client disconnect, shutdown) must not itself be why the
// lease stops being renewed — that would race the sweeper into reaping a
// run that is still actually executing (the exec was launched from a
// different, longer-lived context upstream). The ticker still stops on
// ctx.Done() OR the returned stop func, whichever comes first, so it never
// outlives the run.
func (h *AssignmentHandler) startLeaseHeartbeat(ctx context.Context, assignmentID string, heartbeatInterval, ttl time.Duration) (stop func()) {
	owner := leaseOwnerID()
	stopCh := make(chan struct{})
	var once sync.Once
	go func() {
		t := time.NewTicker(heartbeatInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-stopCh:
				return
			case <-t.C:
				renewed, err := renewLease(context.Background(), h.db, assignmentID, owner, time.Now(), ttl)
				if err != nil {
					h.logger.Warn("lease heartbeat: renewal failed, will retry next tick",
						"assignment_id", assignmentID, "error", err)
					continue
				}
				if !renewed {
					// Row left RUNNING (reaped, finished, or — should not
					// happen — a different owner). Nothing left to renew;
					// stop ticking rather than spin forever on a row that
					// will never accept our writes again.
					return
				}
			}
		}
	}()
	return func() {
		once.Do(func() { close(stopCh) })
	}
}

// SweepExpiredLeases reaps every RUNNING assignment whose lease has
// expired — status='RUNNING' AND lease_expires_at IS NOT NULL AND
// lease_expires_at < now(). Each is failed through the SAME
// failInterruptedAssignment path the stuck-RUNNING sweeper uses
// (assignments_running_recovery.go), so a lease-expired recovery emits
// identical completion signals (WS assignment_failed, the mission
// callback, the queue pump, the terminal journal entries) — the row's
// terminal-state CAS inside finishAssignment is what makes this race-safe
// against a driver that is, in fact, still alive and finishes at the same
// instant: exactly one of the two writes the row's status, and the other's
// completion signals are suppressed (see finishAssignment's own
// RowsAffected==0 branch).
//
// A row this process itself owns can appear here too (its own heartbeat
// goroutine died without the run itself dying — e.g. a panic recovered
// elsewhere that left the goroutine gone but the exec somehow still
// running in the container): that is not a distinguishable case from "a
// different process died", and treating it identically is correct — a run
// with no live lease-renewer is, operationally, unrecoverable via this
// mechanism either way.
func (h *AssignmentHandler) SweepExpiredLeases(ctx context.Context) (int, error) {
	now := time.Now().UTC().Format(timeFmtRFC3339)
	rows, err := h.db.QueryContext(ctx, `
		SELECT id, COALESCE(lease_owner, '') FROM assignments
		 WHERE status = 'RUNNING'
		   AND lease_expires_at IS NOT NULL
		   AND lease_expires_at < ?
		 ORDER BY created_at ASC`, now)
	if err != nil {
		return 0, fmt.Errorf("sweep expired leases: scan: %w", err)
	}
	type due struct{ id, owner string }
	var pending []due
	for rows.Next() {
		var d due
		if err := rows.Scan(&d.id, &d.owner); err != nil {
			rows.Close()
			return 0, fmt.Errorf("sweep expired leases: row scan: %w", err)
		}
		pending = append(pending, d)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("sweep expired leases: rows: %w", err)
	}

	reaped := 0
	for _, d := range pending {
		reason := fmt.Sprintf(
			"lease expired — no renewal from owner %q within %s; the process that was running this assignment is presumed dead (recovered by the lease sweeper, not a container kill)",
			d.owner, defaultLeaseTTL)
		handled, ferr := h.failInterruptedAssignment(ctx, d.id, reason)
		if ferr != nil {
			h.logger.Error("sweep expired lease: fail assignment failed", "assignment_id", d.id, "error", ferr)
			continue
		}
		if handled {
			reaped++
		}
	}
	return reaped, nil
}

// StartLeaseSweeper runs SweepExpiredLeases and
// ReconcileExpiredEphemeralSessions on one ticker until ctx is cancelled —
// the harbormaster.StartTimeoutSweeper shape (F48) the PRD asks for, one
// new ticker rather than two: both sub-sweeps are "a session/run went
// unowned, close the books on it" and share a cadence, so one goroutine
// does both rather than the codebase growing a fourth near-identical
// ticker wrapper.
func (h *AssignmentHandler) StartLeaseSweeper(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = defaultLeaseSweepInterval
	}
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if n, err := h.SweepExpiredLeases(ctx); err != nil {
					h.logger.Warn("lease sweeper: sweep expired leases failed", "error", err)
				} else if n > 0 {
					h.logger.Info("lease sweeper: recovered lease-expired assignments", "count", n)
				}
				if n, err := h.ReconcileExpiredEphemeralSessions(ctx); err != nil {
					h.logger.Warn("lease sweeper: reconcile ephemeral sessions failed", "error", err)
				} else if n > 0 {
					h.logger.Info("lease sweeper: reconciled sessions for expired ephemeral agents", "count", n)
				}
			}
		}
	}()
}
