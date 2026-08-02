package api

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

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
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := h.db.ExecContext(r.Context(), `
		UPDATE keeper_requests
		   SET decision = ?, reason = ?, decided_at = ?
		 WHERE id = ?`, decision, reason, now, reqID); err != nil {
		replyInternalError(w, h.logger, "keeper resolve: record decision", err)
		return
	}

	actorID := ""
	if user := UserFromContext(r.Context()); user != nil {
		actorID = user.ID
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
