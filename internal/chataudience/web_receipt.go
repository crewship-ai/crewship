package chataudience

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// CaptureWebTrustedQuery captures an authenticated web/API admission, distinct
// from chat visibility. Callers must obtain user from their authentication
// context; neither a resource creator nor a tool body is an admission source.
func CaptureWebTrustedQuery(ctx context.Context, q Queryer, kind, resource, user, workspace string, expected *Receipt) (*Receipt, error) {
	if q == nil || resource == "" || user == "" || workspace == "" {
		return nil, nil
	}
	table := ""
	switch kind {
	case "web_issue":
		table = "missions"
	case "web_recurring":
		table = "recurring_issues"
	default:
		return nil, nil
	}
	// Match the existing chat restriction: a globally restricted account
	// cannot gain a broad execution grant through another workspace.
	r := Receipt{Kind: kind, ResourceID: resource, UserID: user, WorkspaceID: workspace}
	err := q.QueryRowContext(ctx, `SELECT wm.id,wm.web_execution_revision,x.crew_id FROM `+table+` x
 JOIN workspaces w ON w.id=x.workspace_id AND w.deleted_at IS NULL
 JOIN crews c ON c.id=x.crew_id AND c.workspace_id=x.workspace_id AND c.deleted_at IS NULL
 JOIN workspace_members wm ON wm.workspace_id=x.workspace_id AND wm.user_id=?
 JOIN users u ON u.id=wm.user_id AND u.suspended_at IS NULL
 WHERE x.id=? AND x.workspace_id=? AND wm.access_mode='trusted' AND wm.role IN ('OWNER','ADMIN','MANAGER','MEMBER','VIEWER')
 AND NOT EXISTS(SELECT 1 FROM workspace_members WHERE user_id=? AND access_mode='restricted')`, user, resource, workspace, user).Scan(&r.MemberID, &r.MemberRevision, &r.CrewID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("capture web authority: %w", err)
	}
	if r.MemberRevision < 1 || (expected != nil && *expected != r) {
		return nil, nil
	}
	return &r, nil
}

// RevalidateTrustedQuery keeps chat and web authority types disjoint.
func RevalidateTrustedQuery(ctx context.Context, q Queryer, expected *Receipt) (*Receipt, error) {
	if expected == nil {
		return nil, nil
	}
	if expected.Kind != "" {
		return CaptureWebTrustedQuery(ctx, q, expected.Kind, expected.ResourceID, expected.UserID, expected.WorkspaceID, expected)
	}
	return CaptureTrustedQuery(ctx, q, expected.ChatID, expected.UserID, expected)
}
