package api

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/inbox"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/keeper"
	"github.com/crewship-ai/crewship/internal/keeper/governance"
)

// HandleResolve is POST /api/v1/admin/keeper/requests/{requestId}/resolve —
// a person ruling on an escalation.
//
// This was the missing half of the tier system. L4 exists so that a human
// confirms every production-credential read, and there was no way for a human
// to confirm one: the inbox card said "missing on the server: a keeper request
// has no resolve endpoint yet" and sent the operator to a terminal, where
// `inbox resolve` marked the NOTIFICATION read without recording a verdict
// against the request it notified about. The decision the whole tier system
// defers to a person was the one decision the product could not accept.
//
// Four security properties, each pinned by test:
//
//   - roleManage. OWNER/ADMIN, the same gate the inbox item is addressed by
//     (keeperInboxTargetRole). Audience and authority are one fact.
//   - Workspace-scoped. keeper_requests carries no workspace of its own, so the
//     scope comes from the credential it names. An admin holding a request id
//     from another tenant gets a 404 and learns nothing from the difference.
//   - Settled once. A request that already has a terminal decision is a 409, not
//     a silent overwrite: this row is what the eval harness reads as ground
//     truth, and a verdict that can be rewritten is not one.
//   - Four-eyes. Where the workspace or the tier requires a second approver, the
//     owner of the requesting agent cannot approve it. Mirrors the escalation
//     path (escalation_handler.go), including auditing the blocked attempt —
//     someone approving their own agent's production request is exactly the
//     event this control exists to catch, so it must leave a trail.
//
// ledgerActorID names who to record. For a reference adjudication that is the
// model, kept alongside the operator who ran it in the reason line — the model
// made the judgement, the person carried it out, and the corpus cares about the
// first.
func ledgerActorID(userID, adjudicator string) string {
	if adjudicator != "" {
		return adjudicator
	}
	return userID
}

