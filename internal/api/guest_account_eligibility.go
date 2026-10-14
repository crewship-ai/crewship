package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
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

func writeInvitationConflict(w http.ResponseWriter, err error) {
	code, message := "invitation_changed", "This invitation changed or is no longer available. Ask your administrator for a new invitation."
	if errors.Is(err, errGuestAdministrator) {
		code, message = "guest_administrator_conflict", "This account owns or administers a workspace and cannot become a Guest. Use a separate account for Guest access."
	}
	writeJSON(w, http.StatusConflict, map[string]string{"code": code, "error": message})
}
