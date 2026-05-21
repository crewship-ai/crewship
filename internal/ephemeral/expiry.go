// Package ephemeral owns the PR-D F5 ephemeral-agent lifecycle
// sweeper. The endpoint surface lives in internal/api (Hire / Rehire);
// this package contains the periodic worker that flips
// agents.expired_at on rows whose expires_at has passed, plus the
// goroutine wrapper that wires it onto a ticker at process start.
//
// We deliberately reuse the existing "Routines" primitive's design
// shape (one Sweep function + one StartSweeper wrapper) rather than
// introducing a new RoutineKind dispatcher — see harbormaster.SweepTimeouts
// / harbormaster.StartTimeoutSweeper for the template. PRD §6 F5 says
// "reuse existing Routines primitive, NOT new scheduler". This is
// that reuse: a goroutine on a ticker scoped to the process lifetime.
//
// Container recycling: the sweep itself only flips DB state. The
// orchestrator's container provider tier (Docker / K8s) GCs orphan
// containers via its own reconciliation loop; we broadcast
// `agent.expired` over the WS hub so chatbridge / UI subscribers can
// drop in-memory caches eagerly instead of waiting for the next poll.
package ephemeral

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
)

// DefaultSweepInterval is the cadence StartExpirySweeper runs the
// sweep on when the caller doesn't override it. 5 minutes balances
// responsiveness against DB pressure on busy workspaces — an ephemeral
// hitting its TTL at minute 0 will ghost no later than minute 5.
// Operators who need tighter expiry latency can pass a shorter
// interval; tests pass 50ms so the goroutine ticks during a single
// test.
const DefaultSweepInterval = 5 * time.Minute

// timeFmt mirrors internal/api / migrations: RFC3339, UTC. The
// expires_at column is written in this format so SQL string compare
// is correct for "in the past" checks.
const timeFmt = time.RFC3339

// Broadcaster is the subset of ws.Hub the sweeper needs to surface
// per-agent expiry events to live UI subscribers. Kept as an
// interface so tests can inject a recording stub without dragging in
// the full hub.
type Broadcaster interface {
	BroadcastWorkspaceEvent(workspaceID, eventType string, payload map[string]string)
}

// Clock is injected so unit tests can drive the sweeper deterministically
// without time.Sleep. Production passes time.Now.
type Clock func() time.Time

