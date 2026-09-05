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
// ── Two clocks, not one ───────────────────────────────────────────────────
//
// The first version of this file collapsed the agent's wait and the human's
// answerability into ONE 300 s deadline, which made the approval queue expire
// five minutes after it was raised: an operator who walked off to fetch an API
// key came back to 409 for a question nobody had ever answered. They are two
// different questions and they now have two different columns.
//
//	deadline_at         the AGENT's wait window (escalationAgentWait, 300 s).
//	                    Bounds the sidecar's long poll and nothing else. When it
//	                    passes, agent_gave_up_at is stamped, the agent is told to
//	                    continue with an explicit warning, and THE ROW STAYS
//	                    PENDING. An agent giving up is not a decision.
//
//	answer_deadline_at  the HUMAN's answerability window (escalationAnswerTTL,
//	                    7 days). When THIS passes with no decision the row goes
//	                    EXPIRED and any staged credential is disposed of.
//
// What happens to a late answer — a human resolving after the agent gave up —
// is stated in ResolveEscalation and in docs/guides/harbormaster.mdx: the
// resolution is recorded, an approved credential is activated so the NEXT run
// has it, and the response says `agent_still_waiting: false` so the operator is
// not left believing they unblocked the run that asked. The run is not resumed;
// it ended minutes or days ago having been told, in writing, that it was
// continuing without the answer.
//
// ── The vocabulary ────────────────────────────────────────────────────────
//
//	PENDING → RESOLVED    a human decided; `action` says approve/reject/redirect
//	        → EXPIRED     the ANSWER deadline passed and no human decided
//	        → CANCELLED   a human withdrew the question before deciding
//
// UNANSWERED is not in that list. It is a WIRE status only
// (escalationWireUnanswered), the answer the long poll gives an agent whose
// window closed while the question stayed open. Making it a row status would be
// a fourth spelling of "still waiting for a human".
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
//	1. THE WAITER. The long poll knows the AGENT's deadline, stamps
//	   agent_gave_up_at when it passes and answers UNANSWERED. That records the
//	   agent's give-up as the same event that ends its wait — which is the half
//	   of the original design worth keeping — WITHOUT taking the question away
//	   from the human.
//	2. THE SWEEPER. The HUMAN's deadline is nobody's long poll, so it belongs to
//	   a background ticker; the escalation read paths (list, pending-count) sweep
//	   their own workspace first so no surface can show a question as open past
//	   the point where it can still be answered.
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
// silently, so the warning is mandatory on the wire and the journal entry is
// severity=warn so it surfaces in the default attention filter. Stated in
// docs/guides/harbormaster.mdx and docs/cli/escalation.mdx.
//
// What changed with the second clock is only what the ROW says while that
// happens: the agent is told the same thing it was always told, and the
// question stays open behind it.

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

// escalationWireUnanswered is what the long poll tells an agent whose wait
// window closed on a question that is STILL OPEN. It is deliberately not a row
// status: nothing transitioned, so nothing may claim a transition. EXPIRED is
// reserved for the row, and an agent handed "EXPIRED" for a question an
// operator can still see and answer in the inbox would be reporting a state the
// system is not in.
const escalationWireUnanswered = "UNANSWERED"

// escalationOutcomeContinuedWithWarning names what the agent does when its
// question expires. It is a constant rather than a literal so the wire, the
// journal and the docs cannot drift into describing different policies.
const escalationOutcomeContinuedWithWarning = "continued_with_warning"

// escalationExpiredWarning is the text handed to the agent when the row itself
// reached a terminal state without a decision. It is deliberately imperative:
// an agent reading it should treat the absence of a decision as a fact about
// the world, not as permission.
const escalationExpiredWarning = "No human answered before the deadline. Continue WITHOUT the answer you asked for: " +
	"do not assume approval, state in your result that this question went unanswered, and avoid irreversible actions " +
	"that depended on it."

// escalationUnansweredWarning is the text handed to the agent when ITS wait
// window closed but the question is still open. Same instruction — continue,
// do not assume approval — and one extra fact the agent needs: an answer may
// still arrive, but not to this run. Without that sentence an agent that is
// told "no answer yet" reasonably infers it should poll again or wait, which is
// exactly the wait we just ended.
const escalationUnansweredWarning = "No human answered within this agent's wait window. The question is still open " +
	"for a human, but THIS run will not receive the answer — do not wait for it and do not ask again. Continue " +
	"WITHOUT it: do not assume approval, state in your result that the question went unanswered, and avoid " +
	"irreversible actions that depended on it."

// escalationAgentWait is how long the AGENT waits — the bound on the sidecar's
// long poll, and nothing else. A var, not a const, so tests can shrink it;
// production never reassigns it.
//
// 300 s matches what internal/sidecar/query.go has always waited, and the
// agreement is structural rather than coincidental: the server writes
// deadline_at from this value and RETURNS it, and the sidecar bounds its poll
// on what it was told. Changing this number moves both sides at once.
var escalationAgentWait = 300 * time.Second

