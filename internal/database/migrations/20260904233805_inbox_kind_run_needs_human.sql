-- Widen inbox_items.kind to admit 'run_needs_human'.
--
-- PRD-ISSUES-AND-ROUTINES-2026.md §9.6/§12, work package B6 (#2349): a run
-- (an issue-session assignment, or a routine/pipeline run) whose outcome
-- contract resolved to NEEDS_HUMAN is exactly the one outcome that reaches
-- the inbox — "blocked on a decision, input or credential" — with an
-- action contract in payload_json (attention_class, thread_key, actions,
-- who_can_act, context). See internal/api/issue_outcome_inbox.go.
--
-- SQLite has no ALTER CHECK, so the table is recreated per the documented
-- pattern (v90, v162, v168/20260728110000, 20260901180845): create _new
-- with the widened constraint, copy positionally, drop, rename, recreate
-- every index. No triggers exist on this table.
--
-- The kind list here must stay in sync with internal/inbox.AllKinds —
-- TestInboxKindsMatchSchema (internal/database) fails CI if it drifts.
--
-- inbox_item_reads.inbox_item_id REFERENCES inbox_items(id) ON DELETE
-- CASCADE (20260902071500, added AFTER every prior inbox_items rebuild —
-- none of them had this dependent yet, which is why this migration is the
-- first one where the pattern above is unsafe as written). This runs as a
-- plain .sql file, wrapped in the migration runner's own transaction with
-- foreign_keys=ON throughout — file migrations have no fnNoTx escape hatch
-- (that is a Go-only option; see migrate.go's migration.fnNoTx doc comment)
-- to toggle the pragma around the rebuild, and PRAGMA foreign_keys=OFF is a
-- no-op inside an open transaction regardless. DROP TABLE inbox_items
-- therefore cascades against every inbox_item_reads row that references it
-- — verified directly (Python's sqlite3, and a scaled reproduction of this
-- exact schema): the cascade fires against the DROPPED table object even
-- though the "same" ids reappear moments later under the renamed table, so
-- doing nothing here would silently erase every read marker on upgrade.
-- Save the rows to a TEMP table before the drop and restore them after the
-- rename — inbox_item_reads itself is never dropped or rebuilt, so its rows
-- reinsert cleanly once inbox_items exists again with the same ids.
CREATE TEMP TABLE _inbox_item_reads_preserved AS SELECT * FROM inbox_item_reads;

CREATE TABLE inbox_items_new (
    id                  TEXT PRIMARY KEY,
    workspace_id        TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    kind                TEXT NOT NULL
                          CHECK (kind IN ('waitpoint', 'escalation', 'failed_run', 'message',
                                          'memory_consolidation', 'schedule_missed',
                                          'schedule_circuit_breaker_tripped',
                                          'webhook_fire_failed', 'automation_enqueue_failed',
                                          'run_needs_human')),
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
    created_at          TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    updated_at          TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    data_subject_id     TEXT
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

CREATE INDEX IF NOT EXISTS idx_inbox_items_subject_ws
    ON inbox_items (data_subject_id, workspace_id)
    WHERE data_subject_id IS NOT NULL;

-- Restore the read markers the rebuild's cascade removed, now that
-- inbox_items exists again with the same ids.
INSERT INTO inbox_item_reads (inbox_item_id, user_id, read_at)
    SELECT inbox_item_id, user_id, read_at FROM _inbox_item_reads_preserved;
DROP TABLE _inbox_item_reads_preserved;
