package api

// The escalation state machine and its deadline.
//
// ── What was broken ───────────────────────────────────────────────────────
//
// An escalation is a question an agent asked a human. The agent-side wait
// (internal/sidecar/query.go) gave up after 300 s and the agent PROCEEDED
// WITHOUT AN ANSWER, while the row stayed 'PENDING' forever: nothing ever
// wrote a terminal status for a question nobody answered. The agent's belief
// and the database disagreed on every timed-out escalation the system had ever
// raised, and neither side said so.
//
// ── The vocabulary ────────────────────────────────────────────────────────
//
//	PENDING → RESOLVED    a human decided; `action` says approve/reject/redirect
//	        → EXPIRED     the deadline passed and no human decided
//	        → CANCELLED   a human withdrew the question before deciding
//
// RESOLVED is kept, not renamed to the audit's proposed ANSWERED, and
// rejection stays in `action` rather than becoming a REJECTED status — see the
// migration (20260813212851_escalation_deadline.sql) for why adding either
// would have been a fifth spelling of "done". Terminal states are terminal:
// every transition out of one is refused with 409.
//
// ── Where expiry is decided ───────────────────────────────────────────────
//
// Both, and deliberately, mirroring how harbormaster settles the identical
// question for approvals (internal/harbormaster/gate.go's deadline branch plus
// internal/harbormaster/store_sweep.go):
//
//	1. THE WAITER. The long poll the sidecar is already blocked on knows the
//	   deadline and flips the row when it passes, then answers EXPIRED. This is
//	   what makes the agent's give-up and the row's state the SAME event rather
//	   than two events that happen to agree.
//	2. THE SWEEPER. A crashed sidecar leaves nobody waiting, so a background
//	   ticker catches those rows, and the escalation read paths (list,
//	   pending-count) sweep their own workspace first so no surface can show a
//	   question as open past its deadline.
//
// Every path funnels through expireEscalationRow, whose UPDATE is guarded on
// status='PENDING'. The transition therefore happens exactly once no matter how
// many observers notice at the same moment, and the journal entry is written
// only by the caller that actually flipped the row.
//
// ── What the agent is told when nobody answers ────────────────────────────
//
// The product decision — previously made by omission — is CONTINUE WITH AN
// EXPLICIT WARNING, recorded as escalationOutcomeContinuedWithWarning in the
// journal payload and returned to the agent in the wait response's `warning`
// field. The run is not failed and no default answer is invented: an agent
// blocked on a question is usually able to make progress without it, and
// failing runs because a human was at lunch would make the escalation tool too
// expensive to use. What is NOT acceptable is the old behaviour of doing that
// silently, so the warning is mandatory on the wire, the row is terminal, and
// the journal entry is severity=warn so it surfaces in the default
// attention filter. Stated in docs/guides/harbormaster.mdx and
// docs/cli/escalation.mdx.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/crewship-ai/crewship/internal/inbox"
	"github.com/crewship-ai/crewship/internal/journal"
)

// The four statuses. Strings, not a typed enum, because they are compared
// against a TEXT column in raw SQL throughout this package and a type that has
// to be unwrapped at every call site buys nothing here.
const (
	escalationStatusPending   = "PENDING"
	escalationStatusResolved  = "RESOLVED"
	escalationStatusExpired   = "EXPIRED"
	escalationStatusCancelled = "CANCELLED"
)

// escalationOutcomeContinuedWithWarning names what the agent does when its
// question expires. It is a constant rather than a literal so the wire, the
// journal and the docs cannot drift into describing different policies.
const escalationOutcomeContinuedWithWarning = "continued_with_warning"

// escalationExpiredWarning is the text handed to the agent in place of an
// answer. It is deliberately imperative: an agent reading it should treat the
// absence of a decision as a fact about the world, not as permission.
const escalationExpiredWarning = "No human answered before the deadline. Continue WITHOUT the answer you asked for: " +
	"do not assume approval, state in your result that this question went unanswered, and avoid irreversible actions " +
	"that depended on it."

