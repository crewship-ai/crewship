-- #2274: scope message_feedback's dedupe key to the workspace.
--
-- UNIQUE(message_id, user_id, signal) (v96) is instance-wide. A forked
-- restore (--as-workspace / --as-crew) carries the same feedback rows
-- with the same (message_id, user_id, signal) — `messages` is not in
-- BackupTables so message_id is never remapped, and users are
-- deliberately non-remappable — so the fork's rows collide with the
-- source's on the same instance and INSERT OR IGNORE drops every one of
-- them in silence (#2274).
--
-- The table already carries workspace_id NOT NULL (denormalised for
-- scope-aware cascade deletes, per the v96 comment), so widening the
-- unique key to (workspace_id, message_id, user_id, signal) puts each
-- workspace's feedback in a namespace of its own and loses nothing:
-- one user still cannot file the same signal twice on the same message
-- within one workspace.
--
-- SQLite cannot alter a UNIQUE constraint in place — it lives in the
-- table definition, not a named index — so this is the documented
-- rebuild: new table, copy, drop, rename, recreate the indexes. Nothing
-- references message_feedback (no table carries a REFERENCES
-- message_feedback clause), so the rebuild is safe inside the wrapper
-- transaction with foreign_keys on: the implicit DELETE FROM before
-- DROP has no children to cascade to, and the copy's own FKs
-- (workspaces/chats/users) all resolve.
--
-- The API upsert's ON CONFLICT target is updated to
-- (workspace_id, message_id, user_id, signal) in the same change.

CREATE TABLE message_feedback_rebuild (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    chat_id TEXT REFERENCES chats(id) ON DELETE CASCADE,
    message_id TEXT NOT NULL,
    trace_id TEXT,
    signal TEXT NOT NULL CHECK (signal IN ('helpful','not_helpful','inaccurate','unsafe','edit','regenerate')),
    reason TEXT,
    user_id TEXT REFERENCES users(id) ON DELETE SET NULL,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    UNIQUE(workspace_id, message_id, user_id, signal)
);

INSERT INTO message_feedback_rebuild
    (id, workspace_id, chat_id, message_id, trace_id, signal, reason, user_id, created_at)
SELECT id, workspace_id, chat_id, message_id, trace_id, signal, reason, user_id, created_at
FROM message_feedback;

DROP TABLE message_feedback;
ALTER TABLE message_feedback_rebuild RENAME TO message_feedback;

-- The four indexes v96 and 20260810154153 put on the original table;
-- they died with the DROP and have to be recreated on the rebuilt one.
CREATE INDEX IF NOT EXISTS idx_feedback_trace ON message_feedback(trace_id) WHERE trace_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_feedback_ws_created ON message_feedback(workspace_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_feedback_message ON message_feedback(message_id);
CREATE INDEX IF NOT EXISTS idx_message_feedback_chat ON message_feedback(chat_id);
