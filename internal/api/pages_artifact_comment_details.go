package api

import (
	"context"
	"database/sql"
	"net/http"
)

var artifactReactionEmojis = []string{"👍", "❤️", "👀", "✅"}

type artifactCommentReaction struct {
	Emoji       string `json:"emoji"`
	Count       int    `json:"count"`
	ReactedByMe bool   `json:"reacted_by_me"`
}

func artifactReactionValid(emoji string) bool {
	for _, allowed := range artifactReactionEmojis {
		if emoji == allowed {
			return true
		}
	}
	return false
}
func (h *PageHandler) recordArtifactCommentInitial(ctx context.Context, tx *sql.Tx, page, thread, comment, body, user, agent, run, stamp string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO page_artifact_comment_history(id,page_id,thread_id,comment_id,version,body,action,actor_user_id,actor_agent_id,actor_run_id,created_at) VALUES(?,?,?,?,1,?,'create',NULLIF(?,''),NULLIF(?,''),NULLIF(?,''),?)`, generateCUID(), page, thread, comment, body, user, agent, run, stamp)
	return err
}
func (h *PageHandler) artifactCommentDetails(ctx context.Context, page string, thread *artifactThreadWire) error {
	q := h.pageAuthReader(ctx)
	userID := ""
	if user := UserFromContext(ctx); user != nil {
		userID = user.ID
	}
	_, agent := ctx.Value(artifactRunKey{}).(*artifactRunAuthority)
	canComment := false
	if userID != "" && !agent {
		var role string
		if q.QueryRowContext(ctx, `SELECT m.role FROM workspace_members m JOIN users u ON u.id=m.user_id AND u.suspended_at IS NULL WHERE m.workspace_id=? AND m.user_id=?`, WorkspaceIDFromContext(ctx), userID).Scan(&role) == nil {
			rec, err := h.loadBoardRequestPage(ctx, page)
			if err == nil {
				canComment = h.mayComment(ctx, WorkspaceIDFromContext(ctx), userID, role, rec)
			}
		}
	}
	if agent {
		userID = ""
	}
	counts := map[string]map[string]artifactCommentReaction{}
	rows, err := q.QueryContext(ctx, `SELECT r.comment_id,r.emoji,COUNT(*),MAX(CASE WHEN r.user_id=? THEN 1 ELSE 0 END) FROM page_artifact_comment_reactions r JOIN page_artifact_comments c ON c.id=r.comment_id WHERE c.thread_id=? AND c.deleted_at IS NULL GROUP BY r.comment_id,r.emoji`, userID, thread.ID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id string
		var reaction artifactCommentReaction
		if err = rows.Scan(&id, &reaction.Emoji, &reaction.Count, &reaction.ReactedByMe); err != nil {
			break
		}
		if counts[id] == nil {
			counts[id] = map[string]artifactCommentReaction{}
		}
		counts[id][reaction.Emoji] = reaction
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	for i := range thread.Comments {
		comment := &thread.Comments[i]
		own := canComment && comment.AuthorUserID == userID && comment.AuthorAgentID == "" && comment.AuthorRunID == ""
		comment.CanViewHistory = own
		comment.CanEdit = own && comment.DeletedAt == ""
		comment.CanDelete = comment.CanEdit
		comment.CanReact = canComment && comment.DeletedAt == ""
		comment.Reactions = []artifactCommentReaction{}
		for _, emoji := range artifactReactionEmojis {
			if reaction, exists := counts[comment.ID][emoji]; exists {
				comment.Reactions = append(comment.Reactions, reaction)
			}
		}
		if comment.DeletedAt != "" {
			comment.Body = ""
		}
	}
	return nil
}
func humanArtifactCommentCaller(w http.ResponseWriter, r *http.Request) bool {
	_, scopedAgent := r.Context().Value(artifactRunKey{}).(*artifactRunAuthority)
	agent, _ := pageGrantCallerIsAgent(r)
	if scopedAgent || agent || projectAgentFrom(r.Context()) != nil || UserFromContext(r.Context()) == nil {
		replyError(w, 401, "human authentication required")
		return false
	}
	return true
}