// escalationAnswerTTL is how long the QUESTION stays answerable by a human —
// the bound on the inbox item, not on the poll. A var for the same reason.
//
// 7 days is chosen against a person's calendar: a weekend plus a day either
// side. It is three orders of magnitude larger than the agent's window because
// it is measuring something else entirely, and the regression this replaced is
// what a system looks like when those two numbers are the same number.
//
// Note the interaction with crewship_escalation_cap.go: PENDING rows now
// persist for days rather than minutes, so a crew's backlog budget is consumed
// until an operator actually answers. That is the behaviour that cap was
// designed against ("unanswered demands on a person", self-healing as they are
// resolved) — the five-minute window had been quietly refunding it.
var escalationAnswerTTL = 7 * 24 * time.Hour

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
	// deadlineAt is the row's own ANSWER deadline, carried so the expiry
	// describes the deadline this question actually had rather than whatever
	// escalationAnswerTTL happens to be when the sweep runs. A row raised under
	// a different TTL must not be recorded as having expired under the current
	// one.
	deadlineAt string
	// credentialID is the staged PENDING_APPROVAL credential this question was
	// raised to get approved, if any. Carried because a terminal transition MUST
	// dispose of it: the resolve path is the only route that can activate or
	// reject that row, and once the escalation is terminal that route answers
	// 409 forever. Leaving it behind stranded an encrypted secret in the vault
	// AND jammed its name against every later proposal.
	credentialID string
}

// escalationTerminalError describes a refused transition out of a terminal
// state, in the caller's words rather than in the machine's.
func escalationTerminalError(status string) string {
	switch status {
	case escalationStatusExpired:
		// Names the ANSWER deadline specifically. An operator who hits this
		// after the agent's 300 s window would otherwise reasonably assume the
		// two are the same deadline — which is exactly the confusion the second
		// clock exists to remove.
		return "escalation expired — nobody answered before its answer deadline and any staged credential has been discarded; raise a new one"
	case escalationStatusCancelled:
		return "escalation was cancelled — the question was withdrawn before it was decided"
	default:
		return "escalation already resolved"
	}
}

// ─── disposing of a staged credential ─────────────────────────────────────

// disposeStagedCredential retires the PENDING_APPROVAL credential a CREDENTIAL
// escalation staged, when that escalation reaches a terminal state WITHOUT an
// approval. It performs exactly what ResolveEscalation's reject arm performs —
// status REJECTED plus deleted_at — because the outcome is the same outcome:
// the secret was never approved and will never be usable.
//
// This exists because that reject arm used to be the ONLY disposal in the
// system. Expiry and cancellation flipped the escalation and left the credential
// alone, which produced the worst kind of leftover: an encrypted secret in the
// vault that no route could activate (resolve answers 409 on a terminal row) and
// no route could reject (same 409), while the live-name probe in
// createPendingCredential counted it as a name in use — so every later proposal
// of that name was refused and the agent was told to have a human type it in.
// One unanswered question jammed auto-staging for that name permanently.
//
// Best-effort by design and idempotent through its status guard: the escalation
// has already transitioned by the time this runs, so a missing or
// already-disposed credential is a log line, never a failed transition. `reason`
// names which terminal path did it, so the audit trail can tell a withdrawn
// question from an unanswered one.
func (h *QueryHandler) disposeStagedCredential(ctx context.Context, workspaceID, credentialID, reason string) {
	if credentialID == "" {
		return
	}
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := h.db.ExecContext(ctx, `
		UPDATE credentials
		   SET status = 'REJECTED', deleted_at = ?, updated_at = ?
		 WHERE id = ? AND workspace_id = ? AND status IN ('PENDING_APPROVAL', 'REQUESTED') AND deleted_at IS NULL`,
		now, now, credentialID, workspaceID)
	if err != nil {
		h.logger.Error("dispose staged credential", "error", err,
			"credential_id", credentialID, "reason", reason)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// Already approved, already disposed of, or never staged. Not a fault.
		return
	}
	recordCredentialEventBestEffort(ctx, h.db, h.logger, credentialID,
		AuditEventRejected, "", "", map[string]any{
			"rejected_by": "system",
			// The distinction an incident review needs: nobody decided against
			// this secret, the question it was attached to stopped being
			// answerable.
			"disposed_reason": reason,
		})
	h.logger.Info("staged credential disposed with its escalation",
		"credential_id", credentialID, "reason", reason)
}

// ─── the agent's clock: giving up is not a decision ───────────────────────

