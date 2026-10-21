package api

import (
	"context"
	"database/sql"
)

func (h *PageHandler) artifactPrincipalMayCreate(ctx context.Context, ws, user string) bool {
	var role string
	var caps sql.NullString
	if err := h.pageAuthReader(ctx).QueryRowContext(ctx, `SELECT role,capabilities FROM workspace_members WHERE workspace_id=? AND user_id=?`, ws, user).Scan(&role, &caps); err != nil {
		return false
	}
	return canRole(role, "create") || HasCapability(resolveCapabilitiesFromRow(caps, role), CapabilityPageCreate)
}