// SweepExpiredAgents flips agents.expired_at = now() on every row
// where expires_at < now() AND expired_at IS NULL AND ephemeral = 1
// AND deleted_at IS NULL. Returns the number of rows it transitioned
// to ghost so callers can log a per-tick summary; an error is
// returned ONLY for DB failures the caller probably wants to surface.
// Per-row write failures inside the loop are logged + counted; we
// don't roll back the partial flip because each row's audit/journal
// emit is independent.
//
// The select-then-update pattern (vs a single bulk UPDATE) is
// intentional: we want to know exactly which rows ghosted so the
// journal entry per agent has the right workspace_id / crew_id
// context. A bulk UPDATE would lose that mapping; replaying the
// SELECT after the UPDATE would race with a concurrent rehire.
//
// Returns (n, err) where n is the number of rows actually ghosted.
// On a partial-failure run we return the count of rows we did manage
// to flip alongside the error so the caller can decide whether to
// retry or move on.
func SweepExpiredAgents(ctx context.Context, db *sql.DB, j journal.Emitter, b Broadcaster, now Clock) (int, error) {
	if now == nil {
		now = time.Now
	}
	nowStr := now().UTC().Format(timeFmt)

	// Snapshot the soon-to-be-ghosted rows so each per-row UPDATE
	// can emit a journal entry with correct workspace/crew context.
	// The `expires_at < ?` predicate is index-backed by
	// idx_agent_expires_at (partial index over WHERE expires_at IS
	// NOT NULL AND expired_at IS NULL — see v102 migration), so
	// this scan touches only the rows actually due.
	//
	// PR-D F5 mid-mission grace: status='RUNNING' rows are
	// deliberately excluded. The contract is best-effort TTL — an
	// actively-running ephemeral gets a grace period through its
	// current message so we don't yank the container out from under
	// an in-flight tool call or partial response. The agent
	// naturally transitions RUNNING → IDLE when the message
	// completes; the next sweep tick (≤ DefaultSweepInterval) picks
	// the now-idle ghost up. expires_at is unchanged so the
	// scheduling intent is preserved; only the expired_at flip
	// waits. Without this guard the sweeper would ghost a
	// container that the chatbridge is still streaming from, which
	// surfaces as a "ghost mid-mission" anomaly in the journal
	// (CodeRabbit audit catch, 2026-05-21).
	const selectSQL = `
		SELECT id, workspace_id, COALESCE(crew_id, ''), name,
		       COALESCE(parent_lead_id, ''), expires_at
		FROM agents
		WHERE ephemeral = 1
		  AND expired_at IS NULL
		  AND deleted_at IS NULL
		  AND status != 'RUNNING'
		  AND expires_at IS NOT NULL
		  AND expires_at < ?`
	rows, err := db.QueryContext(ctx, selectSQL, nowStr)
	if err != nil {
		return 0, fmt.Errorf("ephemeral: sweep select: %w", err)
	}

	type due struct {
		id, ws, crew, name, parentLead, expiresAt string
	}
	var pending []due
	for rows.Next() {
		var d due
		if err := rows.Scan(&d.id, &d.ws, &d.crew, &d.name, &d.parentLead, &d.expiresAt); err != nil {
			rows.Close()
			return 0, fmt.Errorf("ephemeral: sweep scan: %w", err)
		}
		pending = append(pending, d)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("ephemeral: sweep rows: %w", err)
	}
	if len(pending) == 0 {
		return 0, nil
	}

	// Per-row UPDATE with the same guard that the SELECT had. If a
	// rehire raced in between the SELECT and UPDATE, the row no
	// longer matches `expired_at IS NULL AND expires_at < ?` and the
	// UPDATE silently skips — exactly what we want, because the
	// rehire is the source of truth. RowsAffected = 0 in that case
	// and we don't double-count the ghost transition.
	var ghosted int
	var firstErr error
	for _, d := range pending {
		res, uerr := db.ExecContext(ctx, `
			UPDATE agents
			SET expired_at = ?, updated_at = ?
			WHERE id = ?
			  AND ephemeral = 1
			  AND expired_at IS NULL
			  AND deleted_at IS NULL
			  AND status != 'RUNNING'
			  AND expires_at IS NOT NULL
			  AND expires_at < ?`,
			nowStr, nowStr, d.id, nowStr)
		if uerr != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("ephemeral: sweep update %s: %w", d.id, uerr)
			}
			continue
		}
		rowsAffected, _ := res.RowsAffected()
		if rowsAffected != 1 {
			// Lost the race to a concurrent rehire — log at debug
			// and skip. Not an error condition.
			continue
		}
		ghosted++

		// Emit the per-agent journal + WS event. Errors are logged
		// in the journal emit path itself; we keep the loop going
		// because the DB flip already succeeded — losing the event
		// would not justify rolling back the ghost.
		if j != nil {
			_, _ = j.Emit(ctx, journal.Entry{
				WorkspaceID: d.ws,
				CrewID:      d.crew,
				AgentID:     d.id,
				Type:        journal.EntryAuditEntityUpdated,
				Severity:    journal.SeverityNotice,
				ActorType:   journal.ActorSystem,
				ActorID:     "ephemeral-expiry",
				Summary:     fmt.Sprintf("ephemeral agent expired: %s", d.name),
				Payload: map[string]any{
					"action":         "agent.expired",
					"agent_id":       d.id,
					"agent_name":     d.name,
					"expires_at":     d.expiresAt,
					"expired_at":     nowStr,
					"parent_lead_id": d.parentLead,
				},
				Refs: map[string]any{"agent_id": d.id, "crew_id": d.crew},
			})
		}
		if b != nil {
			b.BroadcastWorkspaceEvent(d.ws, "agent.expired", map[string]string{
				"id":         d.id,
				"crew_id":    d.crew,
				"name":       d.name,
				"expired_at": nowStr,
			})
		}
	}
	return ghosted, firstErr
}

// StartExpirySweeper kicks off a goroutine that calls
// SweepExpiredAgents on `interval` until ctx is cancelled. Returns
// immediately so the caller (cmd_start.go) can chain its other start
// hooks. The first sweep fires on the first tick — NOT immediately
// at start — to keep startup quiet; an operator who needs an
// eager run can call SweepExpiredAgents directly.
//
// interval <= 0 falls back to DefaultSweepInterval so a misconfigured
// caller doesn't busy-loop on a 0-duration ticker.
func StartExpirySweeper(ctx context.Context, db *sql.DB, j journal.Emitter, b Broadcaster, interval time.Duration, logger *slog.Logger) {
	if interval <= 0 {
		interval = DefaultSweepInterval
	}
	if logger == nil {
		logger = slog.Default()
	}
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				n, err := SweepExpiredAgents(ctx, db, j, b, time.Now)
				if err != nil {
					// Operator-visible: a persistent DB problem
					// means ephemerals never ghost; bell the cat
					// instead of swallowing.
					if !errors.Is(err, context.Canceled) {
						logger.Warn("ephemeral: sweep error", "err", err, "ghosted", n)
					}
					continue
				}
				if n > 0 {
					logger.Info("ephemeral: sweep ghosted agents", "n", n)
				}
			}
		}
	}()
}
