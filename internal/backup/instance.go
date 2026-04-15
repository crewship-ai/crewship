package backup

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
)

// InstanceOwnerEmailEnv is the env var that grants a user the
// server-level OWNER role for instance-scope operations. Workspace
// OWNER is NOT sufficient — instance backup leaks data across every
// workspace, so the gate is separate and deliberately simple.
const InstanceOwnerEmailEnv = "CREWSHIP_OWNER_EMAIL"

// IsInstanceOwner returns true when userEmail matches the env value
// (case-insensitive, trimmed) AND the env var is non-empty. An
// unconfigured env blocks every caller — the feature is locked until
// the operator opts in. Returns false silently on any ambiguity so
// handlers can map to 403 without leaking the env's presence.
func IsInstanceOwner(userEmail string) bool {
	target := strings.TrimSpace(os.Getenv(InstanceOwnerEmailEnv))
	if target == "" {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(userEmail), target)
}

// EnsureInstanceHostname populates instance_config.hostname on first
// boot after migration v50. Idempotent: subsequent calls are no-ops
// unless the row is empty (e.g. after a restore without rotation).
// Callers run this once at server startup right after Migrate.
func EnsureInstanceHostname(ctx context.Context, db *sql.DB) error {
	if db == nil {
		return nil
	}
	var cur string
	err := db.QueryRowContext(ctx, `SELECT hostname FROM instance_config WHERE id = 1`).Scan(&cur)
	if err != nil {
		return fmt.Errorf("backup: read instance_config: %w", err)
	}
	if cur != "" {
		return nil
	}
	host, err := os.Hostname()
	if err != nil {
		return fmt.Errorf("backup: resolve hostname: %w", err)
	}
	if host == "" {
		return fmt.Errorf("backup: hostname is empty; refusing to persist blank instance identity")
	}
	if _, err := db.ExecContext(ctx, `UPDATE instance_config SET hostname = ? WHERE id = 1`, host); err != nil {
		return fmt.Errorf("backup: set instance hostname: %w", err)
	}
	return nil
}

// CurrentInstanceHostname reads the persisted instance identity, or
// the live os.Hostname() when the DB row is absent/empty.
func CurrentInstanceHostname(ctx context.Context, db *sql.DB) string {
	if db != nil {
		var host string
		if err := db.QueryRowContext(ctx, `SELECT hostname FROM instance_config WHERE id = 1`).Scan(&host); err == nil && host != "" {
			return host
		}
	}
	h, _ := os.Hostname()
	return h
}

// IsCrossInstanceRestore reports whether the bundle was produced on a
// different host than the target. Used by the restore flow to force
// JWE session-key rotation (existing sessions on the SOURCE host must
// not remain valid on a different TARGET host).
func IsCrossInstanceRestore(ctx context.Context, db *sql.DB, m *Manifest) bool {
	if m == nil {
		return false
	}
	source := strings.TrimSpace(m.SourceInstance.Hostname)
	if source == "" {
		// Unknown source — assume cross-instance to err on the safe
		// side. Forces the rotation prompt, which is cheap; missing
		// a rotation on an actual cross-instance restore would leave
		// a security gap.
		return true
	}
	target := strings.TrimSpace(CurrentInstanceHostname(ctx, db))
	return !strings.EqualFold(source, target)
}
