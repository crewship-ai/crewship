package database

// migrationCLITokenTiers (v98) introduces a two-tier CLI token system.
//
// Pre-v98 every cli_tokens row was a single tier: a SHA-256 hash of a
// 20-byte random secret prefixed with "crewship_cli_". No expiry, no
// scope, no admin distinction, no per-use audit beyond the async
// last_used_at update.
//
// v98 adds:
//
//   - `tier`: 'STANDARD' (default, existing rows backfilled to this) or
//     'ADMIN'. ADMIN-tier tokens carry the "crewship_admin_" prefix and
//     are hashed with HMAC-SHA256 keyed by CREWSHIP_ADMIN_TOKEN_HMAC_KEY
//     (a server-side secret separate from ENCRYPTION_KEY). A DB dump
//     alone can't offline-crack ADMIN tokens because the HMAC key is
//     never persisted — an attacker needs both the DB row and the env
//     variable. STANDARD tokens stay on plain SHA-256, which is fine
//     because the cleartext token is 256-bit random (no rainbow tables
//     possible) and the threat for STANDARD is theft, not DB dump.
//
//   - `expires_at`: nullable for STANDARD, mandatory for ADMIN (the
//     handler enforces ADMIN ≤ 7 days). NULL = no expiry.
//
//   - `cli_token_uses`: per-use audit ring for ADMIN tier. STANDARD
//     tier keeps the async last_used_at debounce; ADMIN writes a row
//     per use synchronously so an incident responder can answer "what
//     did this admin token touch in the last hour?" without sampling.
//     Capped at 10k rows via a periodic prune (handler-side, not in
//     this migration).
//
// Backwards-compat: existing STANDARD rows keep working with their
// SHA-256 hash unchanged. The validator dispatches on token prefix
// (crewship_cli_ → SHA-256 lookup against tier=STANDARD; crewship_admin_
// → HMAC-SHA256 lookup against tier=ADMIN).
const migrationCLITokenTiers = `
ALTER TABLE cli_tokens ADD COLUMN tier TEXT NOT NULL DEFAULT 'STANDARD'
    CHECK(tier IN ('STANDARD', 'ADMIN'));
ALTER TABLE cli_tokens ADD COLUMN expires_at TEXT;

CREATE INDEX IF NOT EXISTS idx_cli_token_tier_expires
    ON cli_tokens(tier, expires_at) WHERE expires_at IS NOT NULL;

CREATE TABLE IF NOT EXISTS cli_token_uses (
    id TEXT PRIMARY KEY,
    token_id TEXT NOT NULL REFERENCES cli_tokens(id) ON DELETE CASCADE,
    used_at TEXT NOT NULL DEFAULT (datetime('now')),
    remote_addr TEXT,
    user_agent TEXT,
    path TEXT
);
CREATE INDEX IF NOT EXISTS idx_cli_token_uses_token ON cli_token_uses(token_id, used_at DESC);
CREATE INDEX IF NOT EXISTS idx_cli_token_uses_used_at ON cli_token_uses(used_at DESC);
`
