package database

// migrationScheduleCatchup (v155) adds a per-schedule missed-run
// catch-up policy (issue #1422 item 2).
//
// Background: today an overdue schedule (scheduler was down, or the
// row's next_run_at fell behind for any reason) fires exactly ONCE on
// the next tick — the occurrence idempotency key
// (ScheduledFireIdempotencyKey) prevents a double-fire of that single
// occurrence, but any OTHER occurrences that also came due in the same
// gap are silently dropped with no record they ever existed.
//
// catchup_policy names that behaviour explicitly and gives it two more
// options:
//
//   - 'skip' — fire nothing for the backlog; just resume from the next
//     future occurrence.
//   - 'once' (default) — unchanged current behaviour: fire once for the
//     backlog, then resume from the next future occurrence.
//   - 'all'  — fire once per missed occurrence, oldest first (capped —
//     see maxCatchupFireOccurrences in schedules.go — so a schedule left
//     down for a long time can't runaway-loop the scheduler tick).
//
// last_missed_count records how many occurrences beyond the ones that
// fired were dropped on the most recent tick, so `schedules list` and
// the inbox can surface "N occurrences missed" instead of the gap being
// invisible.
//
// inbox_items.kind CHECK is widened (SQLite has no ALTER CHECK, so the
// table is recreated per the documented pattern — see v90) to admit
// 'schedule_missed', the new kind used to notify a workspace when a
// schedule drops backlog occurrences.
const migrationScheduleCatchup = `
ALTER TABLE pipeline_schedules ADD COLUMN catchup_policy TEXT NOT NULL DEFAULT 'once';
ALTER TABLE pipeline_schedules ADD COLUMN last_missed_count INTEGER NOT NULL DEFAULT 0;

CREATE TABLE inbox_items_new (
    id                  TEXT PRIMARY KEY,
    workspace_id        TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    kind                TEXT NOT NULL
                          CHECK (kind IN ('waitpoint', 'escalation', 'failed_run', 'message', 'memory_consolidation', 'schedule_missed')),
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
`
