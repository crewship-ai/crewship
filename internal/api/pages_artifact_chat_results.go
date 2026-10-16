package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"

	"github.com/crewship-ai/crewship/internal/chataudience"
)

type artifactChatResult struct {
	PageSlug         string `json:"page_slug"`
	Title            string `json:"title"`
	Revision         int64  `json:"revision"`
	CreatedAt        string `json:"created_at"`
	AgentID          string `json:"agent_id"`
	AgentName        string `json:"agent_name"`
	RunID            string `json:"run_id"`
	OnBehalfOfUserID string `json:"on_behalf_of_user_id"`
}

// ListChatArtifactResults returns publications, never claims parsed from agent
// messages or run journal payloads. source_chat_id was recorded from the verified
// publication principal in the revision transaction. A successful request's exact
// result is the only fallback for publications predating that durable linkage.
func (h *PageHandler) ListChatArtifactResults(w http.ResponseWriter, r *http.Request) {
	artifactCommentHeaders(w)
	user := UserFromContext(r.Context())
	agent, _ := pageGrantCallerIsAgent(r)
	if user == nil || agent || projectAgentFrom(r.Context()) != nil {
		replyError(w, 401, "human authentication required")
		return
	}
	workspace, chat := WorkspaceIDFromContext(r.Context()), r.PathValue("chatId")
	tx, err := h.db.BeginTx(r.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		replyInternalError(w, h.logger, "read chat artifact results", err)
		return
	}
	defer tx.Rollback()
	ctx := context.WithValue(r.Context(), pageAuthReaderKey{}, tx)
	var role string
	if err = tx.QueryRowContext(ctx, `SELECT wm.role FROM workspace_members wm JOIN users human ON human.id=wm.user_id AND human.suspended_at IS NULL JOIN workspaces w ON w.id=wm.workspace_id AND w.deleted_at IS NULL WHERE wm.workspace_id=? AND wm.user_id=?`, workspace, user.ID).Scan(&role); err != nil {
		replyError(w, 404, "chat not found")
		return
	}
	ctx = context.WithValue(ctx, ctxRole, role)
	args := []any{chat, workspace}
	args = append(args, chataudience.Args(user.ID)...)
	var chatAgent string
	if err = tx.QueryRowContext(ctx, `SELECT c.agent_id FROM chats c WHERE c.id=? AND c.workspace_id=? AND (`+chataudience.VisibleSQL+`)`, args...).Scan(&chatAgent); errors.Is(err, sql.ErrNoRows) {
		replyError(w, 404, "chat not found")
		return
	} else if err != nil {
		replyInternalError(w, h.logger, "authorize chat artifact results", err)
		return
	}
	// Bound work before per-Page ACL resolution. Inaccessible results are omitted
	// entirely, with no hidden count, title, source ID or inferred placeholder.
	rows, err := tx.QueryContext(ctx, `SELECT p.id,p.slug,p.name,v.revision,v.created_at,v.author_agent_id,COALESCE(a.name,''),v.author_run_id,COALESCE(v.author_user_id,'')
 FROM page_artifact_revisions v JOIN pages p ON p.id=v.page_id JOIN page_artifacts art ON art.page_id=p.id
 LEFT JOIN agents a ON a.id=v.author_agent_id AND a.workspace_id=p.workspace_id
 WHERE p.workspace_id=? AND v.author_agent_id=? AND v.author_run_id IS NOT NULL AND (
 v.source_chat_id=?
 OR (v.source_chat_id IS NULL AND EXISTS(SELECT 1 FROM page_artifact_requests req WHERE req.page_id=p.id AND req.chat_id=? AND req.agent_id=v.author_agent_id AND req.run_id=v.author_run_id AND req.status='succeeded' AND req.result_revision=v.revision)))
 ORDER BY v.created_at DESC,v.revision DESC,p.id LIMIT 100`, workspace, chatAgent, chat, chat)
	if err != nil {
		replyInternalError(w, h.logger, "load chat artifact results", err)
		return
	}
	type candidate struct {
		pageID string
		result artifactChatResult
	}
	candidates := make([]candidate, 0)
	for rows.Next() {
		var v candidate
		if err = rows.Scan(&v.pageID, &v.result.PageSlug, &v.result.Title, &v.result.Revision, &v.result.CreatedAt, &v.result.AgentID, &v.result.AgentName, &v.result.RunID, &v.result.OnBehalfOfUserID); err != nil {
			rows.Close()
			replyInternalError(w, h.logger, "read chat artifact result", err)
			return
		}
		candidates = append(candidates, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		replyInternalError(w, h.logger, "read chat artifact results", err)
		return
	}
	viewer, err := h.loadViewer(ctx, workspace, user.ID)
	if err != nil {
		replyInternalError(w, h.logger, "authorize artifact result Pages", err)
		return
	}
	results := make([]artifactChatResult, 0, 20)
	seen := map[string]bool{}
	for _, candidate := range candidates {
		if seen[candidate.pageID] {
			continue
		}
		seen[candidate.pageID] = true
		rec, err := h.loadPage(ctx, workspace, candidate.result.PageSlug)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			replyInternalError(w, h.logger, "load artifact result Page", err)
			return
		}
		visible, err := h.canSeePage(ctx, workspace, rec, nil, viewer)
		if err != nil {
			replyInternalError(w, h.logger, "authorize artifact result Page", err)
			return
		}
		if !visible {
			continue
		}
		results = append(results, candidate.result)
		if len(results) == 20 {
			break
		}
	}
	writeJSON(w, 200, map[string]any{"results": results})
}

func artifactSourceChatID(r *http.Request) string {
	if authority, ok := r.Context().Value(artifactRunKey{}).(*artifactRunAuthority); ok && authority.AgentID != "" {
		return authority.ChatID
	}
	return ""
}
