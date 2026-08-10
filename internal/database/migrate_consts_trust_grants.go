package database

// migrationWaitpointTrustGrants adds standing approval grants: the
// operator-facing answer to "you have approved this same gate ten times
// already".
//
// The governance surface already remembers consent once, but only on the
// routine-DEFINITION plane — internal/api/pipeline_governance.go skips
// re-review when a save's risk factors are a subset of the approved
// definition ("risk unchanged from the already-approved definition").
// This table is that same idea on the RUN plane: a gate the operator has
// repeatedly waved through stops minting a blocking inbox card.
//
// What a grant is bound to, and why each column is in the key:
//
//	workspace_id + pipeline_id + step_id
//	  Trust is per gate, never per routine and never crew-wide. A
//	  routine may hold a read-only gate and a publish-to-customer gate;
//	  trusting the first must not touch the second. This is deliberately
//	  narrower than crews.autonomy_level (v101), whose blast radius is
//	  every governable action the crew takes — the v106 comment already
//	  records why bundling agent-level posture into that dial was
//	  rejected, and the same reasoning applies here.
//
//	definition_hash
//	  The load-bearing column. It is pipeline_versions.definition_hash —
//	  sha256 over the exact definition_json the operator was looking at
//	  when they granted trust. Any edit to the routine mints a new
//	  version with a new hash (v79 keeps UNIQUE (pipeline_id,
//	  definition_hash)), so no live grant matches and the gate asks
//	  again. Revocation-on-change therefore needs no watcher, no
//	  invalidation pass and no risk re-classification: it is a
//	  consequence of the lookup key. An operator cannot be socially
//	  engineered into trusting a gate and then having the step under it
//	  rewritten.
//
// The partial UNIQUE index scopes uniqueness to LIVE grants. Revoking
// writes revoked_at rather than deleting, so the audit answers "who
// trusted this, and who took it back" — and a later re-grant for the
// same definition is still admitted. Without the WHERE clause, revoking
// and re-granting would collide on the second grant.
//
// max_uses / expires_at are both nullable = unbounded. They exist so a
// cautious operator can say "auto-approve the next 20" or "until Friday"
// instead of choosing between forever and never; the API defaults them
// to NULL because the feature's whole point is to stop asking.
//
// FK to pipelines ON DELETE CASCADE: pipeline ids are CUIDs and not
// meant to recycle, but a grant outliving its routine is trust with no
// definition to pin it to, which is exactly the state this design
// refuses to represent.
//
// Timestamp version, not sequential — the v1..v169 block closed while
// this branch was open (see the migrations slice tail for the same call
// made by notify_taxonomy).
const migrationWaitpointTrustGrants = `
CREATE TABLE IF NOT EXISTS waitpoint_trust_grants (
    id                 TEXT PRIMARY KEY,                                  -- "wtg_" + CUID
    workspace_id       TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    pipeline_id        TEXT NOT NULL REFERENCES pipelines(id) ON DELETE CASCADE,
    step_id            TEXT NOT NULL,                                     -- the wait:approval step, not the routine
    definition_hash    TEXT NOT NULL,                                     -- pipeline_versions.definition_hash the operator trusted
    granted_by_user_id TEXT NOT NULL,
    granted_at         TEXT NOT NULL DEFAULT (datetime('now','subsec')),
    reason             TEXT,                                              -- free text; surfaced in the journal entry
    prior_approvals    INTEGER NOT NULL DEFAULT 0,                        -- how many manual approvals earned the offer
    max_uses           INTEGER,                                           -- NULL = unbounded
    uses               INTEGER NOT NULL DEFAULT 0,
    expires_at         TEXT,                                              -- NULL = no expiry
    revoked_at         TEXT,
    revoked_by_user_id TEXT,
    revoke_reason      TEXT
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_waitpoint_trust_grants_live
    ON waitpoint_trust_grants (workspace_id, pipeline_id, step_id, definition_hash)
    WHERE revoked_at IS NULL;

-- Backs the hot path: every wait:approval step consults this on the way
-- to minting a waitpoint, so the lookup must not scan.
CREATE INDEX IF NOT EXISTS idx_waitpoint_trust_grants_lookup
    ON waitpoint_trust_grants (workspace_id, pipeline_id, step_id)
    WHERE revoked_at IS NULL;
`
