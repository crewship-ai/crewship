package groupchat

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// OpenDirect returns the one durable private conversation for two current
// workspace humans. BEGIN IMMEDIATE serializes both caller orders before the
// canonical pair lookup, creation and membership inserts commit together.
func (s *Store) OpenDirect(ctx context.Context, w, caller, target string) (Conversation, bool, error) {
	if caller == "" || strings.TrimSpace(target) == "" || caller == target {
		return Conversation{}, false, ErrInvalid
	}
	low, high := caller, target
	if low > high {
		low, high = high, low
	}
	var conversation Conversation
	created := false
	err := s.write(ctx, func(q querier) error {
		if err := workspaceMember(ctx, q, w, caller); err != nil {
			return err
		}
		if err := workspaceMember(ctx, q, w, target); err != nil {
			return err
		}
		var id string
		var active bool
		err := q.QueryRowContext(ctx, `SELECT p.conversation_id,c.deleted_at IS NULL FROM workspace_conversation_direct_pairs p JOIN workspace_conversations c ON c.id=p.conversation_id WHERE p.workspace_id=? AND p.user_low_id=? AND p.user_high_id=? AND c.workspace_id=p.workspace_id`, w, low, high).Scan(&id, &active)
		if err == nil && active {
			conversation, err = get(ctx, q, w, caller, id)
			if err != nil {
				return err
			}
			if !conversation.IsDirect {
				return ErrForbidden
			}
			return nil
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if id != "" {
			if _, err = q.ExecContext(ctx, `DELETE FROM workspace_conversation_direct_pairs WHERE workspace_id=? AND user_low_id=? AND user_high_id=?`, w, low, high); err != nil {
				return err
			}
		}
		names := make([]string, 0, 2)
		for _, user := range []string{low, high} {
			var name string
			if err = q.QueryRowContext(ctx, `SELECT COALESCE(NULLIF(TRIM(full_name),''),id) FROM users WHERE id=?`, user).Scan(&name); err != nil {
				return err
			}
			runes := []rune(name)
			if len(runes) > 58 {
				name = string(runes[:57]) + "…"
			}
			names = append(names, name)
		}
		stamp := now()
		conversation = Conversation{ID: newID(), WorkspaceID: w, Kind: "group", IsDirect: true, Title: strings.Join(names, " · "), CreatedBy: caller, CreatedAt: stamp, UpdatedAt: stamp, AccessScope: "participants"}
		if _, err = q.ExecContext(ctx, `INSERT INTO workspace_conversations(id,workspace_id,kind,is_direct,title,created_by,created_at,updated_at) VALUES(?,?,'group',1,?,?,?,?)`, conversation.ID, w, conversation.Title, caller, stamp, stamp); err != nil {
			return err
		}
		for _, user := range []string{low, high} {
			if _, err = q.ExecContext(ctx, `INSERT INTO workspace_conversation_members(conversation_id,user_id,joined_at) VALUES(?,?,?)`, conversation.ID, user, stamp); err != nil {
				return err
			}
		}
		if _, err = q.ExecContext(ctx, `INSERT INTO workspace_conversation_direct_pairs(workspace_id,user_low_id,user_high_id,conversation_id) VALUES(?,?,?,?)`, w, low, high, conversation.ID); err != nil {
			return err
		}
		created = true
		conversation, err = get(ctx, q, w, caller, conversation.ID)
		return err
	})
	return conversation, created, err
}
