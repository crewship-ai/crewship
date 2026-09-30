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
// what an instance admin last saved for "All workspaces". Every workspace got
// an explicit row at that moment; the defaults are what a workspace created
// later starts from, since it has no row of its own.
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