// escalationTTL is how long a raised escalation stays answerable. A var, not a
// const, so tests can shrink it; production never reassigns it.
//
// 300 s matches what internal/sidecar/query.go has always waited — but the
// agreement is now structural rather than coincidental: the server writes
// deadline_at from this value and RETURNS it, and the sidecar bounds its poll
// on what it was told. Changing this number moves both sides at once.
var escalationTTL = 300 * time.Second

// escalationSweepInterval is the background sweeper's tick. Generous on
// purpose: the waiter already handles every escalation someone is actually
// waiting on, so this ticker only exists for rows whose sidecar died, and
// those are not urgent — they are just not allowed to be permanent.
const escalationSweepInterval = 60 * time.Second

// expirableEscalation is the scope a terminal transition needs in order to
// journal itself: who asked, in which crew, on which conversation.
type expirableEscalation struct {
	id          string
	workspaceID string
	crewID      string
	chatID      string
	fromAgentID string
	reason      string
	// deadlineAt is the row's own deadline, carried so the expiry describes
	// the deadline this question actually had rather than whatever
	// escalationTTL happens to be when the sweep runs. A row raised under a
	// different TTL must not be recorded as having expired under the current
	// one.
	deadlineAt string
}

// escalationTerminalError describes a refused transition out of a terminal
// state, in the caller's words rather than in the machine's.
func escalationTerminalError(status string) string {
	switch status {
	case escalationStatusExpired:
		return "escalation expired — nobody answered before its deadline and the agent has already continued without it; raise a new one"
	case escalationStatusCancelled:
		return "escalation was cancelled — the question was withdrawn before it was decided"
	default:
		return "escalation already resolved"
	}
}

// ─── the one transition function ──────────────────────────────────────────