func (h *KeeperHandler) HandleResolve(w http.ResponseWriter, r *http.Request) {
	if !requireRole(w, r, "manage") {
		return
	}
	wsID := WorkspaceIDFromContext(r.Context())
	if wsID == "" {
		writeProblem(w, r, http.StatusBadRequest, "workspace is required")
		return
	}
	reqID := r.PathValue("requestId")
	if strings.TrimSpace(reqID) == "" {
		writeProblem(w, r, http.StatusBadRequest, "request id is required")
		return
	}

	var body struct {
		Decision string `json:"decision"`
		Reason   string `json:"reason"`
		// Adjudicator names an AI reference model when one ruled instead of a
		// person. Empty means a person did, which stays the default and the
		// normal case.
		//
		// It exists so the corpus can say where its labels came from. An AI
		// adjudication is a useful label — it answers "can a small model match a
		// frontier one?", which scales where human clicks do not — but it is not
		// ground truth, and recorded as one it would make the eval report
		// agreement with a person about a number that measured agreement with a
		// model.
		Adjudicator string `json:"adjudicator"`
	}
	if err := readJSON(r, &body); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "invalid JSON body")
		return
	}

	// A closed vocabulary, and deliberately without ESCALATE: this endpoint IS
	// the escalation being answered, so "escalate" would be a request that
	// forwards to itself. Unknown values are refused rather than stored — the row
	// becomes ground truth for the eval harness, and a verdict nothing recognises
	// would be counted as neither approval nor refusal.
	decision := strings.ToUpper(strings.TrimSpace(body.Decision))
	if decision != string(keeper.DecisionAllow) && decision != string(keeper.DecisionDeny) {
		writeProblem(w, r, http.StatusBadRequest,
			"decision must be ALLOW or DENY — this endpoint is the escalation being answered")
		return
	}

	// Scoped through the credential, because keeper_requests has no workspace of
	// its own. Also collects what the four-eyes check and the audit line need, so
	// the decision path reads the row once.
	var (
		current      sql.NullString
		agentID      string
		crewID       string
		credentialID string
		secLevel     int
		initiator    sql.NullString
	)
	err := h.db.QueryRowContext(r.Context(), `
		SELECT kr.decision, kr.requesting_agent_id, kr.requesting_crew_id,
		       kr.credential_id, COALESCE(c.security_level, 1), a.created_by_user_id
		  FROM keeper_requests kr
		  JOIN credentials c ON c.id = kr.credential_id
		  LEFT JOIN agents a ON a.id = kr.requesting_agent_id
		 WHERE kr.id = ? AND c.workspace_id = ?`,
		reqID, wsID).Scan(&current, &agentID, &crewID, &credentialID, &secLevel, &initiator)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeProblem(w, r, http.StatusNotFound, "keeper request not found")
			return
		}
		replyInternalError(w, h.logger, "keeper resolve: load request", err)
		return
	}

	// Only an escalation is awaiting a person. An ALLOW or DENY has already been
	// acted on downstream — the credential was delivered or refused — so letting
	// it be rewritten would change an audit record about something that already
	// happened.
	//
	// This is the EARLY answer, for the message: it can name the verdict already
	// stored, which "no row changed" cannot. It is not the guard — the read
	// happens before the transaction opens, so two callers can both see ESCALATE
	// here and both proceed. The guard is the conditional UPDATE below.
	if current.Valid && current.String != string(keeper.DecisionEscalate) {
		writeProblem(w, r, http.StatusConflict,
			fmt.Sprintf("this request is already settled as %s", current.String))
		return
	}

	gov := governance.Resolve(r.Context(), h.db, h.logger, wsID)
	tier := keeper.SecurityLevel(secLevel).Tier()
	if decision == string(keeper.DecisionAllow) && (gov.RequireSecondApprover || tier.SecondApprover) {
		forcedBy := "workspace policy"
		if tier.SecondApprover {
			forcedBy = "critical credential tier"
		}
		approverID := ""
		if user := UserFromContext(r.Context()); user != nil {
			approverID = user.ID
		}
		if approverID != "" && initiator.Valid && approverID == initiator.String {
			// Audited, not merely refused. A user approving a credential
			// escalation their own agent raised is the event a four-eyes control
			// exists to catch; the successful path is journaled downstream, and
			// without this the blocked attempt would leave no trace at all.
			_, _ = h.journal.Emit(r.Context(), journal.Entry{
				WorkspaceID: wsID,
				CrewID:      crewID,
				Type:        journal.EntryKeeperDecision,
				Severity:    journal.SeverityError,
				ActorType:   journal.ActorUser,
				ActorID:     approverID,
				Summary: fmt.Sprintf(
					"blocked self-approval: user tried to approve a credential escalation raised by agent %s they own (%s)",
					agentID, forcedBy),
			})
			writeProblem(w, r, http.StatusForbidden, fmt.Sprintf(
				"%s requires a second approver: this escalation was raised by an agent you own, so somebody else must confirm it",
				forcedBy))
			return
		}
	}

	reason := strings.TrimSpace(body.Reason)
	if reason == "" {
		reason = "resolved by an operator"
	}
	actorID := ""
	if user := UserFromContext(r.Context()); user != nil {
		actorID = user.ID
	}
	// Who is recorded as deciding. The authenticated user still owns the ACTION —
	// they ran the command and the four-eyes rule still applies to them — but the
	// ledger records the reference model as the adjudicator, because that is what
	// the corpus needs to know.
	actorType, adjudicator := keeperActorUser, strings.TrimSpace(body.Adjudicator)
	if adjudicator != "" {
		actorType = keeperActorReference
	}

	// The ruling lands in TWO places and both are load-bearing, so they commit
	// together or not at all (the pattern issue #1247 established for hire
	// decisions, for the same reason: a terminal decision must never leave its
	// projection behind).
	//
	//   keeper_requests — what the credential path reads to release the secret.
	//   inbox_items     — what the OPERATOR sees, and what eval.LoadCorpus reads
	//                     as ground truth (humanInboxSQL: state='resolved', a
	//                     named resolved_by_user_id, resolved_action in
	//                     approved/denied).
	//
	// Settling only the first would fail twice over and both failures are quiet:
	// the item the person just decided stays sitting in their inbox, and their
	// ruling is invisible to the eval — which then goes on scoring candidates
	// against keeper_requests.decision, i.e. the previous model's own verdict.
	// That is precisely the defect the ground-truth work exists to remove, so it
	// would have been reintroduced by the endpoint meant to fix it.
	now := time.Now().UTC().Format(time.RFC3339)
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		replyInternalError(w, h.logger, "keeper resolve: begin", err)
		return
	}
	defer func() { _ = tx.Rollback() }()

	// `AND decision = 'ESCALATE'` is the real settled-once guard, and it has to be
	// here rather than in the read above: that read happens before the transaction
	// opens, so two callers can both observe ESCALATE, both pass, and both write —
	// the later silently overwriting a verdict somebody already gave and leaving
	// the ledger with two terminal entries for one decision.
	//
	// Making the condition part of the write turns the race into a lost update
	// that reports itself: exactly one statement changes a row, and the rest see
	// zero and roll back. That matters more here than in most places — this row is
	// what eval.LoadCorpus reads as ground truth, and a verdict that can be
	// rewritten is not one.
	res, err := tx.ExecContext(r.Context(), `
		UPDATE keeper_requests
		   SET decision = ?, reason = ?, decided_at = ?
		 WHERE id = ? AND decision = ?`,
		decision, reason, now, reqID, string(keeper.DecisionEscalate))
	if err != nil {
		replyInternalError(w, h.logger, "keeper resolve: record decision", err)
		return
	}
	switch n, err := res.RowsAffected(); {
	case err != nil:
		replyInternalError(w, h.logger, "keeper resolve: confirm decision", err)
		return
	case n == 0:
		// Somebody settled it between the read and this write. The rollback is the
		// deferred one; nothing has been committed, so the inbox row and the ledger
		// stay as they were.
		writeProblem(w, r, http.StatusConflict,
			"this request was settled by somebody else while you were deciding it")
		return
	}

	// The inbox vocabulary is approved/denied, not ALLOW/DENY, and the mapping is
	// not cosmetic: humanInboxSQL reads only those two as verdicts and treats
	// everything else in the action vocabulary (retried, dismissed, archived…) as
	// "I cleared my inbox". A near-miss here yields no label rather than a wrong
	// one, which is the safer failure but still a silent one.
	action := "approved"
	if decision == string(keeper.DecisionDeny) {
		action = "denied"
	}
	if _, err := inbox.ResolveBySourceTx(r.Context(), tx,
		string(inbox.KindEscalation), reqID, action, actorID); err != nil {
		replyInternalError(w, h.logger, "keeper resolve: resolve inbox item", err)
		return
	}

	// The third write, and the one that says WHO. keeper_requests is UPDATEd in
	// place, so without a ledger entry the audit trail records a person's verdict
	// as though the model had made it — `keeper history` showed PENDING → ESCALATE
	// and stopped, while keeper_requests already said DENY. #1369 built this
	// ledger so the projection and the history could never disagree; this is the
	// transition where the disagreement costs the most.
	//
	// Same transaction as the two above: a history entry for a decision that was
	// not applied would be worse than a missing one.
	if err := appendKeeperTransitionTx(r.Context(), tx, keeperTransition{
		RequestID:    reqID,
		WorkspaceID:  wsID,
		State:        decision,
		RequestType:  string(keeper.RequestTypeAccess),
		AgentID:      agentID,
		CrewID:       crewID,
		CredentialID: credentialID,
		Reason:       reason,
		ActorType:    actorType,
		ActorID:      ledgerActorID(actorID, adjudicator),
	}); err != nil {
		replyInternalError(w, h.logger, "keeper resolve: append ledger transition", err)
		return
	}

	if err := tx.Commit(); err != nil {
		replyInternalError(w, h.logger, "keeper resolve: commit", err)
		return
	}
	_, _ = h.journal.Emit(r.Context(), journal.Entry{
		WorkspaceID: wsID,
		CrewID:      crewID,
		Type:        journal.EntryKeeperDecision,
		Severity:    journal.SeverityInfo,
		ActorType:   journal.ActorUser,
		ActorID:     actorID,
		Summary: fmt.Sprintf("credential escalation resolved as %s for agent %s: %s",
			decision, agentID, reason),
	})

	writeJSON(w, http.StatusOK, map[string]any{
		"request_id": reqID,
		"decision":   decision,
		"reason":     reason,
		"decided_at": now,
	})
}
