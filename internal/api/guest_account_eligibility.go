package api

import (
	"context"
	"database/sql"
	"errors"
)

var errGuestAdministrator = errors.New("Guest accounts cannot administer workspaces")

// Account-wide Guest restrictions must never strand an administrator, including
// an unclaimed account provisioned as administrator in another workspace.
func guestAccountEligible(ctx context.Context, tx *sql.Tx, user string) error {
	var admin bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM workspace_members WHERE user_id=? AND role IN ('OWNER','ADMIN'))`, user).Scan(&admin); err != nil {
		return err
	}
	if admin {
		return errGuestAdministrator
	}
	return nil
}

// Check the reverse transition as well: an account restricted anywhere cannot
// take administration elsewhere. The database invariant closes concurrent writes.
func administratorAccountEligible(ctx context.Context, tx *sql.Tx, user, role string) error {
	if role != "ADMIN" && role != "OWNER" {
		return nil
	}
	var guest bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM workspace_members WHERE user_id=? AND access_mode='restricted')`, user).Scan(&guest); err != nil {
		return err
	}
	if guest {
		return errGuestAdministrator
	}
	return nil
}

func invitationConflict(err error) map[string]string {
	code, message := "invitation_changed", "This invitation changed or is no longer available. Ask your administrator for a new invitation."
	if errors.Is(err, errGuestAdministrator) {
		code, message = "guest_administrator_conflict", "Guest access and workspace administration cannot be combined on one account. Restore trusted access in every workspace or use a separate account."
	}
	return map[string]string{"code": code, "error": message}
}
