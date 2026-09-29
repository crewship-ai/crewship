package chataudience

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Receipt is an admission fence, not a bearer capability. Only authenticated
// host code may issue or carry it. A client-provided receipt grants no access.
// Every use must compare it to a freshly authorized database snapshot.
type Receipt struct {
	UserID         string `json:"user_id"`
	ChatID         string `json:"chat_id"`
	WorkspaceID    string `json:"workspace_id"`
	AgentID        string `json:"agent_id"`
	MemberID       string `json:"member_id"`
	MemberRevision int64  `json:"member_revision"`
	ChatGeneration string `json:"chat_generation"`
	ChatRevision   int64  `json:"chat_revision"`
}

type receiptKey struct{}

// WithReceipt carries server-owned authority through in-process dispatch and
// host IPC. WebSocket request metadata must never populate this context key.
func WithReceipt(ctx context.Context, r *Receipt) context.Context {
	return context.WithValue(ctx, receiptKey{}, r)
}
func ReceiptFromContext(ctx context.Context) *Receipt {
	r, _ := ctx.Value(receiptKey{}).(*Receipt)
	return r
}

// CaptureTrusted authorizes and captures the epoch in one SQL snapshot. An
// expected receipt fences retries: re-admission cannot silently refresh it.
func CaptureTrusted(ctx context.Context, db *sql.DB, chat, user string, expected *Receipt) (*Receipt, error) {
	if db == nil || chat == "" || user == "" {
		return nil, nil
	}
	r := Receipt{UserID: user, ChatID: chat}
	args := []any{user, chat, user}
	args = append(args, Args(user)...)
	err := db.QueryRowContext(ctx, `SELECT c.workspace_id,c.agent_id,wm.id,wm.access_revision,c.authority_generation,c.authority_revision
 FROM chats c JOIN workspace_members wm ON wm.workspace_id=c.workspace_id AND wm.user_id=?
 JOIN workspaces w ON w.id=c.workspace_id AND w.deleted_at IS NULL
 JOIN agents a ON a.id=c.agent_id AND a.workspace_id=c.workspace_id AND a.deleted_at IS NULL
 WHERE c.id=? AND NOT EXISTS(SELECT 1 FROM workspace_members WHERE user_id=? AND access_mode='restricted')
 AND (`+VisibleSQL+`)`, args...).Scan(&r.WorkspaceID, &r.AgentID, &r.MemberID, &r.MemberRevision, &r.ChatGeneration, &r.ChatRevision)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("capture human authority: %w", err)
	}
	if r.ChatGeneration == "" || r.MemberRevision < 1 || r.ChatRevision < 1 || (expected != nil && *expected != r) {
		return nil, nil
	}
	return &r, nil
}
