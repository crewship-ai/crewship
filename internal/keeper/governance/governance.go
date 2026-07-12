// Package governance resolves the per-workspace Keeper watchdog settings
// (issue #1001, M0): the in-app OWNER/ADMIN toggle, the named security
// contact the watchdog snitches to, and the risk threshold at which a DENY
// decision also lands in the inbox.
//
// Resolution contract: an explicit workspace row always wins; no row means
// "inherit the server config" so existing env-driven deployments keep their
// behavior on upgrade. The resolver is read on hot paths (the behavior hook
// fires per sampled tool call), so Effective never returns an error — a
// failed read falls back to the server default and the caller's next sample
// retries naturally.
package governance

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// DefaultDenyNotifyMinRisk is the risk score (1–10) at or above which a DENY
// decision is snitched to the inbox when a workspace has no explicit setting.
const DefaultDenyNotifyMinRisk = 7

// Settings is the per-workspace watchdog configuration.
type Settings struct {
	// Enabled gates the behavioral watchdog layer (behavior monitoring,
	// DENY-notify). It does NOT gate the credential-access gatekeeper
	// enforcement path, which stays server-configured (KEEPER_ENABLED) —
	// a workspace toggle must not be able to weaken credential isolation.
	Enabled bool `json:"enabled"`
	// SecurityContactUserID targets snitch inbox items at a named admin.
	// Empty = legacy TargetRole MANAGER fanout.
	SecurityContactUserID string `json:"security_contact_user_id"`
	// DenyNotifyMinRisk is the risk score (1–10) at or above which a DENY
	// decision also lands in the inbox. ESCALATE always does.
	DenyNotifyMinRisk int `json:"deny_notify_min_risk"`
}

// Get returns the explicit workspace row. found is false when the workspace
// has never been configured in-app (callers then inherit the server config).
func Get(ctx context.Context, db *sql.DB, workspaceID string) (Settings, bool, error) {
	var (
		s       Settings
		enabled int
		contact sql.NullString
	)
	err := db.QueryRowContext(ctx, `
		SELECT enabled, security_contact_user_id, deny_notify_min_risk
		FROM keeper_governance_settings WHERE workspace_id = ?`, workspaceID).
		Scan(&enabled, &contact, &s.DenyNotifyMinRisk)
	if err == sql.ErrNoRows {
		return Settings{DenyNotifyMinRisk: DefaultDenyNotifyMinRisk}, false, nil
	}
	if err != nil {
		return Settings{DenyNotifyMinRisk: DefaultDenyNotifyMinRisk}, false, fmt.Errorf("governance: get: %w", err)
	}
	s.Enabled = enabled != 0
	s.SecurityContactUserID = contact.String
	return s, true, nil
}

// Upsert writes the workspace row. updatedBy is the acting user (may be
// empty for system writes). DenyNotifyMinRisk outside [1,10] is clamped.
func Upsert(ctx context.Context, db *sql.DB, workspaceID string, s Settings, updatedBy string) error {
	if s.DenyNotifyMinRisk < 1 {
		s.DenyNotifyMinRisk = 1
	}
	if s.DenyNotifyMinRisk > 10 {
		s.DenyNotifyMinRisk = 10
	}
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := db.ExecContext(ctx, `
		INSERT INTO keeper_governance_settings
			(workspace_id, enabled, security_contact_user_id, deny_notify_min_risk, updated_by, created_at, updated_at)
		VALUES (?, ?, NULLIF(?, ''), ?, NULLIF(?, ''), ?, ?)
		ON CONFLICT(workspace_id) DO UPDATE SET
			enabled = excluded.enabled,
			security_contact_user_id = excluded.security_contact_user_id,
			deny_notify_min_risk = excluded.deny_notify_min_risk,
			updated_by = excluded.updated_by,
			updated_at = excluded.updated_at`,
		workspaceID, boolToInt(s.Enabled), s.SecurityContactUserID, s.DenyNotifyMinRisk,
		updatedBy, now, now)
	if err != nil {
		return fmt.Errorf("governance: upsert: %w", err)
	}
	return nil
}

// Effective resolves the watchdog settings a caller should act on: the
// explicit workspace row when present, otherwise the server default
// (serverDefault = whether the env-configured watchdog is on for this
// instance). Never errors — a failed read behaves as unconfigured.
func Effective(ctx context.Context, db *sql.DB, workspaceID string, serverDefault bool) Settings {
	if db == nil || workspaceID == "" {
		return Settings{Enabled: serverDefault, DenyNotifyMinRisk: DefaultDenyNotifyMinRisk}
	}
	s, found, err := Get(ctx, db, workspaceID)
	if err != nil || !found {
		return Settings{Enabled: serverDefault, DenyNotifyMinRisk: DefaultDenyNotifyMinRisk}
	}
	return s
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
