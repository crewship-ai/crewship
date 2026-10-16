package api

import "context"

func (h *PageHandler) boardCommentDetails(ctx context.Context, page string, thread *boardThreadWire) error {
	q := h.pageAuthReader(ctx)
	userID := ""
	if user := UserFromContext(ctx); user != nil {
		userID = user.ID
	}
	_, agent := ctx.Value(artifactRunKey{}).(*artifactRunAuthority)
	canComment := false
	if userID != "" && !agent {
		if role, err := h.liveContentHumanRole(ctx, WorkspaceIDFromContext(ctx), userID); err == nil {
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
	rows, err := q.QueryContext(ctx, `SELECT r.comment_id,r.emoji,COUNT(*),MAX(CASE WHEN r.user_id=? THEN 1 ELSE 0 END) FROM page_board_comment_reactions r JOIN page_board_comments c ON c.id=r.comment_id WHERE c.thread_id=? AND c.deleted_at IS NULL GROUP BY r.comment_id,r.emoji`, userID, thread.ID)
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
