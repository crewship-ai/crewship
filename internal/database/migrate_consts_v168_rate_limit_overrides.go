package database

// migrationRateLimitOverrides (v168) backs the admin "Rate Limiters" console
// (internal/ratelimitcfg). Every tunable limiter value in the system — the
// per-IP HTTP buckets, the login lockout, the notification anti-storm bucket,
// crew provisioning, agent webhooks — used to be a hardcoded constant. This
// table lets an OWNER/ADMIN override any of them at runtime; a row is present
// ONLY for a key that has been explicitly overridden, so the absence of a row
// means "use the shipped default".
//
// Instance-global configuration (no workspace_id): the overrides apply to the
// whole daemon, exactly like the constants they replace. It is therefore
// deliberately NOT a workspace-scoped bundle table — see NonBackedUpTables in
// internal/backup/intent.go (an operator restoring a workspace must not
// inherit another instance's limiter tuning).
//
// `key` is validated against the ratelimitcfg registry in Go before any write,
// so the column is a free-form TEXT PK rather than an enum; an unknown key can
// never be inserted through the admin path, and Load() ignores any stale key.
const migrationRateLimitOverrides = `
CREATE TABLE IF NOT EXISTS rate_limit_overrides (
    key         TEXT PRIMARY KEY,
    value       INTEGER NOT NULL,
    updated_at  TEXT NOT NULL DEFAULT (datetime('now','subsec')),
    updated_by  TEXT
);
`
