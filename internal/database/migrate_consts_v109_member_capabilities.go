package database

// migrationMemberCapabilities (v109) adds a per-membership capability
// set to workspace_members so workspace admins can grant specific
// end-user actions (create routines, create skills, write memory, ...)
// without promoting a member to the next role tier.
//
// Why: pre-v109 the only knob admins had over end-user reach was the
// 5-rank role ladder (VIEWER < MEMBER < MANAGER < ADMIN < OWNER).
// Promoting Ludmila from MEMBER to MANAGER so she can author a single
// routine also opens credential mutation, skill generation, and every
// other MANAGER+ surface in the workspace. Capabilities are a flat,
// composable layer on top of roles — admin grants exactly the actions
// the user needs, no more.
//
// Capabilities are workspace-scoped (per membership row). A user in
// multiple workspaces configures each independently — there's no
// cross-workspace inheritance. See PRD-SLASH-CAPABILITIES-2026.md §3
// for the non-goal explicitly excluding per-resource ACLs.
//
// Schema: capabilities TEXT (JSON array of strings). Matches the
// cli_tokens.scopes pattern from v100 — JSON in TEXT, parsed in Go,
// no Postgres/SQLite divergence. NULL on legacy rows means "fall back
// to role-derived defaults" until the backfill runs, which gives the
// canRole gate a chance to keep serving single-operator installs that
// upgrade through this migration in a single window.
//
// The backfill writes role-aware bundles so existing OWNER/ADMIN
// users keep the full surface, MANAGERs get the routine/issue/memory
// tier they could already author through requireRole("create"), and
// MEMBER/VIEWER get the chat-only baseline. The bundle catalog is
// duplicated here (rather than referenced from a Go constant) so the
// migration is self-contained — running it in psql without the Go
// binary still produces the right rows.
//
// Net-new column, no FK, no recreate dance. The partial index is
// only on rows that have explicit non-default capability sets so the
// admin "who has elevated privileges" query stays cheap on a
// workspace where most rows still carry the role-derived default.
const migrationMemberCapabilities = `
ALTER TABLE workspace_members ADD COLUMN capabilities TEXT;

UPDATE workspace_members
SET capabilities = CASE role
    WHEN 'OWNER'   THEN '["chat","routine.create","skill.create","credential.create","credential.rotate","issue.create","memory.write"]'
    WHEN 'ADMIN'   THEN '["chat","routine.create","skill.create","credential.create","credential.rotate","issue.create","memory.write"]'
    WHEN 'MANAGER' THEN '["chat","routine.create","issue.create","memory.write"]'
    WHEN 'MEMBER'  THEN '["chat"]'
    WHEN 'VIEWER'  THEN '["chat"]'
    ELSE '["chat"]'
END
WHERE capabilities IS NULL;

CREATE INDEX IF NOT EXISTS idx_workspace_member_capabilities
    ON workspace_members(workspace_id)
    WHERE capabilities IS NOT NULL AND capabilities != '["chat"]';
`
