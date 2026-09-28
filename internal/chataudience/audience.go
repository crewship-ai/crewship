// Package chataudience holds the read audience of an agent chat. The same SQL
// rule is used before pagination, before proxying a transcript, and when a
// WebSocket session is subscribed or reauthorized.
package chataudience

import (
	"context"
	"database/sql"
	"errors"
)

// VisibleSQL is a predicate over `chats c`. Its three arguments are the
// requesting user ID. Workspace membership is checked here as well, so a
// handler cannot turn a private chat into a cross-workspace read by omitting
// its usual route-level membership gate.
//
// An unattributed row must never become a private human conversation visible
// to every workspace member. Known machine work is available to workspace
// operators while its dedicated resource authority is being integrated;
// unknown/legacy provenance fails closed on these chat surfaces.
const VisibleSQL = `EXISTS (
	SELECT 1 FROM workspace_members wm
	WHERE wm.workspace_id = c.workspace_id AND wm.user_id = ?
	  AND (
	    (c.created_by = ? AND c.visibility IN ('private','group'))
	    OR (c.visibility = 'group' AND EXISTS (
	      SELECT 1 FROM chat_participants p
	      WHERE p.chat_id = c.id AND p.user_id = ?
	    ))
	    OR (wm.role IN ('OWNER','ADMIN') AND c.created_by IS NOT NULL
	      AND c.visibility IN ('private','group'))
	    OR (c.created_by IS NULL AND wm.role IN ('OWNER','ADMIN')
	      AND (c.mode = 'MISSION' OR c.origin IN ('ROUTINE','CRON','WEBHOOK','AGENT')))
	  )
)`

// Args supplies VisibleSQL's identity parameters in their fixed order.
func Args(userID string) []any { return []any{userID, userID, userID} }

// CanRead checks the current audience for one chat. A missing chat, removed
// workspace member, or removed group participant is a definitive denial.
func CanRead(ctx context.Context, db *sql.DB, chatID, userID string) (bool, error) {
	return canRead(ctx, db, chatID, userID, "")
}

// CanReadInWorkspace also binds an HTTP request's selected workspace.
func CanReadInWorkspace(ctx context.Context, db *sql.DB, chatID, userID, workspaceID string) (bool, error) {
	if workspaceID == "" {
		return false, nil
	}
	return canRead(ctx, db, chatID, userID, workspaceID)
}

func canRead(ctx context.Context, db *sql.DB, chatID, userID, workspaceID string) (bool, error) {
	if db == nil || chatID == "" || userID == "" {
		return false, nil
	}
	query := `SELECT 1 FROM chats c WHERE c.id = ?`
	args := []any{chatID}
	if workspaceID != "" {
		query += ` AND c.workspace_id = ?`
		args = append(args, workspaceID)
	}
	query += ` AND (` + VisibleSQL + `)`
	args = append(args, Args(userID)...)
	var one int
	err := db.QueryRowContext(ctx, query, args...).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}