// expireEscalationRow flips one PENDING escalation to EXPIRED and, only if
// THIS call was the one that flipped it, writes the terminal side effects:
// journal entry, inbox projection, waiter wakeup and broadcasts.
//
// Returns whether this call performed the transition. A false means somebody
// else got there first — a human resolving in the same instant, a second
// sweeper tick, a waiter and the background ticker racing — and the caller must
// treat that as "not mine to describe". That guard is the entire reason the
// journal can never contain two expiries for one escalation.
func (h *QueryHandler) expireEscalationRow(ctx context.Context, row expirableEscalation, at time.Time) (bool, error) {
	now := at.UTC().Format(time.RFC3339)
	resolution := "expired: the deadline passed with no human answer"
	if row.deadlineAt != "" {
		resolution = fmt.Sprintf("expired: no human answered by %s", row.deadlineAt)
	}

	res, err := h.db.ExecContext(ctx, `
		UPDATE escalations
		   SET status = ?, resolution = ?, resolved_at = ?, resolved_by = 'system'
		 WHERE id = ? AND status = ?`,
		escalationStatusExpired, resolution, now, row.id, escalationStatusPending)
	if err != nil {
		return false, fmt.Errorf("expire escalation %s: %w", row.id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("expire escalation %s rows: %w", row.id, err)
	}
	if n == 0 {
		return false, nil
	}

	// Drop the row out of "needs action". resolved_by is 'system' and the
	// action is the expiry itself, so the inbox does not claim a human acted.
	inbox.ResolveBySource(ctx, h.db, h.logger, "escalation", row.id, "expired", "")

	// Severity warn, not notice: the create emit is warn because an open
	// question needs attention, and an agent that proceeded without its answer
	// needs it just as much. This is the entry an operator reads to find out
	// that a decision they meant to make was made for them by a clock.
	_, _ = h.journal.Emit(ctx, journal.Entry{
		WorkspaceID: row.workspaceID,
		CrewID:      row.crewID,
		AgentID:     row.fromAgentID,
		Type:        journal.EntryPeerEscalation,
		Severity:    journal.SeverityWarn,
		ActorType:   journal.ActorSystem,
		ActorID:     "escalation_deadline",
		Summary:     fmt.Sprintf("escalation expired unanswered: %s", truncate(row.reason, 140)),
		Payload: map[string]any{
			"state":         "expired",
			"resolution":    resolution,
			"deadline_at":   row.deadlineAt,
			"agent_outcome": escalationOutcomeContinuedWithWarning,
			"warning":       escalationExpiredWarning,
		},
		Refs: map[string]any{"escalation_id": row.id, "chat_id": row.chatID},
	})

	// Wake anything still blocked on the long poll. Waiters that timed out
	// already are discarded by notifyEscalationWaiter's non-blocking send.
	h.notifyEscalationWaiter(row.id, escalationResult{
		Status:  escalationStatusExpired,
		Warning: escalationExpiredWarning,
	})

	broadcastChannelEvent(h.hub, "session", row.chatID, "escalation_expired",
		map[string]string{"id": row.id, "status": escalationStatusExpired, "warning": escalationExpiredWarning})
	broadcastWorkspaceEvent(h.hub, row.workspaceID, "escalation.expired",
		map[string]string{"id": row.id, "crew_id": row.crewID, "status": escalationStatusExpired})

	h.logger.Info("escalation expired unanswered",
		"escalation_id", row.id, "crew_id", row.crewID, "deadline_at", row.deadlineAt)
	return true, nil
}

// ─── the sweep ────────────────────────────────────────────────────────────

// escalationDuePredicate is the SQL that finds questions whose time is up.
//
// datetime() on both sides rather than a bare string comparison: deadline_at is
// TEXT, this package writes RFC3339 ('…T…Z') and other writers on this table
// use SQLite's datetime('now') shape ('… …'). Those two orderings disagree
// lexically — 'T' sorts after ' ' — so a raw `deadline_at <= ?` would silently
// skip rows depending on who wrote them, which is a deadline that never fires.
// It costs the index's range half; the leading status equality is what keeps
// the scan proportional to what is still open rather than to all history.
const escalationDuePredicate = `status = ? AND deadline_at IS NOT NULL AND datetime(deadline_at) <= datetime(?)`

// sweepExpiredEscalations expires every past-deadline PENDING escalation and
// returns how many rows THIS call transitioned.
//
// A workspaceID scopes the sweep to one tenant — what the read paths and the
// operator-facing endpoint use, so neither can reach another tenant's rows. An
// empty workspaceID sweeps everything and is the background sweeper's mode.
func (h *QueryHandler) sweepExpiredEscalations(ctx context.Context, workspaceID string) (int, error) {
	now := time.Now().UTC()
	query := `SELECT id, workspace_id, crew_id, chat_id, from_agent_id, reason, COALESCE(deadline_at, '')
		FROM escalations WHERE ` + escalationDuePredicate
	args := []interface{}{escalationStatusPending, now.Format(time.RFC3339)}
	if workspaceID != "" {
		query += ` AND workspace_id = ?`
		args = append(args, workspaceID)
	}

	rows, err := h.db.QueryContext(ctx, query, args...)
	if err != nil {
		return 0, fmt.Errorf("sweep escalations: %w", err)
	}
	// Snapshot before mutating: the UPDATE's status guard is the source of
	// truth, so a resolve landing between this SELECT and the flip simply
	// makes that row's transition return false. Holding a cursor open across
	// the writes would be the alternative, and SQLite does not thank you for
	// it.
	var due []expirableEscalation
	for rows.Next() {
		var e expirableEscalation
		if err := rows.Scan(&e.id, &e.workspaceID, &e.crewID, &e.chatID, &e.fromAgentID, &e.reason, &e.deadlineAt); err != nil {
			rows.Close()
			return 0, fmt.Errorf("sweep escalations scan: %w", err)
		}
		due = append(due, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("sweep escalations rows: %w", err)
	}

	expired := 0
	for _, e := range due {
		flipped, err := h.expireEscalationRow(ctx, e, now)
		if err != nil {
			// One bad row must not abort the pass — the next tick would hit
			// the same row and stall behind it forever.
			h.logger.Warn("sweep escalations: expire row", "error", err, "escalation_id", e.id)
			continue
		}
		if flipped {
			expired++
		}
	}
	return expired, nil
}

// sweepExpiredEscalationsBestEffort runs the workspace-scoped sweep ahead of a
// read so no surface can report a question as open past its deadline. Failures
// are logged and swallowed: a list request must still answer if the
// housekeeping half of it could not.
func (h *QueryHandler) sweepExpiredEscalationsBestEffort(ctx context.Context, workspaceID string) {
	if workspaceID == "" {
		return
	}
	if _, err := h.sweepExpiredEscalations(ctx, workspaceID); err != nil {
		h.logger.Warn("escalation read-path sweep", "error", err, "workspace_id", workspaceID)
	}
}

// StartEscalationExpirySweeper runs sweepExpiredEscalations on a ticker until
// ctx is done. The net for escalations nobody is waiting on any more — a
// crashed or restarted sidecar leaves a PENDING row with no observer, and
// without this it would stay open until a human happened to list that crew.
//
// Pattern mirrors StartStuckQueueSweeper
// (internal/api/assignments_stuck_sweeper.go), including its deliberate
// absence from beginBackgroundWork: this is a boot daemon that runs until its
// context is cancelled, so registering it would make every test teardown that
// drains background work block for the drain's full timeout. It is listed in
// unregisteredSpawnSites (background_guard_test.go) with that reason.
//
// No immediate first tick, for the same reason the queue sweeper skips one: at
// startup the long polls have not had a chance to re-establish, and expiring a
// row a reconnecting sidecar is about to wait on would take the answer away
// from an agent that is still there to receive it.
func (h *QueryHandler) StartEscalationExpirySweeper(ctx context.Context, interval time.Duration) {
	if h == nil || h.db == nil {
		return
	}
	if interval <= 0 {
		interval = escalationSweepInterval
	}
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if _, err := h.sweepExpiredEscalations(ctx, ""); err != nil && ctx.Err() == nil {
					h.logger.Warn("escalation expiry sweeper", "error", err)
				}
			}
		}
	}()
}

