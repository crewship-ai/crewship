-- Admit `inbox_acted` to mission_activity's closed action set
-- (PRD-ISSUES-AND-ROUTINES-2026 §18 scenario 15, work package B15, #2389).
--
-- A person acting on a run_needs_human inbox card — answering the agent,
-- taking over, dismissing — leaves a receipt on the issue's event log
-- (§9.8: who, which action, which card, the session's agent_version, and
-- for an answer the delivery and run it produced). The log's action column
-- is a CHECK-constrained closed set (B1's widen migration), and SQLite
-- cannot alter a CHECK in place, so this is the same table rebuild B1 did —
-- now copying EVERY column, because since B1 the rows carry workspace_id,
-- seq, payload_json, source_kind and source_id that a partial copy would
-- silently null out.
--
-- No new table, no user text, no GDPR change (§16.1).

PRAGMA foreign_keys = OFF;

CREATE TABLE mission_activity_new (
    id           TEXT PRIMARY KEY,
    mission_id   TEXT NOT NULL REFERENCES missions(id) ON DELETE CASCADE,
    actor_type   TEXT NOT NULL CHECK(actor_type IN ('user','agent','system')),
    actor_id     TEXT NOT NULL,
    action       TEXT NOT NULL CHECK(action IN (
        'created', 'status_changed', 'assignee_changed', 'priority_changed',
        'parent_changed', 'relation_added', 'review_approved',
        'review_changes_requested', 'task_completed', 'task_failed',
        'task_cancelled', 'commented', 'description_changed', 'mentioned',
        'attachment_added', 'attachment_removed', 'code_link_added',
        'code_link_removed', 'inbox_acted'
    )),
    details      TEXT,
    created_at   TEXT NOT NULL DEFAULT (datetime('now')),
    workspace_id TEXT REFERENCES workspaces(id) ON DELETE CASCADE,
    seq          INTEGER,
    payload_json TEXT,
    source_kind  TEXT,
    source_id    TEXT
);

INSERT INTO mission_activity_new
    (id, mission_id, actor_type, actor_id, action, details, created_at,
     workspace_id, seq, payload_json, source_kind, source_id)
SELECT id, mission_id, actor_type, actor_id, action, details, created_at,
       workspace_id, seq, payload_json, source_kind, source_id
FROM mission_activity;

DROP TABLE mission_activity;
ALTER TABLE mission_activity_new RENAME TO mission_activity;

CREATE INDEX IF NOT EXISTS idx_mission_activity_created ON mission_activity(created_at);
CREATE INDEX IF NOT EXISTS idx_mission_activity_workspace ON mission_activity(workspace_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_mission_activity_mission_seq ON mission_activity(mission_id, seq);
CREATE INDEX IF NOT EXISTS idx_mission_activity_source ON mission_activity(source_kind, source_id);

PRAGMA foreign_keys = ON;
