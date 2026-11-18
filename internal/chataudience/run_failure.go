package chataudience

import (
	"context"
	"strings"
)

// The target on a work failure is always a single human, and the source may
// become private or disappear after projection. Apply the live fence before
// listing, counting or returning the item, with indexed canonical source lookups.
var RunFailureInboxReadSQL = `CASE
	WHEN inbox_items.kind='failed_run' AND json_extract(CASE WHEN json_valid(inbox_items.payload_json) THEN inbox_items.payload_json ELSE '{}' END,'$.subkind')='work_run_failure'
	THEN inbox_items.target_user_id=? AND (` + runFailureSourceSQL("inbox_items.source_id", "inbox_items.workspace_id", "inbox_items.target_user_id") + `)
	ELSE 1 END`

func CanReadRunFailure(ctx context.Context, q Queryer, runID, workspaceID, userID string) (bool, error) {
	var allowed bool
	err := q.QueryRowContext(ctx, `WITH notice(run_id,workspace_id,user_id) AS (VALUES(?,?,?))
		SELECT `+runFailureSourceSQL("notice.run_id", "notice.workspace_id", "notice.user_id")+` FROM notice`,
		runID, workspaceID, userID).Scan(&allowed)
	return allowed, err
}

// Expressions supplied by our two SQL call sites refer to either an inbox row
// or bound parameters. No caller-provided value is interpolated into SQL.
func runFailureSourceSQL(run, workspace, user string) string {
	visible := strings.ReplaceAll(visibleChatToMemberSQL, "?", "u.id")
	return `EXISTS (
	SELECT 1 FROM work_attempts wa JOIN work_items wi ON wi.id=wa.work_id
	JOIN agents ag ON ag.id=wi.agent_id AND ag.workspace_id=wi.workspace_id AND ag.deleted_at IS NULL
	JOIN workspaces ws ON ws.id=wi.workspace_id AND ws.deleted_at IS NULL
	JOIN workspace_members wm ON wm.workspace_id=wi.workspace_id AND wm.user_id=` + user + ` AND wm.access_mode='trusted'
	JOIN users u ON u.id=wm.user_id AND u.suspended_at IS NULL
	LEFT JOIN assignments a ON wi.source='assignment' AND a.id=wi.domain_id
	 AND a.workspace_id=wi.workspace_id AND a.assigned_to_id=wi.agent_id
	LEFT JOIN execution_origins o ON o.id=a.chat_origin_id AND o.workspace_id=wi.workspace_id
	WHERE wa.run_id=` + run + ` AND wi.workspace_id=` + workspace + `
	 AND wa.run_status IN ('FAILED','TIMEOUT') AND wa.ended_at IS NOT NULL
	 AND NOT EXISTS (SELECT 1 FROM workspace_members rm WHERE rm.user_id=u.id AND rm.access_mode='restricted')
	 AND (
	  (wi.source='chat' AND wi.domain_kind='host_chat_turn' AND wi.authorized_scope='chat:send'
	   AND wi.authorized_by_user_id=u.id
	   AND EXISTS (SELECT 1 FROM chats c WHERE c.id=wi.session_id AND c.workspace_id=wi.workspace_id AND c.agent_id=wi.agent_id
	    AND (` + visible + `)))
	  OR
	  (wi.source='assignment' AND wi.domain_kind='assignment' AND wi.authorized_scope='assignment:'||wi.domain_id
	   AND ( (COALESCE(a.chat_origin_id,'')='' AND a.created_by_user_id=u.id AND wi.authorized_by_user_id=u.id)
	   OR (json_extract(o.human_authority,'$.user_id')=u.id AND json_extract(o.human_authority,'$.workspace_id')=wi.workspace_id
	    AND json_extract(o.human_authority,'$.member_id')=wm.id
	    AND CASE COALESCE(json_extract(o.human_authority,'$.kind'),'')
	     WHEN '' THEN EXISTS (SELECT 1 FROM chats c JOIN agents root ON root.id=c.agent_id AND root.workspace_id=c.workspace_id AND root.deleted_at IS NULL
	      WHERE c.id=o.chat_id AND c.workspace_id=wi.workspace_id
	       AND c.id=json_extract(o.human_authority,'$.chat_id') AND c.agent_id=json_extract(o.human_authority,'$.agent_id')
	       AND c.authority_generation=json_extract(o.human_authority,'$.chat_generation')
	       AND c.authority_revision=json_extract(o.human_authority,'$.chat_revision')
	       AND wm.access_revision=json_extract(o.human_authority,'$.member_revision')
	       AND COALESCE(NULLIF(json_extract(o.human_authority,'$.user_authority_revision'),''),CAST(u.authority_revision AS TEXT))=CAST(u.authority_revision AS TEXT)
	       AND (` + visible + `))
	     WHEN 'web_issue' THEN EXISTS (SELECT 1 FROM missions m JOIN crews cr ON cr.id=m.crew_id AND cr.workspace_id=m.workspace_id AND cr.deleted_at IS NULL
	      WHERE m.id=json_extract(o.human_authority,'$.resource_id') AND m.workspace_id=wi.workspace_id AND cr.id=json_extract(o.human_authority,'$.crew_id')
	       AND wm.web_execution_revision=json_extract(o.human_authority,'$.member_revision'))
	     WHEN 'web_recurring' THEN EXISTS (SELECT 1 FROM recurring_issues ri JOIN crews cr ON cr.id=ri.crew_id AND cr.workspace_id=ri.workspace_id AND cr.deleted_at IS NULL
	      WHERE ri.id=json_extract(o.human_authority,'$.resource_id') AND ri.workspace_id=wi.workspace_id AND cr.id=json_extract(o.human_authority,'$.crew_id')
	       AND wm.web_execution_revision=json_extract(o.human_authority,'$.member_revision'))
	     ELSE 0 END))
	  )
	 )
	)`
}