// ─── HTTP: the sweep as an operator verb ──────────────────────────────────

// SweepExpiredEscalations handles POST /api/v1/escalations/sweep-expired.
//
// The background sweeper already does this on a timer; the endpoint exists so
// the deadline mechanism is operable and observable from the CLI rather than
// only inferable from a ticker nobody can see. Scoped to the caller's
// workspace and gated on manage, because it writes terminal states.
//
// ADMIN+, returns {"expired": N}.
func (h *QueryHandler) SweepExpiredEscalations(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	if workspaceID == "" {
		replyError(w, http.StatusUnauthorized, "workspace required")
		return
	}
	if !canRole(RoleFromContext(r.Context()), "manage") {
		replyError(w, http.StatusForbidden, "admin role required")
		return
	}
	n, err := h.sweepExpiredEscalations(r.Context(), workspaceID)
	if err != nil {
		replyInternalError(w, h.logger, "sweep expired escalations", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"expired": n})
}

// ─── HTTP: cancel ─────────────────────────────────────────────────────────

// CancelEscalation handles POST /api/v1/escalations/{escalationId}/cancel —
// a human withdrawing a question they are not going to answer.
//
// Distinct from resolving with action='reject': a rejection is a DECISION the
// agent should act on ("no, do not do that"), while a cancellation says the
// question stopped mattering. Collapsing the two would tell an agent it was
// refused when in fact nobody ever considered it.
//
// MANAGER+, mirroring ResolveEscalation: closing out a blocking request is the
// same class of act whichever way it is closed.
func (h *QueryHandler) CancelEscalation(w http.ResponseWriter, r *http.Request) {
	escalationID := r.PathValue("escalationId")
	workspaceID := WorkspaceIDFromContext(r.Context())
	if !canRole(RoleFromContext(r.Context()), "create") {
		replyError(w, http.StatusForbidden, "MANAGER+ role required to cancel an escalation")
		return
	}
	actorID := ""
	if u := UserFromContext(r.Context()); u != nil {
		actorID = u.ID
	}

	var body struct {
		Reason string `json:"reason"`
	}
	// An absent body is fine — a reason is good manners, not a precondition.
	_ = readJSON(r, &body)

	var status, crewID, chatID, fromAgentID, fromSlug string
	err := h.db.QueryRowContext(r.Context(), `
		SELECT e.status, e.crew_id, e.chat_id, e.from_agent_id, a.slug
		FROM escalations e
		JOIN agents a ON a.id = e.from_agent_id
		WHERE e.id = ? AND e.workspace_id = ?`,
		escalationID, workspaceID).Scan(&status, &crewID, &chatID, &fromAgentID, &fromSlug)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// A foreign id is the same 404 as an unknown one — the
			// neighbouring convention (see WaitForEscalationResponse), so the
			// endpoint is not an existence oracle for other tenants.
			replyError(w, http.StatusNotFound, "escalation not found")
			return
		}
		replyInternalError(w, h.logger, "cancel escalation lookup", err)
		return
	}
	if status != escalationStatusPending {
		replyError(w, http.StatusConflict, escalationTerminalError(status))
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)
	resolution := "cancelled by operator"
	if body.Reason != "" {
		resolution = "cancelled: " + body.Reason
	}
	res, err := h.db.ExecContext(r.Context(), `
		UPDATE escalations
		   SET status = ?, resolution = ?, resolved_at = ?, resolved_by = 'user'
		 WHERE id = ? AND workspace_id = ? AND status = ?`,
		escalationStatusCancelled, resolution, now, escalationID, workspaceID, escalationStatusPending)
	if err != nil {
		replyInternalError(w, h.logger, "cancel escalation update", err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// Lost the race with a resolve or an expiry.
		replyError(w, http.StatusConflict, "escalation is no longer pending")
		return
	}

	inbox.ResolveBySource(r.Context(), h.db, h.logger, "escalation", escalationID, "cancelled", actorID)

	_, _ = h.journal.Emit(r.Context(), journal.Entry{
		WorkspaceID: workspaceID,
		CrewID:      crewID,
		AgentID:     fromAgentID,
		Type:        journal.EntryPeerEscalation,
		Severity:    journal.SeverityNotice,
		ActorType:   journal.ActorUser,
		ActorID:     actorID,
		Summary:     fmt.Sprintf("escalation %s cancelled", escalationID),
		Payload: map[string]any{
			"state":         "cancelled",
			"reason":        body.Reason,
			"resolution":    resolution,
			"from_slug":     fromSlug,
			"agent_outcome": escalationOutcomeContinuedWithWarning,
		},
		Refs: map[string]any{"escalation_id": escalationID, "chat_id": chatID},
	})

	// The agent asked and is still blocked; tell it the question is gone
	// rather than leaving it to the deadline.
	h.notifyEscalationWaiter(escalationID, escalationResult{
		Status:  escalationStatusCancelled,
		Warning: "The question was withdrawn by a human before it was decided. Continue without an answer and do not assume approval.",
	})

	broadcastChannelEvent(h.hub, "session", chatID, "escalation_cancelled",
		map[string]string{"id": escalationID, "status": escalationStatusCancelled, "reason": body.Reason})
	broadcastWorkspaceEvent(h.hub, workspaceID, "escalation.cancelled",
		map[string]string{"id": escalationID, "crew_id": crewID, "from_slug": fromSlug})

	h.logger.Info("escalation cancelled", "escalation_id", escalationID, "crew_id", crewID, "actor", actorID)
	writeJSON(w, http.StatusOK, map[string]string{
		"id":     escalationID,
		"status": escalationStatusCancelled,
	})
}
