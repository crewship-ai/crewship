// Package governance resolves the per-workspace Keeper watchdog settings
// (issue #1001, M0): the in-app OWNER/ADMIN toggle, the named security
// contact the watchdog snitches to, and the risk threshold at which a DENY
// decision also lands in the inbox.
//
// Resolution contract: an explicit workspace row always wins; no row means
// the watchdog is OFF for that workspace — it is opt-in and default OFF, only
// running once an OWNER/ADMIN enables it. The resolver is read on hot paths
// (the behavior hook fires per sampled tool call), so Resolve never returns an
// error — a failed read falls back to disabled (fail-safe: monitoring off,
// never a spurious escalation) and the caller's next sample retries naturally.
package governance

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
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
	// WatchSpec is the OWNER/ADMIN-authored free-form natural-language watch
	// rules (issue #1001, M1). Empty = fall back to the evaluator's built-in
	// anti-pattern list. Injected into the Keeper evaluator prompts via
	// CompileWatchSpec.
	WatchSpec string `json:"watch_spec"`
	// WatchPresets is the set of enabled preset keys (see WatchPresets catalog).
	// Stored as a JSON array in watch_presets; nil/empty = no presets.
	WatchPresets []string `json:"watch_presets"`
	// RequireSecondApprover is the credential-escalation "four-eyes" toggle
	// (issue #1084). When true, the user recorded as the initiating agent's
	// owner (agents.created_by_user_id) cannot resolve a CREDENTIAL
	// escalation that agent raised — approver must differ from initiator.
	// Enforced in ResolveEscalation (internal/api/escalation_handler.go), not
	// here: this package only resolves the setting. OWNER is NOT exempt.
	// Default false — existing single-approver workflows are unaffected
	// until an OWNER/ADMIN opts in.
	RequireSecondApprover bool `json:"require_second_approver"`
}

// Get returns the explicit workspace row. found is false when the workspace
// has never been configured in-app (the watchdog is then off — see Resolve).
func Get(ctx context.Context, db *sql.DB, workspaceID string) (Settings, bool, error) {
	var (
		s            Settings
		enabled      int
		contact      sql.NullString
		presets      string
		secondApprov int
	)
	err := db.QueryRowContext(ctx, `
		SELECT enabled, security_contact_user_id, deny_notify_min_risk, watch_spec, watch_presets, require_second_approver
		FROM keeper_governance_settings WHERE workspace_id = ?`, workspaceID).
		Scan(&enabled, &contact, &s.DenyNotifyMinRisk, &s.WatchSpec, &presets, &secondApprov)
	if err == sql.ErrNoRows {
		return Settings{DenyNotifyMinRisk: DefaultDenyNotifyMinRisk}, false, nil
	}
	if err != nil {
		return Settings{DenyNotifyMinRisk: DefaultDenyNotifyMinRisk}, false, fmt.Errorf("governance: get: %w", err)
	}
	s.Enabled = enabled != 0
	s.SecurityContactUserID = contact.String
	s.RequireSecondApprover = secondApprov != 0
	if presets != "" {
		if err := json.Unmarshal([]byte(presets), &s.WatchPresets); err != nil {
			return Settings{DenyNotifyMinRisk: DefaultDenyNotifyMinRisk}, false, fmt.Errorf("governance: get: decode watch_presets: %w", err)
		}
	}
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
	// Marshal presets to a JSON array; empty → "" for a stable default that
	// round-trips back to a nil slice in Get.
	presets := ""
	if len(s.WatchPresets) > 0 {
		b, err := json.Marshal(s.WatchPresets)
		if err != nil {
			return fmt.Errorf("governance: upsert: encode watch_presets: %w", err)
		}
		presets = string(b)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := db.ExecContext(ctx, `
		INSERT INTO keeper_governance_settings
			(workspace_id, enabled, security_contact_user_id, deny_notify_min_risk, watch_spec, watch_presets, require_second_approver, updated_by, created_at, updated_at)
		VALUES (?, ?, NULLIF(?, ''), ?, ?, ?, ?, NULLIF(?, ''), ?, ?)
		ON CONFLICT(workspace_id) DO UPDATE SET
			enabled = excluded.enabled,
			security_contact_user_id = excluded.security_contact_user_id,
			deny_notify_min_risk = excluded.deny_notify_min_risk,
			watch_spec = excluded.watch_spec,
			watch_presets = excluded.watch_presets,
			require_second_approver = excluded.require_second_approver,
			updated_by = excluded.updated_by,
			updated_at = excluded.updated_at`,
		workspaceID, boolToInt(s.Enabled), s.SecurityContactUserID, s.DenyNotifyMinRisk,
		s.WatchSpec, presets, boolToInt(s.RequireSecondApprover), updatedBy, now, now)
	if err != nil {
		return fmt.Errorf("governance: upsert: %w", err)
	}
	return nil
}

// Resolve returns the watchdog settings a caller should act on: the explicit
// workspace row when present, otherwise the opt-in default (disabled, default
// DENY-notify threshold). The watchdog is default-OFF per workspace (#1001) —
// a workspace only participates once an OWNER/ADMIN explicitly enables it, so
// an unconfigured workspace resolves to Enabled=false regardless of the server
// config. This is the single fetch-and-warn seam every read site shares
// (behavior hook, credential DENY-notify, F4 endpoints, sweeps); it never
// errors — a failed read behaves as unconfigured (fail-safe: monitoring off,
// never a spurious escalation). logger may be nil.
func Resolve(ctx context.Context, db *sql.DB, logger *slog.Logger, workspaceID string) Settings {
	def := Settings{DenyNotifyMinRisk: DefaultDenyNotifyMinRisk}
	if db == nil || workspaceID == "" {
		return def
	}
	s, found, err := Get(ctx, db, workspaceID)
	if err != nil {
		if logger != nil {
			logger.Warn("keeper governance: resolve failed", "error", err, "workspace_id", workspaceID)
		}
		return def
	}
	if !found {
		return def
	}
	return s
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
