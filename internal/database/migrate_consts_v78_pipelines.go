package database

// migrationAddPipelines (v78) introduces the pipelines primitive — a
// declarative DSL document persisted per-workspace, authored by AI
// agents (or users) and reusable across crews via the
// [AVAILABLE PIPELINES] system-prompt block.
//
// Design notes (see .claude/context/prd/PIPELINES.md for full spec):
//
//   - id is a CUID with the "pln_" prefix. The prefix is decorative
//     only — application code is the source of truth — but it makes
//     pipeline IDs visually distinct from agent / crew / run IDs in
//     logs, journal entries, and graph nodes.
//
//   - slug is workspace-scoped unique. Logical name (e.g.
//     "email-fetch-summarize") used in [AVAILABLE PIPELINES] block,
//     in DSL call_pipeline references, and in URLs. Decoupled from
//     id so marketplace import can rename collisions without
//     breaking cross-pipeline references that resolve via slug.
//
//   - definition_json holds the full DSL document; definition_hash
//     is sha256(definition_json) for cheap dedup at save time and
//     for marketplace integrity checks later.
//
//   - ephemeral=1 marks pipelines auto-generated from cross-crew
//     delegation wraps (planned Phase 2 observability layer). They
//     are excluded from [AVAILABLE PIPELINES] block via the
//     idx_pipelines_workspace_visible partial index. Default is 0
//     so all MVP saves are user/agent-visible by default.
//
//   - workspace_visible toggles whether the pipeline appears in
//     other crews' system prompts. Default 1; flipping to 0 makes
//     a pipeline private to its author crew (Phase 2 permissions
//     will replace this binary toggle with a per-crew allow list).
//
//   - last_test_run_at + last_test_run_passed enforce the "test-run
//     gate before save" invariant. The save handler refuses to
//     persist a row unless a fresh (within 5 min) test_run passed.
//     This keeps brittle pipelines out of the workspace registry.
//
//   - execution_tier_json is a per-pipeline override of the
//     workspace-level tier mapping. NULL means "fall back to
//     workspaces.execution_tiers_json defaults".
//
//   - Authorship metadata: author_crew_id, author_agent_id,
//     author_user_id, author_chat_id, author_run_id, authored_via,
//     imported_from_url. Captures the full provenance chain so the
//     UI can deeplink from a pipeline back to the conversation
//     where the agent emitted it (or the user who wrote it). All
//     FKs ON DELETE SET NULL — losing the author should not orphan
//     the pipeline; provenance just becomes "unknown".
//
// Pipeline runs are intentionally NOT given their own table in MVP.
// They are logged into existing journal_entries with synthetic
// entry types (pipeline.run.started, pipeline.step.completed, etc.).
// This keeps the schema footprint small and gives pipeline runs
// free visibility in Journal + Graph views without parallel
// plumbing. A dedicated pipeline_runs table may be extracted in
// Phase 2 once query patterns stabilise.
//
// workspaces.execution_tiers_json is added by this same migration.
// The default value seeds four tiers (trivial / fast / moderate /
// smart) keyed to current Claude model IDs. Workspaces created
// after this migration inherit the same default via a column
// default; existing workspaces are backfilled in the migration body.
const migrationAddPipelines = `
CREATE TABLE IF NOT EXISTS pipelines (
    id                       TEXT PRIMARY KEY,
    workspace_id             TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    slug                     TEXT NOT NULL,
    name                     TEXT NOT NULL,
    description              TEXT,
    dsl_version              TEXT NOT NULL DEFAULT '1.0',
    definition_json          TEXT NOT NULL,
    definition_hash          TEXT NOT NULL,
    ephemeral                INTEGER NOT NULL DEFAULT 0,
    workspace_visible        INTEGER NOT NULL DEFAULT 1,
    invocation_count         INTEGER NOT NULL DEFAULT 0,
    last_invoked_at          TEXT,
    last_invocation_status   TEXT,
    author_crew_id           TEXT REFERENCES crews(id) ON DELETE SET NULL,
    author_agent_id          TEXT REFERENCES agents(id) ON DELETE SET NULL,
    author_user_id           TEXT REFERENCES users(id) ON DELETE SET NULL,
    author_chat_id           TEXT,
    author_run_id            TEXT,
    authored_via             TEXT NOT NULL DEFAULT 'agent_tool_call'
                               CHECK (authored_via IN ('agent_tool_call','user_api','imported','seed')),
    imported_from_url        TEXT,
    last_test_run_at         TEXT,
    last_test_run_passed     INTEGER NOT NULL DEFAULT 0,
    execution_tier_json      TEXT,
    created_at               TEXT NOT NULL DEFAULT (datetime('now','subsec')),
    updated_at               TEXT NOT NULL DEFAULT (datetime('now','subsec')),
    deleted_at               TEXT,
    UNIQUE (workspace_id, slug)
);

CREATE INDEX IF NOT EXISTS idx_pipelines_workspace
    ON pipelines (workspace_id)
    WHERE deleted_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_pipelines_workspace_visible
    ON pipelines (workspace_id, workspace_visible)
    WHERE deleted_at IS NULL AND ephemeral = 0;

CREATE INDEX IF NOT EXISTS idx_pipelines_author_crew
    ON pipelines (author_crew_id)
    WHERE deleted_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_pipelines_invocation_count
    ON pipelines (workspace_id, invocation_count DESC)
    WHERE deleted_at IS NULL;

ALTER TABLE workspaces
    ADD COLUMN execution_tiers_json TEXT NOT NULL DEFAULT '{"trivial":{"primary":{"adapter":"claude","model":"claude-haiku-4-5-20251001"}},"fast":{"primary":{"adapter":"claude","model":"claude-haiku-4-5-20251001"},"fallback":[{"adapter":"claude","model":"claude-sonnet-4-6"}]},"moderate":{"primary":{"adapter":"claude","model":"claude-sonnet-4-6"}},"smart":{"primary":{"adapter":"claude","model":"claude-opus-4-7"}}}';
`
