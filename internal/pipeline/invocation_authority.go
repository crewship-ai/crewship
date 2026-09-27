package pipeline

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const (
	RoutineRunAuthority   = "routine.run"
	RoutineBatchAuthority = "routine.batch"
)

var ErrInvocationAuthorityRevoked = errors.New("routine invocation permission revoked")

// HumanInvocationAuthority stamps only authenticated human admission. Token
// and autonomous dispatch retain their existing policies.
func HumanInvocationAuthority(userID, authority string) string {
	if userID == "" {
		return ""
	}
	return authority
}

// NewInvocationAuthorityChecker re-reads the original admission policy without
// a cache. Pages must use their own policy; arbitrary metadata cannot select it.
// Empty means a legacy or not-yet-classified source: membership still applies.
func NewInvocationAuthorityChecker(db *sql.DB) func(context.Context, RunInput) error {
	return func(ctx context.Context, in RunInput) error {
		if in.InvocationAuthority == "" {
			return nil
		}
		if in.InvokingUserID == "" || db == nil {
			return ErrInvocationAuthorityRevoked
		}
		isPage := strings.HasPrefix(in.InvocationAuthority, pageActionAuthorityPrefix)
		if !isPage && in.InvocationAuthority != RoutineRunAuthority && in.InvocationAuthority != RoutineBatchAuthority {
			return ErrInvocationAuthorityRevoked
		}
		var role string
		var caps sql.NullString
		err := db.QueryRowContext(ctx, `SELECT role, capabilities FROM workspace_members WHERE workspace_id=? AND user_id=?`, in.WorkspaceID, in.InvokingUserID).Scan(&role, &caps)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrInvocationAuthorityRevoked
		}
		if err != nil {
			return fmt.Errorf("routine invocation authority lookup: %w", err)
		}
		// Same layered rule as public admission: create-tier roles always pass;
		// single-run additionally admits an explicit routine.run capability.
		switch role {
		case "OWNER", "ADMIN", "MANAGER":
			if isPage {
				return checkPageActionAuthority(ctx, db, in, role)
			}
			return nil
		}
		if in.InvocationAuthority == RoutineRunAuthority && caps.Valid {
			var capabilities []string
			if json.Unmarshal([]byte(caps.String), &capabilities) == nil {
				for _, c := range capabilities {
					if strings.TrimSpace(c) == RoutineRunAuthority {
						return nil
					}
				}
			}
		}
		return ErrInvocationAuthorityRevoked
	}
}
