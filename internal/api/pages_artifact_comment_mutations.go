package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/crewship-ai/crewship/internal/tsformat"
)

const maxArtifactCommentHistory = 50

type artifactCommentChange struct {
	BaseVersion *int64  `json:"base_version"`
	MutationID  string  `json:"mutation_id"`
	Body        *string `json:"body,omitempty"`
	Active      *bool   `json:"active,omitempty"`
}

func (h *PageHandler) EditArtifactComment(w http.ResponseWriter, r *http.Request) {
	h.changeArtifactComment(w, r, "edit")
}
func (h *PageHandler) DeleteArtifactComment(w http.ResponseWriter, r *http.Request) {
	h.changeArtifactComment(w, r, "delete")
}
func (h *PageHandler) ReactArtifactComment(w http.ResponseWriter, r *http.Request) {
	h.changeArtifactComment(w, r, "react")
}

func (h *PageHandler) changeArtifactComment(w http.ResponseWriter, r *http.Request, operation string) {
	artifactCommentHeaders(w)
	if !humanArtifactCommentCaller(w, r) {
		return
	}
	rec, ok := h.artifactCommentsPage(w, r, true)
	if !ok {
		return
	}
	raw, ok := readCapped(w, r, 6*maxArtifactCommentBytes+2048, "artifact comment")
	if !ok {
		return
	}
	if !utf8.Valid(raw) {
		replyError(w, 400, "UTF-8 comment JSON required")
		return
	}
	var req artifactCommentChange
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&req) != nil || decoder.Decode(new(any)) != io.EOF || req.BaseVersion == nil || *req.BaseVersion < 1 || !boardMutationValid(req.MutationID) {
		replyError(w, 400, "base_version and mutation_id required")
		return
	}
	emoji := r.PathValue("emoji")
	if operation == "edit" {
		if req.Body == nil || strings.TrimSpace(*req.Body) == "" || len(*req.Body) > maxArtifactCommentBytes || !utf8.ValidString(*req.Body) || req.Active != nil {
			replyError(w, 422, "nonempty comment body within 4 KiB required")
			return
		}
	} else if operation == "react" {
		if req.Active == nil || req.Body != nil || !artifactReactionValid(emoji) {
			replyError(w, 422, "supported emoji and active boolean required")
			return
		}
	} else if req.Body != nil || req.Active != nil {
		replyError(w, 400, "delete accepts only base_version and mutation_id")
		return
	}
	thread, comment := r.PathValue("threadId"), r.PathValue("commentId")
	digest := boardFingerprint("artifact-comment-change", []any{operation, thread, comment, emoji, req})
	user := UserFromContext(r.Context()).ID
	ws := WorkspaceIDFromContext(r.Context())
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		h.boardError(w, err)
		return
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(r.Context(), `UPDATE pages SET updated_at=updated_at WHERE id=? AND workspace_id=?`, rec.ID, ws); err != nil {
		h.boardError(w, err)
		return
	}
	ctx := context.WithValue(r.Context(), pageAuthReaderKey{}, tx)
	var role string
	if tx.QueryRowContext(ctx, `SELECT m.role FROM workspace_members m JOIN users u ON u.id=m.user_id AND u.suspended_at IS NULL WHERE m.workspace_id=? AND m.user_id=?`, ws, user).Scan(&role) != nil {
		replyError(w, 403, "active membership required")
		return
	}
	ctx = context.WithValue(ctx, ctxRole, role)
	r = r.WithContext(ctx)
	rec, ok = h.artifactCommentsPage(w, r, true)
	if !ok {
		return
	}
	var author, agent, run string
	var deleted sql.NullString
	var version, current int64
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(c.author_user_id,''),COALESCE(c.author_agent_id,''),COALESCE(c.author_run_id,''),c.deleted_at,c.version,t.version FROM page_artifact_comments c JOIN page_artifact_threads t ON t.id=c.thread_id WHERE c.id=? AND c.thread_id=? AND t.page_id=?`, comment, thread, rec.ID).Scan(&author, &agent, &run, &deleted, &version, &current); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			replyError(w, 404, "comment not found")
		} else {
			h.boardError(w, err)
		}
		return
	}
	if operation != "react" && (author != user || agent != "" || run != "") {
		replyError(w, 403, "only the actual human author may edit or delete this comment")
		return
	}
	var priorDigest, priorUser, priorAgent, priorRun, priorThread string
	var applied int64
	err = tx.QueryRowContext(ctx, `SELECT request_digest,COALESCE(actor_user_id,''),COALESCE(actor_agent_id,''),COALESCE(actor_run_id,''),thread_id,result_version FROM page_artifact_comment_mutations WHERE page_id=? AND mutation_id=?`, rec.ID, req.MutationID).Scan(&priorDigest, &priorUser, &priorAgent, &priorRun, &priorThread, &applied)
	if err == nil {
		if priorDigest != digest || priorUser != user || priorAgent != "" || priorRun != "" {
			artifactCommentError(w, 409, "mutation_conflict", "mutation_id already used for another action")
			return
		}
		result, err := h.loadArtifactThread(ctx, rec.ID, priorThread)
		if err != nil {
			h.boardError(w, err)
			return
		}
		result.AppliedVersion = &applied
		writeJSON(w, 200, result)
		return
	}
	if !errors.Is(err, sql.ErrNoRows) {
		h.boardError(w, err)
		return
	}
	if deleted.Valid {
		artifactCommentError(w, 409, "comment_deleted", "comment was deleted")
		return
	}
	if current != *req.BaseVersion {
		artifactCommentError(w, 409, "stale_thread", "thread changed; review before changing comment")
		return
	}
	now := h.clock.Now().UTC()
	stamp := tsformat.Format(now)
	if err := compactDiscussionReceipts(r.Context(), tx, rec.ID, false, now); err != nil {
		h.boardError(w, err)
		return
	}
	var total, recent int
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(CASE WHEN operation!='create' THEN 1 ELSE 0 END),0),COALESCE(SUM(created_at>?),0) FROM page_artifact_comment_mutations WHERE page_id=?`, tsformat.Format(now.Add(-time.Hour)), rec.ID).Scan(&total, &recent); err != nil {
		h.boardError(w, err)
		return
	}
	if total >= maxArtifactCommentMutations {
		artifactCommentError(w, 409, "comment_mutation_limit", "artifact discussion mutation limit reached")
		return
	}
	if recent >= maxArtifactCommentHourlyMutations {
		w.Header().Set("Retry-After", "3600")
		artifactCommentError(w, 429, "rate_limited", "artifact discussion mutation rate limit reached")
		return
	}
	if operation == "react" {
		if *req.Active {
			_, err = tx.ExecContext(ctx, `INSERT INTO page_artifact_comment_reactions(comment_id,user_id,emoji,created_at) VALUES(?,?,?,?) ON CONFLICT(comment_id,user_id,emoji) DO NOTHING`, comment, user, emoji, stamp)
		} else {
			_, err = tx.ExecContext(ctx, `DELETE FROM page_artifact_comment_reactions WHERE comment_id=? AND user_id=? AND emoji=?`, comment, user, emoji)
		}
	} else {
		var history int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM page_artifact_comment_history WHERE comment_id=?`, comment).Scan(&history); err != nil {
			h.boardError(w, err)
			return
		}
		if history >= maxArtifactCommentHistory || (operation == "edit" && history >= maxArtifactCommentHistory-1) {
			artifactCommentError(w, 409, "comment_history_limit", "comment body history limit reached")
			return
		}
		body := ""
		if operation == "edit" {
			body = *req.Body
			_, err = tx.ExecContext(ctx, `UPDATE page_artifact_comments SET body=?,version=version+1,edited_at=?,edited_by_user_id=? WHERE id=?`, body, stamp, user, comment)
		} else {
			_, err = tx.ExecContext(ctx, `UPDATE page_artifact_comments SET body='[deleted]',version=version+1,deleted_at=?,deleted_by_user_id=? WHERE id=?`, stamp, user, comment)
			if err == nil {
				_, err = tx.ExecContext(ctx, `DELETE FROM page_artifact_comment_reactions WHERE comment_id=?`, comment)
			}
		}
		if err == nil {
			_, err = tx.ExecContext(ctx, `INSERT INTO page_artifact_comment_history(id,page_id,thread_id,comment_id,version,body,action,actor_user_id,created_at) VALUES(?,?,?,?,?,?,?,?,?)`, generateCUID(), rec.ID, thread, comment, version+1, body, operation, user, stamp)
		}
	}
	if err != nil {
		h.boardError(w, err)
		return
	}
	applied = current + 1
	if _, err = tx.ExecContext(ctx, `UPDATE page_artifact_threads SET version=?,updated_at=? WHERE id=? AND page_id=? AND version=?`, applied, stamp, thread, rec.ID, current); err != nil {
		h.boardError(w, err)
		return
	}
	var reaction any
	var active any
	if operation == "react" {
		reaction = emoji
		active = *req.Active
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO page_artifact_comment_mutations(id,page_id,thread_id,comment_id,mutation_id,request_digest,actor_user_id,operation,result_version,created_at,reaction_emoji,reaction_active) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, generateCUID(), rec.ID, thread, comment, req.MutationID, digest, user, operation, applied, stamp, reaction, active); err != nil {
		h.boardError(w, err)
		return
	}
	result, err := h.loadArtifactThread(ctx, rec.ID, thread)
	if err != nil {
		h.boardError(w, err)
		return
	}
	result.AppliedVersion = &applied
	if err = tx.Commit(); err != nil {
		h.boardError(w, err)
		return
	}
	broadcastWorkspaceEvent(h.hub, ws, "page.updated", map[string]any{"page_id": rec.ID})
	writeJSON(w, 200, result)
}

func (h *PageHandler) GetArtifactCommentHistory(w http.ResponseWriter, r *http.Request) {
	artifactCommentHeaders(w)
	if !humanArtifactCommentCaller(w, r) {
		return
	}
	tx, err := h.db.BeginTx(r.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		h.boardError(w, err)
		return
	}
	defer tx.Rollback()
	r = r.WithContext(context.WithValue(r.Context(), pageAuthReaderKey{}, tx))
	var role string
	if tx.QueryRowContext(r.Context(), `SELECT m.role FROM workspace_members m JOIN users u ON u.id=m.user_id AND u.suspended_at IS NULL WHERE m.workspace_id=? AND m.user_id=?`, WorkspaceIDFromContext(r.Context()), UserFromContext(r.Context()).ID).Scan(&role) != nil {
		replyError(w, 403, "active membership required")
		return
	}
	r = r.WithContext(context.WithValue(r.Context(), ctxRole, role))
	rec, ok := h.artifactCommentsPage(w, r, true)
	if !ok {
		return
	}
	var author, agent, run string
	if tx.QueryRowContext(r.Context(), `SELECT COALESCE(c.author_user_id,''),COALESCE(c.author_agent_id,''),COALESCE(c.author_run_id,'') FROM page_artifact_comments c JOIN page_artifact_threads t ON t.id=c.thread_id WHERE c.id=? AND c.thread_id=? AND t.page_id=?`, r.PathValue("commentId"), r.PathValue("threadId"), rec.ID).Scan(&author, &agent, &run) != nil {
		replyError(w, 404, "comment not found")
		return
	}
	if author != UserFromContext(r.Context()).ID || agent != "" || run != "" {
		replyError(w, 403, "comment history is available only to its actual human author")
		return
	}
	rows, err := tx.QueryContext(r.Context(), `SELECT version,body,action,COALESCE(actor_user_id,''),created_at FROM page_artifact_comment_history WHERE page_id=? AND thread_id=? AND comment_id=? ORDER BY version DESC LIMIT ?`, rec.ID, r.PathValue("threadId"), r.PathValue("commentId"), maxArtifactCommentHistory)
	if err != nil {
		h.boardError(w, err)
		return
	}
	defer rows.Close()
	history := []map[string]any{}
	for rows.Next() {
		var version int64
		var body, action, actor, stamp string
		if err = rows.Scan(&version, &body, &action, &actor, &stamp); err != nil {
			h.boardError(w, err)
			return
		}
		history = append(history, map[string]any{"version": version, "body": body, "action": action, "actor_user_id": actor, "created_at": stamp})
	}
	if rows.Err() != nil {
		h.boardError(w, rows.Err())
		return
	}
	writeJSON(w, 200, map[string]any{"history": history})
}
