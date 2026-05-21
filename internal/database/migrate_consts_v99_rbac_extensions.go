package database

// migrationRBACExtensions (v99) widens the RBAC surface beyond the
// flat workspace_members.role model. The pre-v99 system gave every
// MANAGER+ workspace member full read/write across every crew, every
// agent, every credential — which is the right starting point for a
// solo workspace but too coarse the moment a team grows past three
// or four members.
//
// The pattern follows GitHub's fine-grained PAT + per-team role
// model and GitLab's per-project membership: workspace role is the
// floor, per-crew role can elevate within that crew (but never
// exceed the workspace role), agent has an explicit creator/owner
// so a MANAGER can't accidentally delete a peer's work, and CLI
// tokens carry an optional scope set so an automation token can be
// narrower than the user's full permission set.
//
// All columns are additive with sensible defaults so existing rows
// keep their current behaviour after migration.
//
// Schema changes:
//
//   - crew_members.role: per-crew role override. NULL = inherit
//     workspace role (the pre-v99 behaviour). Non-NULL value can
//     only ELEVATE the workspace role, never drop below it — the
//     effective-role helper takes max(workspace, crew). The check
//     constraint pins the allowed values to the same set
//     workspace_members already uses.
//
//   - agents.created_by_user_id: the user who created the agent.
//     Backfilled to NULL for pre-v99 rows so the per-agent edit
//     gate degrades to workspace-role-only for those (no surprise
//     ownership swap on existing fleet). New rows write the
//     creating user's id; the edit gate in canEditAgent uses this
//     to let MANAGER+ users edit their own agents without giving
//     them blanket rights over peer agents.
//
//   - cli_tokens.scopes: JSON array of scope strings the token is
//     restricted to. NULL = unrestricted (token carries the user's
//     full workspace role, pre-v99 behaviour). A list narrows the
//     token to a subset. Server-side validation enforces that
//     requested scopes ≤ user's max permissions at issue time;
//     a downgrade of the user's role after issue cannot grant the
//     token NEW permissions (the scope acts as a CEILING, not a
//     guarantee).
//
// What this migration does NOT do:
//
//   - Custom workspace roles (still hardcoded OWNER/ADMIN/MANAGER/
//     MEMBER/VIEWER). That would be Phase 2 (custom_roles table +
//     role_permissions join) and probably an EE-only feature.
//
//   - Per-resource ACL rows for credentials, MCP servers, etc.
//     Those are scope-controllable from the CLI-token side now;
//     full per-row ACL is Phase 3.
//
//   - Policy-as-code (Casbin / OPA). Deferred — too much
//     conceptual surface for an MVP that primarily wants "OWNER
//     vs MANAGER vs MEMBER" to work right.
const migrationRBACExtensions = `
-- Per-crew role override.
ALTER TABLE crew_members ADD COLUMN role TEXT
    CHECK(role IS NULL OR role IN ('OWNER','ADMIN','MANAGER','MEMBER','VIEWER'));

CREATE INDEX IF NOT EXISTS idx_crew_member_role
    ON crew_members(crew_id, role) WHERE role IS NOT NULL;

-- Per-agent owner. NULL for pre-v99 agents (no owner attribution).
ALTER TABLE agents ADD COLUMN created_by_user_id TEXT
    REFERENCES users(id) ON DELETE SET NULL;

CREATE INDEX IF NOT EXISTS idx_agent_creator
    ON agents(created_by_user_id) WHERE created_by_user_id IS NOT NULL;

-- CLI token scopes. JSON array literal, NULL = unrestricted.
ALTER TABLE cli_tokens ADD COLUMN scopes TEXT;
`
