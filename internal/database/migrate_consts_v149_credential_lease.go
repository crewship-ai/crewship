package database

// migrationCredentialLeaseExpiry (v149) adds an optional short-lived lease to
// the agent_credentials grant (#1373). A credential grant is normally standing
// (long-lived, reused across sessions) — the Hugging Face incident's #1 lesson
// is that a stolen STANDING token stays valuable, while a stolen session-scoped
// lease is worthless once its TTL lapses.
//
// expires_at is a nullable RFC3339 UTC timestamp:
//   - NULL  → a standing grant (the pre-migration default, unchanged behaviour).
//   - set   → the grant is a lease; injection paths (notably /keeper/execute,
//     the L3/L4 per-command injection point) MUST refuse it once
//     `expires_at <= now`, fail-closed, and require a fresh grant.
//
// Additive and nullable so every pre-migration assignment keeps working as a
// standing grant with no backfill required.
const migrationCredentialLeaseExpiry = `
ALTER TABLE agent_credentials ADD COLUMN expires_at TEXT;
`