// markAgentGaveUp stamps agent_gave_up_at on a still-PENDING escalation, then
// reports whether this call was the one that stamped it.
//
// The stamp is the record that the agent stopped waiting — the honest half of
// what the single-deadline design was trying to achieve. It is NOT a status
// transition: the question is still open, still in the inbox, and still
// answerable. Guarded on status='PENDING' and on the column still being NULL so
// that a human who resolved in the same instant wins (no stamp is written for a
// question that WAS answered) and a retried long poll cannot rewrite the
// instant the agent actually left.
func (h *QueryHandler) markAgentGaveUp(ctx context.Context, escalationID string, at time.Time) (bool, error) {
	res, err := h.db.ExecContext(ctx, `
		UPDATE escalations SET agent_gave_up_at = ?
		 WHERE id = ? AND status = ? AND agent_gave_up_at IS NULL`,
		at.UTC().Format(time.RFC3339), escalationID, escalationStatusPending)
	if err != nil {
		return false, fmt.Errorf("mark escalation %s agent-gave-up: %w", escalationID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("mark escalation %s agent-gave-up rows: %w", escalationID, err)
	}
	return n > 0, nil
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

	// The question is terminal, so its staged secret can never be approved and
	// can never be rejected through the resolve path. Dispose of it here, the
	// same way a rejection would, or it becomes an unreachable encrypted row
	// holding its name against every later proposal.
	h.disposeStagedCredential(ctx, row.workspaceID, row.credentialID, "escalation expired unanswered")

	// Drop the row out of "needs action". resolved_by is 'system' and the
	// action is the expiry itself, so the inbox does not claim a human acted.
	inbox.ResolveBySource(ctx, h.db, h.logger, "escalation", row.id, "expired", "")

	// Severity warn, not notice: the create emit is warn because an open
	// question needs attention, and an agent that proceeded without its answer
	// needs it just as much. This is the entry an operator reads to find out
	// that a decision they meant to make was made for them by a clock.
	//
	// row.reason is the same agent-supplied text CreateEscalation redacted and
	// bounded before it went into the journal the first time (#2238) — the
	// sweep reads it back out of the escalations table and writes a SECOND
	// peer.escalation entry, so it needs the identical redact-before-truncate
	// treatment or a credential-shaped reason leaks here instead.
	_, _ = h.journal.Emit(ctx, journal.Entry{
		WorkspaceID: row.workspaceID,
		CrewID:      row.crewID,
		AgentID:     row.fromAgentID,
		Type:        journal.EntryPeerEscalation,
		Severity:    journal.SeverityWarn,
		ActorType:   journal.ActorSystem,
		ActorID:     "escalation_deadline",
		Summary:     fmt.Sprintf("escalation expired unanswered: %s", truncate(inbox.RedactSecrets(row.reason), 140)),
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
// answer_deadline_at, NOT deadline_at. The sweep is the HUMAN's clock: it
// closes questions nobody is going to answer any more. deadline_at only ever
// bounded the agent's poll, and sweeping on it is precisely the regression this
// file was rewritten to remove — it expired the operator's inbox five minutes
// after the agent asked.
//
// datetime() on both sides rather than a bare string comparison: the column is
// TEXT, this package writes RFC3339 ('…T…Z') and other writers on this table
// use SQLite's datetime('now') shape ('… …'). Those two orderings disagree
// lexically — 'T' sorts after ' ' — so a raw `answer_deadline_at <= ?` would
// silently skip rows depending on who wrote them, which is a deadline that
// never fires. It costs the index's range half; the leading status equality is
// what keeps the scan proportional to what is still open rather than to all
// history.
const escalationDuePredicate = `status = ? AND answer_deadline_at IS NOT NULL AND datetime(answer_deadline_at) <= datetime(?)`

// sweepExpiredEscalations expires every past-deadline PENDING escalation and
// returns how many rows THIS call transitioned.
//
// A workspaceID scopes the sweep to one tenant — what the read paths and the
// operator-facing endpoint use, so neither can reach another tenant's rows. An
// empty workspaceID sweeps everything and is the background sweeper's mode.
func (h *QueryHandler) sweepExpiredEscalations(ctx context.Context, workspaceID string) (int, error) {
	now := time.Now().UTC()
	query := `SELECT id, workspace_id, crew_id, chat_id, from_agent_id, reason,
			COALESCE(answer_deadline_at, ''), COALESCE(credential_id, '')
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
		if err := rows.Scan(&e.id, &e.workspaceID, &e.crewID, &e.chatID, &e.fromAgentID, &e.reason,
			&e.deadlineAt, &e.credentialID); err != nil {
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

	var status, crewID, chatID, fromAgentID, fromSlug, credentialID string
	err := h.db.QueryRowContext(r.Context(), `
		SELECT e.status, e.crew_id, e.chat_id, e.from_agent_id, a.slug, COALESCE(e.credential_id, '')
		FROM escalations e
		JOIN agents a ON a.id = e.from_agent_id
		WHERE e.id = ? AND e.workspace_id = ?`,
		escalationID, workspaceID).Scan(&status, &crewID, &chatID, &fromAgentID, &fromSlug, &credentialID)
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

	// Withdrawing the question withdraws the proposal attached to it. Same
	// disposal as an expiry and as a rejection, for the same reason: the resolve
	// path is the only thing that can activate or reject a staged credential,
	// and it now answers 409 for this row forever.
	h.disposeStagedCredential(r.Context(), workspaceID, credentialID, "escalation cancelled by operator")

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
