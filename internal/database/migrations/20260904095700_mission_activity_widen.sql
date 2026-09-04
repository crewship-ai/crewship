-- Widen mission_activity into the one event log per issue (PRD-ISSUES-AND-
-- ROUTINES-2026 §9.1, work package B1).
--
-- mission_activity (v41, internal/database/migrate_consts_v33_v41.go) already
-- has mission_id, the actor_type CHECK this design wants, actor_id, action,
-- details, created_at, and one central writer (internal/api/issue_events.go).
-- Today it is a status-change audit table, not an ordered event log — this
-- migration is the genuine change of purpose §9.1 calls for: a per-mission
-- sequence number every reader can cursor on, a payload for the automation
-- matcher and the future context-assembly delta (§11.1), a source pointer for
-- events not authored by a human/agent PATCH, and a workspace_id so the table
-- can stand on its own instead of relying on the mission chain (I7, F55).
--
-- ── Why a rebuild ───────────────────────────────────────────────────────
--
-- Four of the five additions (workspace_id, seq, payload_json, source_kind,
-- source_id) are plain nullable ADD COLUMNs and the UNIQUE(mission_id, seq)
-- constraint they need is a CREATE UNIQUE INDEX — SQLite allows any number of
-- NULL seq values under a unique index, so that part costs no rebuild at all.
--
-- The CHECK on `action` is the one piece that does: `action` is an EXISTING
-- NOT NULL column with no CHECK today, and SQLite has no ALTER TABLE that
-- adds a constraint to an existing column — only DROP/ADD COLUMN and RENAME
-- are supported in place. The keeper_runtime_settings rebuild
-- (20260801150210_keeper_escalate_from_rebuild.sql) is the precedent this
-- migration copies: recreate the table with every existing column and
-- constraint carried over verbatim, plus the new ones, then copy the data
-- across under foreign_keys=OFF so mid-rebuild FK checks against the
-- half-built table don't fire.
--
-- ── The action vocabulary ──────────────────────────────────────────────
--
-- issueAction (internal/api/issue_events.go) is the closed set every writer
-- already draws from; knownIssueActions is its own test-enforced
-- enumeration. The CHECK below is that same list, PLUS 'task_cancelled' —
-- a value assignments_run.go's bypass writer (the CANCELLED branch of its
-- run-completion handler) has been writing since before this PRD and that
-- issueAction never named. Leaving it out would turn a live, working write
-- path into a constraint violation the moment this migration applied; a
-- CHECK is a poor place to relitigate a vocabulary gap it did not create.
-- 'commented' is included even though nothing writes it today (comments go
-- through mission_comments, not this table) — it is already reserved by
-- journalTypeForIssueAction and knownIssueActions, and excluding it here
-- would just move today's non-writer into a second migration the day
-- something finally does write it.
--
-- ── source_kind / source_id ─────────────────────────────────────────────
--
-- Nullable pair, indexed together: "what produced this row, and which
-- instance of it" — a run id, a routine fire, a webhook delivery — for
-- readers that need to walk from an event back to its cause without parsing
-- `details` prose. Nothing sets it in this migration's write path yet; B2's
-- delivery/wake loop is the first producer with a source worth naming.
--
-- Column order matches the original table with the new columns appended, so
-- BackupTableIntent's positional expectations for any pre-existing dump/
-- restore code that names columns explicitly are undisturbed.

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
        'code_link_removed'
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
    (id, mission_id, actor_type, actor_id, action, details, created_at)
SELECT id, mission_id, actor_type, actor_id, action, details, created_at
FROM mission_activity;

DROP TABLE mission_activity;
ALTER TABLE mission_activity_new RENAME TO mission_activity;

CREATE INDEX IF NOT EXISTS idx_mission_activity_mission ON mission_activity(mission_id);
CREATE INDEX IF NOT EXISTS idx_mission_activity_created ON mission_activity(created_at);

-- workspace_id: "every event in this workspace" without a join through
-- missions (I7's point — a deleted/foreign mission must not be required to
-- scope a read).
CREATE INDEX IF NOT EXISTS idx_mission_activity_workspace ON mission_activity(workspace_id);

-- seq is the per-mission cursor §11.1's context assembly and B1's own
-- "reuse, don't duplicate" session lookups are built on: "everything above
-- last_consumed_seq is unread for this session". NULL is legal (existing
-- rows before the backfill migration, and any legacy write that still races
-- ahead of it) — SQLite's UNIQUE index treats every NULL as distinct from
-- every other, so this is the guarantee for POPULATED rows only, which is
-- exactly the rows a cursor ever reads.
CREATE UNIQUE INDEX IF NOT EXISTS idx_mission_activity_mission_seq ON mission_activity(mission_id, seq);

-- "which event did this run/delivery/routine-fire produce" — the reverse of
-- source_kind/source_id being embedded in the row.
CREATE INDEX IF NOT EXISTS idx_mission_activity_source ON mission_activity(source_kind, source_id);

PRAGMA foreign_keys = ON;
