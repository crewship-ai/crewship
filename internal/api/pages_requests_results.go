package api

import (
	"context"
	"database/sql"
	"net/http"
)

// ResolvePageRequestResult is the only link resolver for generic private
// completion notices. Stored inbox pointers never carry access or a cached slug.
func (h *PageHandler) ResolvePageRequestResult(w http.ResponseWriter, r *http.Request) {
	artifactCommentHeaders(w)
	user := UserFromContext(r.Context())
	agent, _ := pageGrantCallerIsAgent(r)
	if user == nil || agent || projectAgentFrom(r.Context()) != nil {
		replyError(w, 401, "human authentication required")
		return
	}
	kind, id := r.PathValue("kind"), r.PathValue("requestId")
	if kind != "artifact" && kind != "board" {
		replyError(w, 404, "result unavailable")
		return
	}
	tx, err := h.db.BeginTx(r.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		replyInternalError(w, h.logger, "resolve request result", err)
		return
	}
	defer tx.Rollback()
	ctx := context.WithValue(r.Context(), pageAuthReaderKey{}, tx)
	workspace := WorkspaceIDFromContext(ctx)
	var page, role string
	var revision sql.NullInt64
	query := `SELECT r.page_id,CASE WHEN r.status='succeeded' THEN r.result_revision END,m.role FROM page_artifact_requests r JOIN pages p ON p.id=r.page_id JOIN users u ON u.id=r.human_user_id AND u.suspended_at IS NULL JOIN workspace_members m ON m.workspace_id=p.workspace_id AND m.user_id=u.id JOIN workspaces w ON w.id=m.workspace_id AND w.deleted_at IS NULL WHERE r.id=? AND p.workspace_id=? AND r.human_user_id=? AND NOT EXISTS(SELECT 1 FROM workspace_members WHERE user_id=u.id AND access_mode='restricted')`
	if kind == "board" {
		query = `SELECT COALESCE(r.result_page_id,r.board_page_id,''),r.result_revision,m.role FROM page_board_requests r JOIN users u ON u.id=r.human_user_id AND u.suspended_at IS NULL JOIN workspace_members m ON m.workspace_id=r.workspace_id AND m.user_id=u.id JOIN workspaces w ON w.id=m.workspace_id AND w.deleted_at IS NULL WHERE r.id=? AND r.workspace_id=? AND r.human_user_id=? AND NOT EXISTS(SELECT 1 FROM workspace_members WHERE user_id=u.id AND access_mode='restricted')`
	}
	if tx.QueryRowContext(ctx, query, id, workspace, user.ID).Scan(&page, &revision, &role) != nil || page == "" {
		replyError(w, 404, "result unavailable")
		return
	}
	ctx = context.WithValue(ctx, ctxRole, role)
	var slug string
	if tx.QueryRowContext(ctx, `SELECT slug FROM pages WHERE id=? AND workspace_id=?`, page, workspace).Scan(&slug) != nil {
		replyError(w, 404, "result unavailable")
		return
	}
	rec, err := h.loadPage(ctx, workspace, slug)
	if err != nil {
		replyError(w, 404, "result unavailable")
		return
	}
	viewer, err := h.loadViewer(ctx, workspace, user.ID)
	if err != nil {
		replyError(w, 404, "result unavailable")
		return
	}
	visible, err := h.canSeePage(ctx, workspace, rec, nil, viewer)
	if err != nil || !visible {
		replyError(w, 404, "result unavailable")
		return
	}
	out := map[string]any{"page_slug": slug}
	if revision.Valid {
		var present int
		if tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM page_artifact_revisions WHERE page_id=? AND revision=?`, page, revision.Int64).Scan(&present) == nil && present == 1 {
			out["revision"] = revision.Int64
		}
	}
	writeJSON(w, 200, out)
}
