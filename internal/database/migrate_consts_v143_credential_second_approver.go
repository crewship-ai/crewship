package database

// migrationCredentialSecondApprover (v143) adds a per-workspace "four-eyes"
// toggle for credential escalation approvals (issue #1084). It extends the
// M0 Keeper governance row rather than adding a parallel settings table —
// same rationale as v139 (migrate_consts_v139_keeper_watch_spec.go): the
// resolver already returns a single per-workspace Settings struct, and this
// is one more OWNER/ADMIN-authored governance knob alongside it.
//
// require_second_approver: when set, ResolveEscalation refuses to let the
// same human who is recorded as the initiating agent's owner
// (agents.created_by_user_id, v100) resolve a CREDENTIAL escalation that
// agent raised — approver must differ from initiator. OWNER is NOT exempt:
// this is a strict segregation-of-duties gate, not a permission check, so no
// role bypasses it. Default 0 (off) — existing single-approver workflows are
// unaffected until an OWNER/ADMIN opts in.
//
// No new attribution columns are needed: the initiator identity is already
// derivable by joining escalations.from_agent_id -> agents.created_by_user_id
// (v100), and the approver is the authenticated user resolving the request
// (already recorded via credentials.approved_by_user_id, v119, for the
// linked-credential case). This migration only adds the toggle itself.
const migrationCredentialSecondApprover = `
ALTER TABLE keeper_governance_settings ADD COLUMN require_second_approver INTEGER NOT NULL DEFAULT 0;
`
