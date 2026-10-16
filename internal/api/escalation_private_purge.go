package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"time"

	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/inbox"
	"github.com/crewship-ai/crewship/internal/tsformat"
)

type purgedTextSource struct{ id, chat string }

// Retain only the source linkage needed to keep its durable projections
// classified. An absent source must never turn a private journal row public.
// Source-less service and protected credential rows retain their hard purge.
func (h *QueryHandler) purgeEscalationSources(req *http.Request, workspace, crew string) (int64, error) {
	var sources []purgedTextSource
	deleted, err := func() (int64, error) {
		conn, err := h.db.Conn(req.Context())
		if err != nil {
			return 0, err
		}
		defer conn.Close()
		if _, err = conn.ExecContext(req.Context(), "BEGIN IMMEDIATE"); err != nil {
			return 0, err
		}
		defer func() { _, _ = conn.ExecContext(context.WithoutCancel(req.Context()), "ROLLBACK") }()
		rows, err := conn.QueryContext(req.Context(), `SELECT e.id,e.chat_id FROM escalations e WHERE e.workspace_id=? AND e.crew_id=? AND e.deleted_at IS NULL AND `+chataudience.VerifiedTextSourceSQL, workspace, crew)
		if err != nil {
			return 0, err
		}
		for rows.Next() {
			var source purgedTextSource
			if err = rows.Scan(&source.id, &source.chat); err != nil {
				break
			}
			sources = append(sources, source)
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			return 0, err
		}
		if len(sources) > 0 {
			user := UserFromContext(req.Context())
			if user == nil {
				return 0, errDecisionDenied
			}
			var live bool
			err = conn.QueryRowContext(req.Context(), `SELECT EXISTS(SELECT 1 FROM workspace_members wm JOIN users u ON u.id=wm.user_id AND u.suspended_at IS NULL WHERE wm.workspace_id=? AND wm.user_id=? AND wm.role IN ('OWNER','ADMIN') AND wm.access_mode='trusted' AND NOT EXISTS(SELECT 1 FROM workspace_members restricted WHERE restricted.user_id=wm.user_id AND restricted.access_mode='restricted'))`, workspace, user.ID).Scan(&live)
			if err != nil {
				return 0, err
			}
			if !live {
				return 0, errDecisionDenied
			}
		}
		now := tsformat.Format(time.Now())
		res, err := conn.ExecContext(req.Context(), `UPDATE escalations AS e SET deleted_at=?,status='CANCELLED',reason='',context=NULL,metadata=NULL,resolution=NULL,action=NULL,redirect_to=NULL,resolved_by=NULL,resolved_by_user_id=NULL,resolved_at=NULL,answer_operation_key=NULL,answer_operation_hash=NULL,deadline_at=NULL,answer_deadline_at=NULL,agent_gave_up_at=NULL,peer_conversation_id=NULL,credential_id=NULL WHERE e.workspace_id=? AND e.crew_id=? AND e.deleted_at IS NULL AND `+chataudience.VerifiedTextSourceSQL, now, workspace, crew)
		if err != nil {
			return 0, err
		}
		retained, err := res.RowsAffected()
		if err != nil {
			return 0, err
		}
		res, err = conn.ExecContext(req.Context(), `DELETE FROM escalations AS e WHERE e.workspace_id=? AND e.crew_id=? AND NOT (`+chataudience.VerifiedTextSourceSQL+`)`, workspace, crew)
		if err != nil {
			return 0, err
		}
		removed, err := res.RowsAffected()
		if err != nil {
			return 0, err
		}
		if _, err = conn.ExecContext(req.Context(), "COMMIT"); err != nil {
			return 0, err
		}
		return retained + removed, nil
	}()
	if err != nil {
		return 0, err
	}
	for _, source := range sources {
		inbox.ResolveBySource(req.Context(), h.db, h.logger, "escalation", source.id, "cancelled", "")
		h.notifyEscalationWaiter(source.id, escalationResult{Status: escalationStatusCancelled, Warning: "The question was removed by an administrator; continue without assuming approval."})
		broadcastChatEntityRefsInvalidation(req.Context(), h.hub, source.chat)
	}
	return deleted, nil
}

func replyPurgeSourceError(w http.ResponseWriter, err error) bool {
	if errors.Is(err, errDecisionDenied) || errors.Is(err, sql.ErrNoRows) {
		replyError(w, http.StatusForbidden, "admin role required")
		return true
	}
	return false
}
