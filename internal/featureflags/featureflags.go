// Package featureflags gives non-HTTP code (background jobs, journal
// emit call sites) a way to read the same two-tier feature flag state
// the REST surface in internal/api/feature_flags_handler.go exposes,
// without importing the api package.
package featureflags

import (
	"context"
	"database/sql"
)

// IsEnabled resolves key's effective value for workspaceID: a
// per-workspace override (feature_flag_overrides) wins over the flag's
// instance-wide default (feature_flags.enabled). A flag key with no
// matching row resolves to false — an undefined flag is off, not an
// error, since callers gate optional behavior on this and shouldn't
// have to special-case "not yet created".
//
// Mirrors the LEFT JOIN FeatureFlagHandler.List uses in
// internal/api/feature_flags_handler.go.
func IsEnabled(ctx context.Context, db *sql.DB, workspaceID, key string) (bool, error) {
	var enabledInt int
	var overrideNull sql.NullInt64
	err := db.QueryRowContext(ctx, `
		SELECT f.enabled, o.enabled
		FROM feature_flags f
		LEFT JOIN feature_flag_overrides o
		  ON o.flag_id = f.id AND o.workspace_id = ?
		WHERE f.key = ?`, workspaceID, key,
	).Scan(&enabledInt, &overrideNull)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if overrideNull.Valid {
		return overrideNull.Int64 != 0, nil
	}
	return enabledInt != 0, nil
}
