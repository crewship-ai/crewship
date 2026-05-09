package database

// migrationAddInboxItems (v85) introduces a unified inbox_items table —
// the canonical "stuff that needs the human" surface that replaces
// scattered queries against pipeline_waitpoints, escalations, and
// (eventually) failed runs.
//
// Design choices:
//
//   - Single table with a `kind` discriminator + JSON payload, instead
//     of a polymorphic FK or one-table-per-kind. The list query
//     (workspace_id + state) is the hot path, and a single B-tree
//     index covers it cleanly. New kinds drop in by extending the
//     CHECK constraint.
//
//   - source_id is a text pointer back to the originating row in the
//     authoritative table for that kind (waitpoint token, escalation
//     id, run id, etc.). The write-through hooks in API land
//     guarantee the inbox row is kept in sync; the source remains the
//     source of truth for kind-specific business logic until phase 2
//     drops the old tables in favour of inbox_items.
//
//   - Lifecycle is Linear-Triage style — three states only:
//     unread → read → resolved. No archive, no flag, no snooze. If
//     it's resolved it stays visible (greyed) for 7 days so the user
//     has a "what just happened" feed; a follow-up cron job is what
//     would prune it (we deliberately don't auto-prune here so the
//     audit story is intact for now).
//
//   - target_user_id is nullable so workspace-wide notifications
//     (e.g. "any OWNER can approve") don't need a fan-out row per
//     user. The query at read time scopes by (target_user_id IS NULL
//     OR target_user_id = ?). target_role lets the inbox semantics
//     match Crewship's "MANAGER+ can approve" gates without naming
//     specific users.
//
//   - Backfill on creation: we walk pipeline_waitpoints (status =
//     pending) and escalations (status = PENDING) once at migration
//     time so the inbox lights up on first deploy with the work that's
//     already queued. Going forward the API write-through keeps it
//     synced; failed runs are NOT backfilled (too noisy historically;
//     only new failures populate so the inbox doesn't open with a
//     dump of yesterday's transient errors).
const migrationAddInboxItems = `
CREATE TABLE IF NOT EXISTS inbox_items (
    id                  TEXT PRIMARY KEY,
    workspace_id        TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    kind                TEXT NOT NULL
                          CHECK (kind IN ('waitpoint', 'escalation', 'failed_run', 'message')),
    source_id           TEXT NOT NULL,                                   -- pointer back to waitpoint token / escalation id / run id
    target_user_id      TEXT,                                            -- NULL = anyone in workspace
    target_role         TEXT,                                            -- e.g. 'OWNER' / 'MANAGER' / NULL
    title               TEXT NOT NULL,                                   -- human-readable summary line
    body_md             TEXT,                                            -- markdown body (optional)
    sender_type         TEXT,                                            -- 'agent' | 'crew' | 'system' | 'pipeline'
    sender_id           TEXT,
    sender_name         TEXT,
    state               TEXT NOT NULL DEFAULT 'unread'
                          CHECK (state IN ('unread', 'read', 'resolved')),
    priority            TEXT NOT NULL DEFAULT 'medium'
                          CHECK (priority IN ('urgent', 'high', 'medium', 'low')),
    blocking            INTEGER NOT NULL DEFAULT 1,                      -- 1 = needs explicit action, 0 = informational
    payload_json        TEXT NOT NULL DEFAULT '{}',                      -- kind-specific structured data
    read_at             TEXT,
    read_by_user_id     TEXT,
    resolved_at         TEXT,
    resolved_by_user_id TEXT,
    resolved_action     TEXT,                                            -- 'approved' / 'rejected' / 'retried' / 'cancelled' / etc.
    created_at          TEXT NOT NULL DEFAULT (datetime('now','subsec')),
    updated_at          TEXT NOT NULL DEFAULT (datetime('now','subsec'))
);

-- Hot-path index: workspace inbox feed sorted by created_at DESC.
CREATE INDEX IF NOT EXISTS idx_inbox_items_workspace_state_created
    ON inbox_items (workspace_id, state, created_at DESC);

-- Bell badge count (unread per workspace) — partial index keeps it tiny.
CREATE INDEX IF NOT EXISTS idx_inbox_items_unread
    ON inbox_items (workspace_id)
    WHERE state = 'unread';

-- Write-through dedup: the hook needs to find an existing row by
-- (kind, source_id) before re-inserting (e.g. waitpoint approve flips
-- state on the existing inbox_item rather than creating a duplicate).
CREATE UNIQUE INDEX IF NOT EXISTS idx_inbox_items_kind_source
    ON inbox_items (kind, source_id);

-- Backfill currently-open waitpoints. The 'unread' default puts each
-- one into the bell on first deploy — appropriate because the user
-- hasn't seen them in a unified view yet. payload_json carries the
-- waitpoint-specific fields the UI / actions need.
INSERT OR IGNORE INTO inbox_items (
    id, workspace_id, kind, source_id, target_role, title, body_md,
    sender_type, blocking, payload_json, created_at, updated_at
)
SELECT
    'ibx_wp_' || token,
    workspace_id,
    'waitpoint',
    token,
    'MANAGER',
    'Waitpoint pending approval',
    COALESCE(prompt, ''),
    'pipeline',
    1,
    json_object(
        'kind', kind,
        'pipeline_run_id', pipeline_run_id,
        'step_id', step_id,
        'invoking_crew_id', invoking_crew_id,
        'timeout_at', timeout_at
    ),
    created_at,
    created_at
FROM pipeline_waitpoints
WHERE status = 'pending';

-- Backfill currently-open escalations.
INSERT OR IGNORE INTO inbox_items (
    id, workspace_id, kind, source_id, target_role, title, body_md,
    sender_type, sender_id, blocking, payload_json, created_at, updated_at
)
SELECT
    'ibx_esc_' || id,
    workspace_id,
    'escalation',
    id,
    'MANAGER',
    'Agent escalation: ' || COALESCE(reason, 'unspecified'),
    COALESCE(context, ''),
    'agent',
    from_agent_id,
    1,
    json_object(
        'crew_id', crew_id,
        'chat_id', chat_id,
        'peer_conversation_id', peer_conversation_id,
        'reason', reason
    ),
    created_at,
    created_at
FROM escalations
WHERE status = 'PENDING';
`
