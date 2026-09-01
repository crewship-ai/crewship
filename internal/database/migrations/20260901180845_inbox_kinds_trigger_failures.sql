-- Widen inbox_items.kind to admit 'webhook_fire_failed' and
-- 'automation_enqueue_failed'.
--
-- PRD-ISSUES-AND-ROUTINES-2026.md §17 A4 ("Trigger failure is visible for
-- all three trigger kinds") / F20: schedule failures already raise a
-- journal entry AND a MANAGER inbox card (internal/pipeline/schedules.go);
-- webhook fire failures wrote a DB row only, and automation enqueue
-- failures produced a bare logger.Error — neither reached a human. This
-- migration adds the two inbox kinds the new alert paths write:
--
--   - webhook_fire_failed      (internal/api/pipeline_webhooks.go,
--     alertWebhookFireFailure)
--   - automation_enqueue_failed (internal/automation/registry.go,
--     Registry.emitEnqueueFailed)
--
-- Both are written via inbox.Upsert (not Insert): the same webhook or
-- automation can trip the alert more than once across its life — fail,
-- recover, fail again — and each trip is news about the SAME subject
-- (source_id = webhook id / automation id) rather than a fresh one-off
-- event, so a card a human already resolved is resurrected to unread
-- instead of being silently swallowed by the (kind, source_id) unique
-- index the way a second Insert would be.
--
-- SQLite has no ALTER CHECK, so the table is recreated per the documented
-- pattern (v90, v162, v168/20260728110000): create _new with the widened
-- constraint, copy positionally, drop, rename, recreate all four indexes.
-- No triggers exist on this table.
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
                                          'webhook_fire_failed', 'automation_enqueue_failed')),
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
