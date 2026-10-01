package governance

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// DefaultsSettingKey is the app_settings row holding the instance defaults:
// the template a new workspace is created with. They are a template, not a
// live policy — SeedWorkspace copies them into the workspace's own row when
// it is created, and changing them later changes no existing workspace.
const DefaultsSettingKey = "keeper.governance_defaults"

// Defaults returns the instance defaults, or the built-in opt-out (watchdog
// off, default DENY-notify threshold) when none were ever saved. found tells
// the two apart.
func Defaults(ctx context.Context, db Querier) (Settings, bool, error) {
	builtIn := Settings{DenyNotifyMinRisk: DefaultDenyNotifyMinRisk}
	var raw string
	err := db.QueryRowContext(ctx, `SELECT value FROM app_settings WHERE key = ?`, DefaultsSettingKey).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return builtIn, false, nil
	}
	if err != nil {
		return builtIn, false, fmt.Errorf("governance: defaults: %w", err)
	}
	var s Settings
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		return builtIn, false, fmt.Errorf("governance: defaults: decode: %w", err)
	}
	return defaultable(normalize(s)), true, nil
}

// SetDefaults stores the instance defaults. The security contact and the
// governance-model credential are dropped: a user or a vault credential
// belongs to one workspace and cannot be anyone's default.
func SetDefaults(ctx context.Context, db Execer, s Settings) error {
	b, err := json.Marshal(defaultable(normalize(s)))
	if err != nil {
		return fmt.Errorf("governance: set defaults: %w", err)
	}
	_, err = db.ExecContext(ctx, `
		INSERT INTO app_settings (key, value, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		DefaultsSettingKey, string(b), time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return fmt.Errorf("governance: set defaults: %w", err)
	}
	return nil
}

// defaultable strips what only one workspace can hold.
func defaultable(s Settings) Settings {
	s.SecurityContactUserID = ""
	s.GovModelCredentialID = ""
	return s
}

// QueryExecer is a transaction (or the DB) that both reads and writes.
type QueryExecer interface {
	Querier
	Execer
}

// SeedWorkspace gives a new workspace its own copy of the defaults (the
// built-in opt-out when none were saved). Call it in the transaction that
// creates the workspace. A row that already exists — a restored workspace
// brings its own — is left alone.
func SeedWorkspace(ctx context.Context, tx QueryExecer, workspaceID string) error {
	d, _, err := Defaults(ctx, tx)
	if err != nil {
		return err
	}
	d = normalize(d)
	presets, err := encodePresets(d.WatchPresets)
	if err != nil {
		return fmt.Errorf("governance: seed: %w", err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	_, err = tx.ExecContext(ctx, `
		INSERT INTO keeper_governance_settings
			(workspace_id, enabled, deny_notify_min_risk, watch_spec, watch_presets, require_second_approver,
			 gov_model_provider, gov_model_id, auto_lease_seconds, behavior_sample_every, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(workspace_id) DO NOTHING`,
		workspaceID, boolToInt(d.Enabled), d.DenyNotifyMinRisk, d.WatchSpec, presets, boolToInt(d.RequireSecondApprover),
		d.GovModelProvider, d.GovModelID, d.AutoLeaseSeconds, d.BehaviorSampleEvery, now, now)
	if err != nil {
		return fmt.Errorf("governance: seed: %w", err)
	}
	return nil
}
