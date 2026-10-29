package access

import (
	"context"
	"database/sql"
	"errors"
)

// The complete replacement is about to set this membership to trusted. Other
// restricted memberships still impose the account-wide Guest ceiling.
func trustedCrewRecipient(ctx context.Context, q queryer, user, workspace string) error {
	var one int
	err := q.QueryRowContext(ctx, `SELECT 1 FROM workspace_members wm
JOIN users u ON u.id=wm.user_id AND u.suspended_at IS NULL
WHERE wm.user_id=? AND wm.workspace_id=? AND wm.role IN ('MEMBER','MANAGER')
AND NOT EXISTS(SELECT 1 FROM workspace_members ceiling WHERE ceiling.user_id=wm.user_id AND ceiling.workspace_id<>wm.workspace_id AND ceiling.access_mode='restricted')`, user, workspace).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrDenied
	}
	return err
}

func crewManage(ctx context.Context, q queryer, user, workspace, crew string) error {
	var one int
	err := q.QueryRowContext(ctx, `SELECT 1 FROM live_access_grants g
JOIN workspace_members wm ON wm.id=g.member_id AND wm.access_mode='trusted' AND wm.role IN ('MEMBER','MANAGER')
JOIN users u ON u.id=wm.user_id AND u.suspended_at IS NULL
JOIN crews c ON c.id=g.crew_id AND c.workspace_id=wm.workspace_id AND c.deleted_at IS NULL AND c.kind<>'setup'
WHERE wm.user_id=? AND wm.workspace_id=? AND c.id=? AND g.resource_kind='crew' AND g.operation='manage'
AND NOT EXISTS(SELECT 1 FROM workspace_members ceiling WHERE ceiling.user_id=wm.user_id AND ceiling.access_mode='restricted')`, user, workspace, crew).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrDenied
	}
	return err
}

// CrewManage checks an explicit additive grant, never a workspace role floor.
// Mutations must use CrewManageIn again on their writer transaction.
func (s Store) CrewManage(ctx context.Context, user, workspace, crew string) error {
	if s.DB == nil {
		return ErrDenied
	}
	return crewManage(ctx, s.DB, user, workspace, crew)
}

func (s Store) CrewManageIn(ctx context.Context, tx *sql.Tx, user, workspace, crew string) error {
	if tx == nil {
		return ErrDenied
	}
	return crewManage(ctx, tx, user, workspace, crew)
}
