package pipeline

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/crewship-ai/crewship/internal/access"
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
		// Unattended triggers retain the live human author's account ceiling.
		// Crew/agent-authored routines (no human author) keep their own policy.
		if in.InvokingUserID == "" && unattendedAccountTrigger(in.TriggeredVia) {
			if db == nil {
				return ErrInvocationAuthorityRevoked
			}
			var author sql.NullString
			err := db.QueryRowContext(ctx, `SELECT author_user_id FROM pipelines WHERE id=? AND workspace_id=? AND deleted_at IS NULL`, in.PipelineID, in.WorkspaceID).Scan(&author)
			if err != nil {
				return fmt.Errorf("routine trigger owner authority: %w", err)
			}
			if author.Valid && author.String != "" {
				var active bool
				err = db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE id=? AND suspended_at IS NULL)`, author.String).Scan(&active)
				if err != nil {
					return fmt.Errorf("routine trigger account authority: %w", err)
				}
				if !active {
					return ErrInvocationAuthorityRevoked
				}
				var member bool
				err = db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM workspace_members WHERE user_id=? AND workspace_id=? AND access_mode='trusted')`, author.String, in.WorkspaceID).Scan(&member)
				if err != nil {
					return fmt.Errorf("routine trigger author membership: %w", err)
				}
				if !member {
					return ErrInvocationAuthorityRevoked
				}
			}
		}

		if in.InvokingUserID != "" && db != nil {
			// Persisted human work must retain a live account, including after
			// waits or restarts. Autonomous triggers have no human identity.
			var active bool
			if err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE id=? AND suspended_at IS NULL)`, in.InvokingUserID).Scan(&active); err != nil {
				return fmt.Errorf("routine account authority: %w", err)
			}
			if !active {
				return ErrInvocationAuthorityRevoked
			}
			restricted, err := (access.Store{DB: db}).HasRestrictedMembership(ctx, in.InvokingUserID)
			if err != nil {
				return fmt.Errorf("routine resource authority: %w", err)
			}
			if restricted {
				return ErrInvocationAuthorityRevoked
			}
		}
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

func unattendedAccountTrigger(source TriggeredVia) bool {
	return source == TriggeredViaAutomation || source == TriggeredViaSchedule || source == TriggeredViaWebhook || source == TriggeredViaWakeCheck || source == TriggeredViaCallPipeline
}
