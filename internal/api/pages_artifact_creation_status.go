package api

import (
	"database/sql"
	"errors"
	"net/http"
	"time"
)

// Resolve the live run on the server; the browser must never turn a streaming
// flag, client chat session or agent-supplied run ID into publication authority.
func (h *PageHandler) ArtifactCreationStatus(w http.ResponseWriter, r *http.Request) {
	artifactCommentHeaders(w)
	user := UserFromContext(r.Context())
	agent, _ := pageGrantCallerIsAgent(r)
	if user == nil || agent {
		replyError(w, 401, "human authentication required")
		return
	}
	workspace, chat := WorkspaceIDFromContext(r.Context()), r.PathValue("chatId")
	var own int
	if err := h.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM chats WHERE id=? AND workspace_id=? AND created_by=?`, chat, workspace, user.ID).Scan(&own); err != nil {
		replyInternalError(w, h.logger, "read artifact creation chat", err)
		return
	}
	if own != 1 {
		replyError(w, 404, "chat not found")
		return
	}
	result := map[string]any{"run_id": "", "can_authorize": false, "authorized": false, "remaining": 0, "expires_at": "", "revoked": false, "policy_allowed": false}
	if !h.artifactRequestChatPrivate(r.Context(), chat, user.ID) || !h.artifactPrincipalMayCreate(r.Context(), workspace, user.ID) {
		writeJSON(w, 200, result)
		return
	}
	var a artifactRunAuthority
	var expires string
	err := h.db.QueryRowContext(r.Context(), `SELECT run_id,workspace_id,agent_id,crew_id,human_user_id,chat_id,message_id,expires_at FROM artifact_run_principals p WHERE workspace_id=? AND chat_id=? AND human_user_id=? AND NOT EXISTS(SELECT 1 FROM journal_entries j WHERE j.workspace_id=p.workspace_id AND j.agent_id=p.agent_id AND j.trace_id=p.run_id AND j.entry_type IN ('run.completed','run.failed','run.cancelled','run.timeout','run.recovered_stop')) ORDER BY expires_at DESC LIMIT 1`, workspace, chat, user.ID).Scan(&a.RunID, &a.WorkspaceID, &a.AgentID, &a.CrewID, &a.HumanUserID, &a.ChatID, &a.MessageID, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		writeJSON(w, 200, result)
		return
	}
	if err != nil {
		replyInternalError(w, h.logger, "read live artifact creation run", err)
		return
	}
	if h.checkArtifactRunPrincipal(r.Context(), &a) != nil || h.checkDocumentRunPageScope(r.Context(), "", &a) != nil {
		writeJSON(w, 200, result)
		return
	}
	policyAllowed := h.artifactAgentPolicyAllows(r.Context(), workspace, a.CrewID)
	result["policy_allowed"] = policyAllowed
	result["run_id"], result["expires_at"], result["can_authorize"] = a.RunID, expires, policyAllowed
	var deadline string
	var revoked, consumed sql.NullString
	err = h.db.QueryRowContext(r.Context(), `SELECT expires_at,revoked_at,consumed_at FROM artifact_creation_authorizations WHERE run_id=? AND authorized_by_user_id=?`, a.RunID, user.ID).Scan(&deadline, &revoked, &consumed)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		replyInternalError(w, h.logger, "read creation permission", err)
		return
	}
	if err == nil {
		limit, parseErr := time.Parse(time.RFC3339Nano, deadline)
		valid := parseErr == nil && time.Now().Before(limit) && !revoked.Valid
		result["revoked"], result["authorized"], result["can_authorize"] = revoked.Valid, valid, valid && policyAllowed
		if valid && !consumed.Valid {
			result["remaining"] = 1
		}
	}
	writeJSON(w, 200, result)
}
