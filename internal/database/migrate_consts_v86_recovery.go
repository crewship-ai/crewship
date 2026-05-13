package database

// migrationAddRecoveryAndPairing (v86) lays the groundwork for the
// pre-beta auth overhaul: a `purpose` discriminator on the existing
// verification_tokens table (so password-reset tokens can ride the
// same surface as the email-verify tokens that are already there)
// plus a `cli_pairings` table that the device-code flow uses to
// hand a session off to a user's local CLI (Claude Code, Gemini,
// Codex, OpenCode, Cursor, Factory Droid — agnostic, set by the
// registry in lib/cli-adapters.ts, never by the backend).
//
// Design choices:
//
//   - Re-use verification_tokens instead of a parallel
//     password_reset_tokens table. The schema (identifier, token,
//     expires, single-use semantics) is identical; only the *intent*
//     differs. A `purpose` discriminator with a CHECK constraint
//     keeps the type system honest while avoiding two near-duplicate
//     tables that would drift over time. Existing rows backfill to
//     'email_verify' to preserve current behaviour.
//
//   - cli_pairings is a device-code flow (RFC 8628 in spirit, not
//     letter): UI generates a short, human-typeable code; user runs
//     `crewship login --pair --code=XXXX-XXXX` from their terminal;
//     CLI redeems it once for a real cli_token. The redeem endpoint
//     is unauthenticated *by design* — the code itself is the
//     credential (10-min TTL, single-use, rate-limited per IP).
//     adapter_hint is telemetry only — the backend MUST NOT route on
//     it, otherwise adding a 7th CLI adapter becomes a backend touch.
//
//   - status is a CHECK-constrained enum so consumed_at and status
//     can't drift apart (a redeemed pairing must be both
//     status='consumed' and consumed_at IS NOT NULL).
const migrationAddRecoveryAndPairing = `
-- Existing verification_tokens rows are all email-verify intents.
-- Backfill explicitly so the column is not nullable going forward
-- and the CHECK constraint can be relied on by readers.
ALTER TABLE verification_tokens ADD COLUMN purpose TEXT NOT NULL DEFAULT 'email_verify'
    CHECK (purpose IN ('email_verify', 'password_reset'));

CREATE INDEX IF NOT EXISTS idx_verification_tokens_purpose
    ON verification_tokens (purpose, expires);

CREATE TABLE IF NOT EXISTS cli_pairings (
    id            TEXT PRIMARY KEY,
    user_id       TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    code          TEXT NOT NULL UNIQUE,                -- 8-char base32, human-typeable
    status        TEXT NOT NULL DEFAULT 'pending'
                    CHECK (status IN ('pending', 'consumed', 'expired')),
    adapter_hint  TEXT,                                -- telemetry only: 'CLAUDE_CODE' | 'GEMINI_CLI' | ...
    created_at    TEXT NOT NULL,
    expires_at    TEXT NOT NULL,                       -- created_at + 10 min
    consumed_at   TEXT
);

CREATE INDEX IF NOT EXISTS idx_cli_pairings_code
    ON cli_pairings (code);

-- Poll hot-path: UI polls by (user_id, status) every 2 sec waiting
-- for status to flip from 'pending' to 'consumed'.
CREATE INDEX IF NOT EXISTS idx_cli_pairings_user_status
    ON cli_pairings (user_id, status);
`
