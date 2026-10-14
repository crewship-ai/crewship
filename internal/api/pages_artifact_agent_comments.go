package api

import (
	"context"
	"database/sql"
	"net/http"
	"strconv"
)

type artifactCommentAuthorityKey struct{}

func (h *PageHandler) agentMayComment(ctx context.Context, workspace string, rec *pageRecord, agent string) bool {
	grants, err := h.agentGrants(ctx, workspace, rec, agent)
	if err != nil {
		return false
	}
	for _, grant := range grants {
		if grant.Level == pageGrantComment || grant.Level == pageGrantWrite {
			return true
		}
	}
	return false
}
func (h *PageHandler) artifactAgentCommentsPage(w http.ResponseWriter, r *http.Request, a *artifactRunAuthority, write bool) (*pageRecord, bool) {
	if err := h.checkArtifactRunPrincipal(r.Context(), a); err != nil {
		replyError(w, 403, err.Error())
		return nil, false
	}
	rec, err := h.loadPage(r.Context(), a.WorkspaceID, r.PathValue("slug"))
	if err != nil {
		replyError(w, 404, "artifact not found")
		return nil, false
	}
	if !rec.HasArtifact || rec.HasBoard {
		replyError(w, 404, "artifact not found")
		return nil, false
	}
	if err := h.checkArtifactRunDelegation(r.Context(), rec.ID, a); err != nil {
		replyError(w, 403, err.Error())
		return nil, false
	}
	if write && !h.artifactAgentPolicyAllows(r.Context(), a.WorkspaceID, a.CrewID) {
		artifactCommentError(w, 403, "artifact_policy_disabled", "Agent artifact discussion is disabled for this crew")
		return nil, false
	}
	var role string
	if h.pageAuthReader(r.Context()).QueryRowContext(r.Context(), `SELECT role FROM workspace_members WHERE workspace_id=? AND user_id=?`, a.WorkspaceID, a.HumanUserID).Scan(&role) != nil {
		replyError(w, 403, "human membership required")
		return nil, false
	}
	if !h.mayComment(r.Context(), a.WorkspaceID, a.HumanUserID, role, rec) || !h.agentMayComment(r.Context(), a.WorkspaceID, rec, a.AgentID) {
		replyError(w, 403, "explicit agent grant and live human comment authority required")
		return nil, false
	}
	return rec, true
}
func (h *PageHandler) agentCommentRequest(w http.ResponseWriter, r *http.Request) (*http.Request, bool) {
	r, ok := h.artifactAgentRequest(w, r)
	if !ok {
		return r, false
	}
	a := r.Context().Value(artifactRunKey{}).(*artifactRunAuthority)
	return r.WithContext(context.WithValue(r.Context(), artifactCommentAuthorityKey{}, a)), true
}
func (h *PageHandler) ListArtifactThreadsAgent(w http.ResponseWriter, r *http.Request) {
	artifactCommentHeaders(w)
	r, ok := h.agentCommentRequest(w, r)
	if !ok {
		return
	}
	limit := 5
	if value := r.URL.Query().Get("limit"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > 10 {
			replyError(w, 400, "limit must be 1 to 10")
			return
		}
		limit = parsed
	}
	revision := int64(0)
	if value := r.URL.Query().Get("revision"); value != "" {
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err != nil || parsed < 1 {
			replyError(w, 400, "revision must be positive")
			return
		}
		revision = parsed
	}
	cursor := r.URL.Query().Get("cursor")
	if cursor != "" && !boardMutationValid(cursor) {
		replyError(w, 400, "invalid discussion cursor")
		return
	}
	tx, err := h.db.BeginTx(r.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		h.boardError(w, err)
		return
	}
	defer tx.Rollback()
	r = r.WithContext(context.WithValue(r.Context(), pageAuthReaderKey{}, tx))
	rec, ok := h.artifactCommentsPage(w, r, false)
	if !ok {
		return
	}
	rows, err := tx.QueryContext(r.Context(), `SELECT id FROM page_artifact_threads WHERE page_id=? AND (?=0 OR revision=?) AND (?='' OR id<?) ORDER BY id DESC LIMIT ?`, rec.ID, revision, revision, cursor, cursor, limit+1)
	if err != nil {
		h.boardError(w, err)
		return
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			break
		}
		ids = append(ids, id)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		h.boardError(w, err)
		return
	}
	next := ""
	if len(ids) > limit {
		ids = ids[:limit]
		next = ids[len(ids)-1]
	}
	threads := []artifactThreadWire{}
	for _, id := range ids {
		thread, err := h.loadArtifactThread(r.Context(), rec.ID, id)
		if err != nil {
			h.boardError(w, err)
			return
		}
		threads = append(threads, *thread)
	}
	writeJSON(w, 200, map[string]any{"threads": threads, "can_comment": true, "can_resolve": false, "next_cursor": next})
}

func (h *PageHandler) ReplyArtifactThreadAgent(w http.ResponseWriter, r *http.Request) {
	if r, ok := h.agentCommentRequest(w, r); ok {
		h.ReplyArtifactThread(w, r)
	}
}
