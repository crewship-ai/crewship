package api

import (
	"context"
	"database/sql"
)

// Publication uses the routine's existing MANAGER+ floor; queue rejection uses
// the existing OWNER/ADMIN decision floor. Neither trusts a cached role or a
// trusted membership while the account is restricted in another workspace.
func routineReviewAuthorityIn(ctx context.Context, tx *sql.Tx, workspace, actor string, adminOnly bool) (bool, error) {
	var allowed bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM workspace_members wm JOIN users u ON u.id=wm.user_id JOIN workspaces w ON w.id=wm.workspace_id WHERE w.deleted_at IS NULL AND wm.workspace_id=? AND wm.user_id=? AND wm.access_mode='trusted' AND wm.role IN ('OWNER','ADMIN','MANAGER') AND (?=0 OR wm.role IN ('OWNER','ADMIN')) AND u.suspended_at IS NULL AND NOT EXISTS(SELECT 1 FROM workspace_members guest WHERE guest.user_id=u.id AND guest.access_mode='restricted'))`, workspace, actor, adminOnly).Scan(&allowed)
	return allowed, err
}
