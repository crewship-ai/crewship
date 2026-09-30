package pipeline

import (
	"context"
	"database/sql"
)

// CheckDeclaredPageAction validates a server-selected action for the restricted
// workflow adapter. It preserves the ordinary Page dispatch role floor and
// declaration/publication checks; an agent run grant cannot replace them.
type PageActionQuery interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func CheckDeclaredPageAction(ctx context.Context, db PageActionQuery, user, workspace string, action PageActionInvocation) error {
	if db == nil || user == "" || workspace == "" {
		return ErrInvocationAuthorityRevoked
	}
	var role string
	if err := db.QueryRowContext(ctx, `SELECT role FROM workspace_members WHERE user_id=? AND workspace_id=?`, user, workspace).Scan(&role); err != nil {
		return ErrInvocationAuthorityRevoked
	}
	if role != "OWNER" && role != "ADMIN" && role != "MANAGER" {
		return ErrInvocationAuthorityRevoked
	}
	return checkPageActionAuthority(ctx, db, RunInput{WorkspaceID: workspace, InvokingUserID: user, InvocationAuthority: action.Authority()}, role)
}
