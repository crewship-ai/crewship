package notifyroute

import (
	"context"
	"database/sql"
	"errors"

	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/runauthority"
)

type runFailureNoticeSource struct {
	WorkspaceID, UserID, ChatID, AgentID, CrewID string
}

// resolveRunFailureNotice resolves the original authorized requester and checks that
// the failed attempt still owns this generation and has a current audience.
func resolveRunFailureNotice(ctx context.Context, db runauthority.Queryer, run string) (runFailureNoticeSource, bool, error) {
	var source runFailureNoticeSource
	var kind, origin string
	err := db.QueryRowContext(ctx, `
SELECT wi.workspace_id,wi.authorized_by_user_id,wi.session_id,wi.agent_id,COALESCE(wi.crew_id,''),wi.source,COALESCE(a.chat_origin_id,'')
FROM work_attempts wa JOIN work_items wi ON wi.id=wa.work_id AND wi.generation=wa.generation
JOIN agents ag ON ag.id=wi.agent_id AND ag.workspace_id=wi.workspace_id AND ag.deleted_at IS NULL
JOIN workspaces ws ON ws.id=wi.workspace_id AND ws.deleted_at IS NULL
LEFT JOIN assignments a ON wi.source='assignment' AND a.id=wi.domain_id
 AND a.workspace_id=wi.workspace_id AND a.assigned_to_id=wi.agent_id
WHERE wa.run_id=? AND wa.run_status='FAILED' AND wa.ended_at IS NOT NULL AND wi.state IN ('failed','needs_reconciliation')
 AND ((wi.source='chat' AND wi.domain_kind='host_chat_turn' AND wi.authorized_scope='chat:send')
  OR (wi.source='assignment' AND wi.domain_kind='assignment' AND wi.authorized_scope='assignment:'||wi.domain_id
   AND (COALESCE(a.chat_origin_id,'')<>'' OR (wi.authorized_by_user_id<>'' AND a.created_by_user_id=wi.authorized_by_user_id))))`, run).
		Scan(&source.WorkspaceID, &source.UserID, &source.ChatID, &source.AgentID, &source.CrewID, &kind, &origin)
	if errors.Is(err, sql.ErrNoRows) {
		return source, false, nil
	}
	if err != nil {
		return source, false, err
	}
	if origin != "" {
		// A lead's delegation can carry verified human provenance even when
		// the assignment itself was authored by an agent.
		authority, err := runauthority.ValidateOriginReceipt(ctx, db, origin, source.WorkspaceID)
		if errors.Is(err, runauthority.ErrRequesterStale) || errors.Is(err, runauthority.ErrUnavailable) || errors.Is(err, runauthority.ErrIdentityMismatch) {
			return source, false, nil
		}
		if err != nil {
			return source, false, err
		}
		source.UserID = authority.HumanAuthority.UserID
		return source, true, nil
	}
	if kind == "chat" {
		var allowed bool
		args := append([]any{source.UserID, source.ChatID, source.WorkspaceID}, chataudience.Args(source.UserID)...)
		err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM chats c
			JOIN users u ON u.id=? AND u.suspended_at IS NULL
			WHERE c.id=? AND c.workspace_id=? AND (`+chataudience.VisibleSQL+`))`, args...).Scan(&allowed)
		return source, allowed, err
	}
	var allowed bool
	err = db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM workspace_members wm
		JOIN users u ON u.id=wm.user_id AND u.suspended_at IS NULL
		WHERE wm.workspace_id=? AND wm.user_id=?)`, source.WorkspaceID, source.UserID).Scan(&allowed)
	return source, allowed, err
}
