package api

import "context"

// Private output mutations call this through pageAuthReader again after
// their writer fence. Middleware admission alone cannot preserve authority if
// an account/workspace/membership changes while a request body is uploaded.
// Restricted membership anywhere is the same conservative ceiling as the
// existing HTTP auth middleware; this does not change generic Pages behavior.
func (h *PageHandler) liveContentHumanRole(ctx context.Context, workspace, user string) (string, error) {
	var role string
	err := h.pageAuthReader(ctx).QueryRowContext(ctx, `SELECT m.role FROM workspace_members m JOIN users u ON u.id=m.user_id AND u.suspended_at IS NULL JOIN workspaces w ON w.id=m.workspace_id AND w.deleted_at IS NULL WHERE m.workspace_id=? AND m.user_id=? AND NOT EXISTS(SELECT 1 FROM workspace_members restricted WHERE restricted.user_id=m.user_id AND restricted.access_mode='restricted')`, workspace, user).Scan(&role)
	return role, err
}
