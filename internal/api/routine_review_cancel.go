package api

import (
	"net/http"
	"time"

	"github.com/crewship-ai/crewship/internal/harbormaster"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/tsformat"
)

func (h *ApprovalsHandler) cancelRoutineDraftReview(w http.ResponseWriter, req *http.Request, workspace, id, actor, reason string) {
	tx, err := h.db.BeginTx(req.Context(), nil)
	if err != nil {
		replyError(w, 500, "cancel failed")
		return
	}
	defer tx.Rollback()
	allowed, err := routineReviewAuthorityIn(req.Context(), tx, workspace, actor, true)
	if err != nil {
		replyError(w, 500, "cancel failed")
		return
	}
	if !allowed {
		replyError(w, 403, "current trusted administrator authority required")
		return
	}
	result, err := tx.ExecContext(req.Context(), `UPDATE approvals_queue SET status='cancelled',decided_by=?,decided_at=?,decision_comment=? WHERE id=? AND workspace_id=? AND kind=? AND status='pending'`, actor, tsformat.Format(time.Now()), reason, id, workspace, harbormaster.KindRoutineDraftReview)
	if err != nil {
		replyError(w, 500, "cancel failed")
		return
	}
	count, err := result.RowsAffected()
	if err != nil {
		replyError(w, 500, "cancel failed")
		return
	}
	if count != 1 {
		replyError(w, 409, "approval is not pending")
		return
	}
	notify, err := appendProjectIssueAudit(req.Context(), tx, h.journal, journal.Entry{WorkspaceID: workspace, Type: journal.EntryApprovalCancelled, ActorType: journal.ActorUser, ActorID: actor, Summary: "Routine draft publication review cancelled", Payload: map[string]any{"approval_id": id, "kind": harbormaster.KindRoutineDraftReview}})
	if err != nil {
		replyError(w, 500, "cancel failed")
		return
	}
	if err = tx.Commit(); err != nil {
		replyError(w, 500, "cancel failed")
		return
	}
	if notify != nil {
		notify()
	}
	writeJSON(w, 200, map[string]string{"status": "cancelled", "cancelled_by": actor})
}
