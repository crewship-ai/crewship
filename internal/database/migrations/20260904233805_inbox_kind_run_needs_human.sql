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
