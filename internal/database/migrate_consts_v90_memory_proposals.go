package database

// migrationAddMemoryProposals (v90) introduces the HITL staging surface
// for the consolidator: instead of appending learned-YYYY-MM-DD.md
// (Originally authored as v89 in feat/memory-reliability-bundle;
// renumbered to v90 on rebase because main landed an unrelated
// cascade-triggers migration at v89 first. The two migrations are
// fully independent — both apply cleanly in either order.)
//
// directly, when CREWSHIP_CONSOLIDATE_HITL=1 the consolidator writes
// proposal-{runID}.md into {outputDir}/.proposed/ and inserts a row
// here. An operator approves via POST /api/v1/consolidate/proposed/{id}/approve
// — at which point the proposal content is merged into the canonical
// learned-*.md and the row flips to status='approved'.
//
// Design choices:
//
//   - One row per proposal (one consolidator tick produces one
//     proposal, regardless of rule count). Per-rule granularity would
//     fragment the UX — operators want to review "what did the LLM
//     extract this run", not pick individual rules.
//
//   - inbox_item_id is a soft link to the inbox row created at the
//     same time. The inbox row is the user-facing surface; this table
//     is the canonical state. Keeping both kept-in-sync via a
//     transaction at insert/approve/reject time mirrors the
//     write-through pattern v85 uses for waitpoints/escalations.
//
//   - status CHECK is strict: only pending -> approved / rejected,
//     no other transitions. Backend code is responsible for not
//     touching the row after it is in a terminal state.
//
//   - evidence_json carries the raw rules JSON the LLM returned so
//     the explain endpoint can rebuild the per-rule signal breakdown
//     without re-reading the proposal markdown.
//
//   - inbox_items CHECK constraint rebuild: SQLite has no ALTER CHECK,
//     so we recreate the table with the new value list and copy data.
//     The unique (kind, source_id) index and the workspace_state_created
//     index get rebuilt at the same time. The partial index for unread
//     items is rebuilt too. Existing rows are preserved verbatim.
//
//   - workspaces.memory_config TEXT column carries per-workspace
//     overrides as a JSON document: { "scrubber_mode": "block|warn",
//     "cap_bytes_agent_md": 4000, "cap_bytes_crew_md": 4000,
//     "cap_bytes_pins_md": 8000, "watcher_enabled": true,
//     "scrubber_allowlist": "regex" }. NULL means "use process-level
//     env defaults" — the path the application uses today.
const migrationAddMemoryProposals = `
CREATE TABLE IF NOT EXISTS memory_proposals (
    id                  TEXT PRIMARY KEY,
    workspace_id        TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    crew_id             TEXT NOT NULL REFERENCES crews(id) ON DELETE CASCADE,
    proposal_path       TEXT NOT NULL,
    status              TEXT NOT NULL DEFAULT 'pending'
                          CHECK (status IN ('pending', 'approved', 'rejected')),
    inbox_item_id       TEXT,
    evidence_json       TEXT NOT NULL DEFAULT '{}',
    rules_count         INTEGER NOT NULL DEFAULT 0,
    entries_scanned     INTEGER NOT NULL DEFAULT 0,
    created_at          TEXT NOT NULL DEFAULT (datetime('now','subsec')),
    decided_at          TEXT,
    decided_by_user_id  TEXT,
    -- HITL approval trail integrity: a terminal decision (approved/
    -- rejected) must record BOTH a timestamp and the user_id of the
    -- operator who made the call. Earlier we only enforced decided_at,
    -- which let a row terminate with no actor recorded — orphaning
    -- the audit trail required by SOC2/EU AI Act Art. 14 for HITL
    -- actions on memory.
    CHECK (
      (status = 'pending'  AND decided_at IS NULL AND decided_by_user_id IS NULL) OR
      (status IN ('approved','rejected') AND decided_at IS NOT NULL AND decided_by_user_id IS NOT NULL)
    )
);

CREATE INDEX IF NOT EXISTS idx_memory_proposals_ws_status_created
    ON memory_proposals (workspace_id, status, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_memory_proposals_crew
    ON memory_proposals (crew_id, status);

-- inbox_items: rebuild to widen the kind CHECK so 'memory_consolidation'
-- is an allowed inbox surface. The pattern follows SQLite's documented
-- ALTER-by-recreate steps: create _new with new schema, copy rows, drop
-- old, rename _new -> original. Indexes drop with the old table; we
-- recreate them on the new one. No FK targets inbox_items so the
-- copy/drop sequence is safe.
CREATE TABLE inbox_items_new (
    id                  TEXT PRIMARY KEY,
    workspace_id        TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    kind                TEXT NOT NULL
                          CHECK (kind IN ('waitpoint', 'escalation', 'failed_run', 'message', 'memory_consolidation')),
    source_id           TEXT NOT NULL,
    target_user_id      TEXT,
    target_role         TEXT,
    title               TEXT NOT NULL,
    body_md             TEXT,
    sender_type         TEXT,
    sender_id           TEXT,
    sender_name         TEXT,
    state               TEXT NOT NULL DEFAULT 'unread'
                          CHECK (state IN ('unread', 'read', 'resolved')),
    priority            TEXT NOT NULL DEFAULT 'medium'
                          CHECK (priority IN ('urgent', 'high', 'medium', 'low')),
    blocking            INTEGER NOT NULL DEFAULT 1,
    payload_json        TEXT NOT NULL DEFAULT '{}',
    read_at             TEXT,
    read_by_user_id     TEXT,
    resolved_at         TEXT,
    resolved_by_user_id TEXT,
    resolved_action     TEXT,
    created_at          TEXT NOT NULL DEFAULT (datetime('now','subsec')),
    updated_at          TEXT NOT NULL DEFAULT (datetime('now','subsec'))
);

INSERT INTO inbox_items_new SELECT * FROM inbox_items;

DROP TABLE inbox_items;
ALTER TABLE inbox_items_new RENAME TO inbox_items;

CREATE INDEX IF NOT EXISTS idx_inbox_items_workspace_state_created
    ON inbox_items (workspace_id, state, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_inbox_items_unread
    ON inbox_items (workspace_id)
    WHERE state = 'unread';

CREATE UNIQUE INDEX IF NOT EXISTS idx_inbox_items_kind_source
    ON inbox_items (kind, source_id);

-- Per-workspace memory policy override. NULL = follow process env defaults
-- (CREWSHIP_MEMORY_* variables); a JSON document overrides selectively.
ALTER TABLE workspaces ADD COLUMN memory_config TEXT;
`
