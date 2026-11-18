package notifyroute

import (
	"context"
	"database/sql"
	"errors"

	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/runauthority"
)

type runFailureNoticeSource struct {
	WorkspaceID, UserID string
}

// resolveRunFailureNotice resolves the original authorized requester and checks that
// the ended attempt still has a current audience.
func resolveRunFailureNotice(ctx context.Context, db runauthority.Queryer, run string) (runFailureNoticeSource, bool, error) {
	var source runFailureNoticeSource
	var origin string
	err := db.QueryRowContext(ctx, `
SELECT wi.workspace_id,wi.authorized_by_user_id,COALESCE(a.chat_origin_id,'')
FROM work_attempts wa JOIN work_items wi ON wi.id=wa.work_id
JOIN agents ag ON ag.id=wi.agent_id AND ag.workspace_id=wi.workspace_id AND ag.deleted_at IS NULL
JOIN workspaces ws ON ws.id=wi.workspace_id AND ws.deleted_at IS NULL
LEFT JOIN assignments a ON wi.source='assignment' AND a.id=wi.domain_id
 AND a.workspace_id=wi.workspace_id AND a.assigned_to_id=wi.agent_id
WHERE wa.run_id=? AND wa.run_status IN ('FAILED','TIMEOUT') AND wa.ended_at IS NOT NULL
 AND ((wi.source='chat' AND wi.domain_kind='host_chat_turn' AND wi.authorized_scope='chat:send')
  OR (wi.source='assignment' AND wi.domain_kind='assignment' AND wi.authorized_scope='assignment:'||wi.domain_id
   AND (COALESCE(a.chat_origin_id,'')<>'' OR (wi.authorized_by_user_id<>'' AND a.created_by_user_id=wi.authorized_by_user_id))))`, run).
		Scan(&source.WorkspaceID, &source.UserID, &origin)
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
	}
	allowed, err := chataudience.CanReadRunFailure(ctx, db, run, source.WorkspaceID, source.UserID)
	return source, allowed, err
}
