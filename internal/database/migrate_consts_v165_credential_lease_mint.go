package database

// migrationCredentialLeaseMint (v165) is the second half of the credential-lease
// work (#1373). v149 gave the grant an `expires_at` so a lease could be *carried*
// and *enforced*; nothing ever *minted* one except an operator typing
// `credential assign --ttl`. This migration adds the two things auto-issuance
// needs:
//
//  1. Lease provenance on agent_credentials. A grant that silently starts
//     expiring is unexplainable in an incident review — the operator needs to
//     see WHY it is a lease and WHICH approval minted it. lease_source is the
//     issuing event ('manual' | 'keeper_allow' | 'escalation_approve'),
//     lease_issued_at is when, and lease_request_id points at the keeper_requests
//     row (or escalation id) that authorised it. All nullable: a pre-migration
//     lease keeps working and simply reports an unknown source, and a standing
//     grant (expires_at IS NULL) has none of them set.
//
//  2. keeper_governance_settings.auto_lease_seconds — the per-workspace opt-in
//     TTL for auto-issuance. 0 (the default, and the value every existing row
//     backfills to) means "do not auto-issue", i.e. exactly today's behaviour.
//     A positive value makes a Keeper ALLOW / escalation approve on an L3/L4
//     credential re-issue the grant as a lease of that length. It lives with the
//     other Keeper governance toggles (watchdog, four-eyes, gov model) because it
//     is the same kind of per-workspace security posture knob and shares their
//     OWNER/ADMIN-only surface.
//
// Additive columns only — no table rebuild, no backfill pass needed (SQLite's
// ADD COLUMN with a NOT NULL DEFAULT 0 populates existing rows in place).
const migrationCredentialLeaseMint = `
ALTER TABLE agent_credentials ADD COLUMN lease_source TEXT;
ALTER TABLE agent_credentials ADD COLUMN lease_issued_at TEXT;
ALTER TABLE agent_credentials ADD COLUMN lease_request_id TEXT;

ALTER TABLE keeper_governance_settings ADD COLUMN auto_lease_seconds INTEGER NOT NULL DEFAULT 0;

-- Lease expiry is read on hot paths: every boot credential resolve, every
-- /keeper/execute injection, and the crew-scoped internal listing the sidecar
-- reaper polls every 60s. Those all filter "expires_at IS NULL OR expires_at >
-- now" alongside an agent_id / credential_id lookup, so a partial index over
-- just the leased rows keeps the scan proportional to the (small) number of
-- live leases instead of the whole grant table.
CREATE INDEX IF NOT EXISTS idx_agent_credentials_lease
    ON agent_credentials(expires_at, agent_id, credential_id)
    WHERE expires_at IS NOT NULL;
`
