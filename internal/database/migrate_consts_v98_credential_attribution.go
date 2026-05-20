package database

// migrationAddCredentialAttribution (v98) gives credentials three new
// pieces of provenance metadata. Two cover the question "who created
// this row?" and the third covers "what is this row's job?" — together
// they let Crewship surface AUTO_MANAGED sidecar credentials in the
// UI with attribution to the crew's lead agent, while leaving every
// pre-v98 row untouched.
//
//   created_by_actor_type — 'user' | 'agent' | 'system'.
//                           Default 'user' on backfill: every credential
//                           that existed before this migration was put
//                           there by an authenticated UI / CLI user, so
//                           that's the only sound default.
//   created_by_actor_id   — FK-by-string to either users.id or
//                           agents.id depending on actor_type. NULL is
//                           legal: the API can't always pin the human
//                           (e.g. session expired mid-create) and
//                           system-level seed inserts have no actor.
//                           No foreign-key constraint because the column
//                           is polymorphic; integrity is enforced in the
//                           application layer.
//   provisioned_for_service — Free-form "<crew-slug>/<service-name>"
//                           tag. Non-empty marks the row as owned by a
//                           specific sidecar service declaration; the
//                           UI hides reveal/edit actions on such rows
//                           and routes mutations through the manifest
//                           apply path instead.
//
// All three are nullable (or default-backed for actor_type) so the
// migration is purely additive and rolls back to a no-op by leaving
// the columns empty.
//
// Index: a composite on (workspace_id, provisioned_for_service) lets
// the apply dispatch ask "does an auto-managed credential for
// uo-outlands/postgres already exist in this workspace?" with one
// index seek, avoiding a full table scan on every re-apply.
const migrationAddCredentialAttribution = `
ALTER TABLE credentials
    ADD COLUMN created_by_actor_type TEXT NOT NULL DEFAULT 'user'
        CHECK (created_by_actor_type IN ('user', 'agent', 'system'));
ALTER TABLE credentials
    ADD COLUMN created_by_actor_id TEXT;
ALTER TABLE credentials
    ADD COLUMN provisioned_for_service TEXT;
CREATE INDEX IF NOT EXISTS idx_credentials_provisioned_for_service
    ON credentials (workspace_id, provisioned_for_service)
    WHERE provisioned_for_service IS NOT NULL;
`
