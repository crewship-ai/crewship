package database

// migrationInboxKinds (v168) widens the inbox_items.kind CHECK to admit
// 'schedule_circuit_breaker_tripped'.
//
// Bug this fixes: internal/pipeline/schedules.go raises an inbox alert when
// a schedule is auto-disabled after N consecutive failures (#1405). It wrote
// the bare literal "schedule_circuit_breaker_tripped", which no migration
// had ever added to the CHECK — v90 widened the set to include
// 'memory_consolidation' and v162 to include 'schedule_missed', but nothing
// ever admitted the circuit-breaker value.
//
// The failure was invisible. inbox.Insert LOGS its error rather than
// propagating it to any user-visible path, so in production every one of
// these inserts failed the constraint and was swallowed into a Warn line.
// The cost-bleed protection worked; the alert telling a human "your routine
// has been disabled and is no longer running" reached nobody.
//
// It also escaped test coverage: internal/pipeline's rig
// (openPinningTestDB) builds a hand-rolled inbox_items with no CHECK, so
// schedules_circuit_breaker_test.go asserted the alert landed and passed
// green. That rig is corrected in the same change, and
// TestInboxKindsMatchSchema (migrate_v168_inbox_kinds_test.go) now inserts
// every inbox.AllKinds value against the REAL migrated schema so the code's
// vocabulary and the constraint can never silently diverge again.
//
// SQLite has no ALTER CHECK, so the table is recreated per the documented
// pattern (see v90, v162): create _new with the widened constraint, copy
// positionally, drop, rename, recreate all four indexes. No triggers exist
// on this table (verified against the migrated schema), so none are
// restored.
//
// The kind list here must stay in sync with internal/inbox.AllKinds — the
// totality guard fails CI if it drifts.
const migrationInboxKinds = `
CREATE TABLE inbox_items_new (
    id                  TEXT PRIMARY KEY,
    workspace_id        TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    kind                TEXT NOT NULL
                          CHECK (kind IN ('waitpoint', 'escalation', 'failed_run', 'message',
                                          'memory_consolidation', 'schedule_missed',
                                          'schedule_circuit_breaker_tripped')),
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
