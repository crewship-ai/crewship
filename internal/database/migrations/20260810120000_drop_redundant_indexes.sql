-- Drop eleven indexes that no query planner can ever choose, because a
-- longer index on the same table already begins with the same columns.
--
-- SQLite can use a multi-column index for any leading prefix of its columns:
-- an index on (workspace_id, created_at) answers `WHERE workspace_id = ?`
-- exactly as well as an index on (workspace_id) alone. So wherever both
-- exist, the shorter one is dead weight — never selected for a read, but
-- still fully maintained on every INSERT, UPDATE and DELETE.
--
-- These accumulated the ordinary way. Each single-column index was correct
-- when v01 added it; the composite that subsumed it arrived later, for a
-- query that needed the second column, and nothing went back to remove the
-- one it had just made redundant. Eleven of them now sit on the hottest
-- tables in the system — missions, chats, assignments, credentials,
-- journal_entries — so this is write amplification exactly where writes are
-- most contended, and on SQLite every one of those writes holds the single
-- database-wide write lock while it updates the extra B-tree.
--
-- Each DROP below names the index that covers it. The pairing was derived
-- mechanically from the live schema (compare index_info column lists, and
-- require the partial-index WHERE clauses to match exactly, since a partial
-- index only covers the rows its predicate admits).
--
-- Deliberately NOT dropped, though a naive column comparison would flag them:
--
--   * any UNIQUE index — those enforce a constraint, not just access speed,
--     and a covering non-unique index does not replace them;
--   * indexes whose covering candidate has a DIFFERENT partial WHERE clause,
--     which covers a different subset of rows and therefore covers nothing;
--   * expression indexes, whose indexed columns SQLite does not report, so
--     "is this a prefix of that" cannot be answered mechanically.
--
-- Reversible: every one of these can be recreated from the definitions in
-- migrate_consts_v01_init.go / migrate_consts_v42_v45.go / migrate.go if a
-- future query genuinely wants the narrower index. Nothing reads these names
-- at runtime — the only references in the tree are the original CREATE
-- statements and prose comments.

-- crew_members: covered by idx_crew_member_user_crew (user_id, crew_id).
DROP INDEX IF EXISTS idx_crew_member_user;

-- chats: covered by idx_chats_ws_created (workspace_id, created_at).
DROP INDEX IF EXISTS idx_chat_workspace;

-- chats: covered by idx_chats_agent_activity (agent_id, last_activity_at),
-- and also by idx_chat_agent_status_created (agent_id, status, created_at).
DROP INDEX IF EXISTS idx_chat_agent;

-- assignments: covered by idx_assignment_to_status (assigned_to_id, status).
DROP INDEX IF EXISTS idx_assignment_to;

-- credentials: covered by idx_credential_type_provider
-- (workspace_id, type, provider).
DROP INDEX IF EXISTS idx_credential_workspace;

-- peer_conversations: covered by idx_peer_conv_crew_created
-- (crew_id, created_at).
DROP INDEX IF EXISTS idx_peer_conv_crew;

-- missions: covered by idx_missions_ws_created (workspace_id, created_at),
-- and also by idx_mission_ws_type_status (workspace_id, mission_type, status).
DROP INDEX IF EXISTS idx_mission_workspace;

-- workflow_templates: covered by idx_workflow_templates_name_ws
-- (workspace_id, name).
DROP INDEX IF EXISTS idx_workflow_templates_ws;

-- pipelines: covered by idx_pipelines_workspace_status
-- (workspace_id, status). Both carry the same `WHERE deleted_at IS NULL`
-- predicate, which is what makes the coverage real rather than apparent.
DROP INDEX IF EXISTS idx_pipelines_workspace;

-- attachments: covered by idx_attachments_blob (workspace_id, sha256).
DROP INDEX IF EXISTS idx_attachments_workspace;

-- journal_entries: this one is not a prefix but an exact duplicate.
-- idx_journal_trace (v42-v45) and idx_journal_trace_id (v-in-migrate.go) are
-- the same index under two names — same column, same
-- `WHERE trace_id IS NOT NULL` predicate — created by two migrations that
-- did not know about each other. Both run unconditionally on every install,
-- fresh or upgraded, so dropping this one always leaves idx_journal_trace
-- behind; the trace lookup keeps its index either way.
DROP INDEX IF EXISTS idx_journal_trace_id;
