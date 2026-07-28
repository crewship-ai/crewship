-- Multi-part credentials — PRD-CREDENTIALS-V2-2026 §2.2, and the fix for the
-- §1.5 V5 defect: "one secret = one value" cannot express AWS static creds
-- (access key id + secret + region), a service-account JSON (blob + filename),
-- or anything carrying a TOTP seed, a passphrase, a host or an account id.
--
-- USERPASS was made to fit by bolting a `username` column onto `credentials`.
-- That does not generalise: the next multi-part type adds another column and
-- the one after that another, and every reader of the credentials row grows a
-- branch it did not ask for. Vaultwarden's answer — a small set of item types
-- plus custom fields as a first-class feature — covers thousands of tools with
-- no per-tool schema, and this table is that feature.
--
-- WHAT DOES NOT MOVE. `credentials.encrypted_value` and `credentials.username`
-- stay exactly where they are and keep their meaning. Several readers depend
-- on them (the delivery path, the sidecar boot payload, resolveAgentCredentials,
-- the reveal endpoint, the rotation pool), and this migration deliberately does
-- NOT backfill them into rows here. A backfill would create a SECOND writable
-- copy of one datum with no owner: `credential update --username` writes the
-- column, a field write would write the row, and delivery — which reads the
-- column — would keep using the stale one while the UI showed the fresh one.
-- Silent divergence in a vault is worse than an extra column. So fields hold
-- only the ADDITIONAL parts, and the API refuses the reserved keys `username`,
-- `value` and `password` for the same reason.
--
-- WHY TWO VALUE COLUMNS. `value` is cleartext, `encrypted_value` is AEAD, and
-- `is_secret` says which one is populated. The obvious alternative — one
-- `encrypted_value` column that sometimes holds plaintext — is a trap: a
-- future reader doing decryptCredential(row.encrypted_value) on every row
-- either errors on the cleartext ones or, worse, some path treats a plaintext
-- as ciphertext and hands it onward. Naming the columns for what they hold
-- means the mistake cannot be made silently, and it lets a security review
-- answer "where does this database store plaintext?" by grepping for a column
-- name rather than by reading application code.
--
-- Non-secret fields ARE cleartext on purpose, for the reason already recorded
-- for `credentials.username`: `region`, `account_id`, `host` are identifiers,
-- not secrets. Cleartext lets the UI search and sort them without a per-row
-- AEAD decrypt, and every value NOT put through GCM is a value that cannot be
-- lost when the master key rotates badly.
--
-- The CHECK is the enforcement, not documentation. The handler is exactly the
-- component under suspicion when a secret ends up in the cleartext column, so
-- the rejection has to come from the engine. Likewise the composite primary
-- key: the API also rejects a duplicate key, but two concurrent POSTs both
-- clear an application-level "does it exist?" check before either inserts, and
-- only the constraint closes that window.
--
-- ON DELETE CASCADE fires on a HARD delete. `DELETE /credentials/{id}` is a
-- soft delete (deleted_at), so fields outlive a revoke exactly as
-- `credentials.encrypted_value` does — the same posture, not a new one. They
-- are unreachable while soft-deleted because every read path joins through the
-- credential's `deleted_at IS NULL` filter.

CREATE TABLE IF NOT EXISTS credential_fields (
    credential_id   TEXT    NOT NULL REFERENCES credentials(id) ON DELETE CASCADE,
    -- lower_snake_case, validated in Go. Lowercase-only is not cosmetic: a
    -- field key becomes an env-var suffix and a file basename on the delivery
    -- path, where `Region` and `region` would collide the moment either is
    -- upcased to REGION. One canonical form now avoids a rename migration.
    key             TEXT    NOT NULL,
    -- Exactly one of these is non-NULL; is_secret says which. See the CHECK.
    value           TEXT,
    encrypted_value TEXT,
    is_secret       INTEGER NOT NULL DEFAULT 1,
    -- Display/injection order. Not unique: reordering by rewriting one row's
    -- ordinal must not have to renumber its siblings inside a transaction.
    ordinal         INTEGER NOT NULL DEFAULT 0,
    created_at      TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    updated_at      TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    PRIMARY KEY (credential_id, key),
    CHECK (is_secret IN (0, 1)),
    CHECK (
        (is_secret = 1 AND encrypted_value IS NOT NULL AND value IS NULL)
        OR
        (is_secret = 0 AND value IS NOT NULL AND encrypted_value IS NULL)
    )
);

-- The only access pattern: "all fields of this credential, in order". The
-- composite PK already covers credential_id lookups, but not the ORDER BY, so
-- listing a credential's fields would sort every time without this.
CREATE INDEX IF NOT EXISTS idx_credential_fields_cred_ordinal
    ON credential_fields(credential_id, ordinal);
