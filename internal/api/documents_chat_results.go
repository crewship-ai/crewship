package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"

	"github.com/crewship-ai/crewship/internal/chataudience"
)

type DocumentChatResult struct {
	PageID           string `json:"page_id"`
	Type             string `json:"type"`
	PageSlug         string `json:"page_slug"`
	Title            string `json:"title"`
	Revision         int64  `json:"revision"`
	CreatedAt        string `json:"created_at"`
	AgentID          string `json:"agent_id"`
	AgentName        string `json:"agent_name"`
	RunID            string `json:"run_id"`
	OnBehalfOfUserID string `json:"on_behalf_of_user_id"`
}

type DocumentChatResultsResponse struct {
	Results []DocumentChatResult `json:"results"`
}

// ListChatDocumentResults returns durable saved Documents recorded by creation,
// never results inferred from model messages or run completion.
func (h *PageHandler) ListChatDocumentResults(w http.ResponseWriter, r *http.Request) {
	artifactCommentHeaders(w)
	user := UserFromContext(r.Context())
	agent, _ := pageGrantCallerIsAgent(r)
	if user == nil || agent || projectAgentFrom(r.Context()) != nil || r.Context().Value(artifactRunKey{}) != nil {
		replyError(w, 401, "human authentication required")
		return
	}
	workspace, chat := WorkspaceIDFromContext(r.Context()), r.PathValue("chatId")
	tx, err := h.db.BeginTx(r.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		replyInternalError(w, h.logger, "read chat Document results", err)
		return
	}
	defer tx.Rollback()
	ctx := context.WithValue(r.Context(), pageAuthReaderKey{}, tx)
	r = r.WithContext(ctx)
	if err := validateContentHumanCredential(r, tx, "pages:read"); err != nil {
		if errors.Is(err, errContentCredentialScope) {
			replyError(w, 403, "current pages:read scope required")
		} else {
			replyError(w, 401, "current human credential required")
		}
		return
	}
	role, err := h.liveContentHumanRole(ctx, workspace, user.ID)
	if errors.Is(err, sql.ErrNoRows) {
		replyError(w, 404, "chat not found")
		return
	} else if err != nil {
		replyInternalError(w, h.logger, "authorize saved Document results", err)
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
		replyInternalError(w, h.logger, "authorize chat Document results", err)
		return
	}
	if h.documentCall == nil {
		replyError(w, 503, "Documents is unavailable")
		return
	}
	if _, _, err := privatePageOwnershipSQL(ctx, tx, "p"); err != nil {
		replyInternalError(w, h.logger, "authorize private output schema", err)
		return
	}
	// Bound work before per-Page ACL resolution. Inaccessible results are omitted
	// entirely, with no hidden count, title, source ID or inferred placeholder.
	rows, err := tx.QueryContext(ctx, `SELECT p.id,p.slug,p.name,c.revision,c.created_at,c.agent_id,COALESCE(a.name,''),c.run_id,COALESCE(c.human_user_id,''),'document'
 FROM document_creations c JOIN pages p ON p.id=c.page_id AND p.workspace_id=c.workspace_id JOIN documents d ON d.page_id=p.id AND d.workspace_id=p.workspace_id
 LEFT JOIN agents a ON a.id=c.agent_id AND a.workspace_id=p.workspace_id
 WHERE p.workspace_id=? AND c.agent_id=? AND c.source_chat_id=?
 ORDER BY c.created_at DESC,c.revision DESC,p.id LIMIT 100`, workspace, chatAgent, chat)
	if err != nil {
		replyInternalError(w, h.logger, "load chat Document results", err)
		return
	}
	type candidate struct {
		pageID string
		result DocumentChatResult
	}
	candidates := make([]candidate, 0)
	for rows.Next() {
		var v candidate
		if err = rows.Scan(&v.pageID, &v.result.PageSlug, &v.result.Title, &v.result.Revision, &v.result.CreatedAt, &v.result.AgentID, &v.result.AgentName, &v.result.RunID, &v.result.OnBehalfOfUserID, &v.result.Type); err != nil {
			rows.Close()
			replyInternalError(w, h.logger, "read chat Document result", err)
			return
		}
		v.result.PageID = v.pageID
		candidates = append(candidates, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		replyInternalError(w, h.logger, "read chat Document results", err)
		return
	}
	viewer, err := h.loadViewer(ctx, workspace, user.ID)
	if err != nil {
		replyInternalError(w, h.logger, "authorize Document result Pages", err)
		return
	}
	results := make([]DocumentChatResult, 0, 20)
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
			replyInternalError(w, h.logger, "load Document result Page", err)
			return
		}
		visible, err := h.canSeePage(ctx, workspace, rec, nil, viewer)
		if err != nil {
			replyInternalError(w, h.logger, "authorize Document result Page", err)
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
	writeJSON(w, 200, DocumentChatResultsResponse{Results: results})
}
