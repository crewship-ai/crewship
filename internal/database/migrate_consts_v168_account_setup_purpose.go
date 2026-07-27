package database

// v168 — allow `account_setup` in verification_tokens.purpose.
//
// Adding a colleague to a workspace had no working path on an instance
// without a mail server: the invite endpoint wrote a row, never called the
// mailer, and its token never reached the UI. The replacement hands the
// admin a setup link to deliver out of band, and that link is a
// verification_tokens row like any other — except it must be
// distinguishable from a password reset:
//
//   - Lifetime. A reset is 30 minutes because the person asked for it and
//     is waiting. A setup link is sent by someone else and may sit in a
//     chat window over a weekend, so it lives days.
//   - Isolation. /forgot deletes prior tokens for the same address scoped
//     to its own purpose. Sharing one purpose would mean a password reset
//     silently voids a pending account setup, and vice versa.
//
// Storing a 7-day token under `password_reset` would have avoided this
// migration while quietly weakening the documented 30-minute reset window
// for that row — a worse trade than a table rebuild.
//
// SQLite cannot ALTER a CHECK constraint, so the column is rebuilt. The
// table is small (live tokens only, all short-lived) and carries no foreign
// keys in either direction, which is what makes the straightforward
// create-copy-swap safe here rather than needing v167's PRAGMA dance.
const migrationAccountSetupPurpose = `
CREATE TABLE verification_tokens_v168 (
    identifier TEXT NOT NULL,
    token      TEXT NOT NULL UNIQUE,
    expires    DATETIME NOT NULL,
    purpose    TEXT NOT NULL DEFAULT 'email_verify'
                 CHECK (purpose IN ('email_verify', 'password_reset', 'account_setup')),
    UNIQUE (identifier, token)
);

-- Carry every live token across. Dropping them instead would invalidate
-- any reset link already in someone's inbox at upgrade time.
INSERT INTO verification_tokens_v168 (identifier, token, expires, purpose)
    SELECT identifier, token, expires, purpose FROM verification_tokens;

DROP TABLE verification_tokens;
ALTER TABLE verification_tokens_v168 RENAME TO verification_tokens;

CREATE INDEX IF NOT EXISTS idx_verification_tokens_purpose
    ON verification_tokens (purpose, expires);
`
