package api

import (
	"net/http"

	"github.com/crewship-ai/crewship/internal/missionactivity"
)

func (h *InboxHandler) takeOverIssue(w http.ResponseWriter, r *http.Request, wsID, missionID, ident, crewID, cardID, userID, payload string, receipt inboxActReceipt) {
	ctx := r.Context()
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		internalError(w, r, h.logger, "take over: begin", err)
		return
	}
	defer tx.Rollback()
	targets, err := holdIssueWorkTx(ctx, tx, missionID, userID, "Taken over from Inbox", receipt.ActedAt)
	if err == nil && receipt.SessionID != "" {
		_, err = tx.ExecContext(ctx, `UPDATE issue_agent_sessions SET state='idle',updated_at=? WHERE id=? AND state='awaiting_input'`, receipt.ActedAt, receipt.SessionID)
	}
	if err == nil {
		receipt.EventID = generateCUID()
		written, eventErr := missionactivity.EmitTx(ctx, tx, missionactivity.Entry{ID: receipt.EventID, MissionID: missionID, ActorType: "user", ActorID: userID, Action: "inbox_acted", Details: "take_over on NEEDS_HUMAN card " + cardID + "; automatic work paused", SourceKind: "work_operation", SourceID: "inbox:" + cardID})
		err = eventErr
		receipt.Seq = written.Seq
	}
	if err == nil {
		err = resolveInboxCardWithReceipt(ctx, tx, cardID, userID, inboxActTakeOver, payload, receipt)
	}
	if err != nil {
		internalError(w, r, h.logger, "take over: persist", err)
		return
	}
	if err = tx.Commit(); err != nil {
		internalError(w, r, h.logger, "take over: commit", err)
		return
	}
	// Cooperative cancellation is durable even if hard termination is unavailable.
	if len(targets) > 0 {
		issues := NewIssueHandler(h.db, h.hub, nil, h.logger)
		issues.hardStopTargets(ctx, wsID, ident, targets)
	}
	if h.hub != nil {
		broadcastWorkspaceEvent(h.hub, wsID, "inbox.updated", map[string]string{"id": cardID, "state": "resolved"})
		broadcastWorkspaceEvent(h.hub, wsID, "issue.updated", map[string]string{"id": missionID, "identifier": ident, "crew_id": crewID})
	}
	writeJSON(w, 200, map[string]any{"id": cardID, "state": "resolved", "action": inboxActTakeOver, "receipt": receipt})
}
