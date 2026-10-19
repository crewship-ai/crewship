package api

import (
	"context"
	"fmt"
)

// privateGrantedHumanRead resolves only an explicit named-human read row. It
// does not supply intrinsic custody/OWNER authority or consult ordinary grants.
// No reader or writer calls it until the central ACL integration is qualified.
func (h *PageHandler) privateGrantedHumanRead(ctx context.Context, wsID string, rec *pageRecord, userID string) (bool, error) {
	if rec == nil || !rec.PrivateAudience || rec.ID == "" || rec.OwnerUserID == "" || rec.OwnerCrewID != "" || wsID == "" || userID == "" {
		return false, nil
	}
	var allowed bool
	// Membership identities are historical TEXT in the share row. Bind both the
	// current membership ID and human ID so removal/rejoin cannot revive a share.
	// Read current Page custody here rather than trusting an earlier record.
	err := h.pageAuthReader(ctx).QueryRowContext(ctx, `SELECT EXISTS(
 SELECT 1 FROM page_private_readers share
 JOIN pages p ON p.id=share.page_id AND p.workspace_id=share.workspace_id
 JOIN workspaces w ON w.id=p.workspace_id AND w.deleted_at IS NULL
 JOIN users recipient ON recipient.id=share.recipient_user_id AND recipient.suspended_at IS NULL
 JOIN workspace_members recipient_member ON recipient_member.id=share.recipient_member_id
  AND recipient_member.user_id=recipient.id AND recipient_member.workspace_id=p.workspace_id
  AND recipient_member.access_mode='trusted'
 JOIN users issuer ON issuer.id=share.issuer_user_id AND issuer.suspended_at IS NULL
 JOIN workspace_members issuer_member ON issuer_member.id=share.issuer_member_id
  AND issuer_member.user_id=issuer.id AND issuer_member.workspace_id=p.workspace_id
  AND issuer_member.access_mode='trusted'
 WHERE p.id=? AND p.workspace_id=? AND share.recipient_user_id=?
  AND p.private_audience=1 AND p.owner_user_id IS NOT NULL AND p.owner_user_id<>'' AND p.owner_crew_id IS NULL
  AND share.revoked_at IS NULL
  AND (issuer.id=p.owner_user_id OR issuer_member.role='OWNER')
  AND NOT EXISTS(SELECT 1 FROM workspace_members ceiling WHERE ceiling.user_id=recipient.id AND ceiling.access_mode='restricted')
  AND NOT EXISTS(SELECT 1 FROM workspace_members ceiling WHERE ceiling.user_id=issuer.id AND ceiling.access_mode='restricted')
)`, rec.ID, wsID, userID).Scan(&allowed)
	if err != nil {
		return false, fmt.Errorf("resolve explicit private human read: %w", err)
	}
	return allowed, nil
}
