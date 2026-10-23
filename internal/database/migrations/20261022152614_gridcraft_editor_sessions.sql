-- Live workbook editing (experimental, default off). An editor session binds one
-- client window to its human user, the credential that opened it, one Page and
-- the workbook generation. Human cell edits name their session as the writer
-- and are refused once it has expired or been closed. Sessions are runtime
-- state: they never travel in a backup and are dropped with their workbook.
CREATE TABLE gridcraft_sessions (
 session_id TEXT PRIMARY KEY CHECK(length(session_id) BETWEEN 1 AND 128 AND session_id NOT GLOB '*[^A-Za-z0-9_-]*'),
 workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 page_id TEXT NOT NULL,
 user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 credential TEXT NOT NULL CHECK(length(credential) BETWEEN 1 AND 300),
 generation INTEGER NOT NULL CHECK(generation>0),
 created_at INTEGER NOT NULL,
 expires_at INTEGER NOT NULL CHECK(expires_at>created_at),
 closed_at INTEGER,
 FOREIGN KEY(workspace_id,page_id) REFERENCES gridcraft_heads(workspace_id,page_id) ON DELETE CASCADE
);
CREATE INDEX gridcraft_sessions_page_user ON gridcraft_sessions(page_id,user_id,expires_at);
-- Foreign keys are indexed so user, workspace and workbook deletion cascade cheaply.
CREATE INDEX gridcraft_sessions_user ON gridcraft_sessions(user_id);
CREATE INDEX gridcraft_sessions_workspace_page ON gridcraft_sessions(workspace_id,page_id);
-- The outbox sweeper reads only undelivered revisions.
CREATE INDEX gridcraft_outbox_pending ON gridcraft_outbox(page_id,rev) WHERE state='pending';
-- Workspace kill switch for the live editor surface; off unless overridden.
INSERT OR IGNORE INTO feature_flags (id, key, description, enabled, percentage)
VALUES ('ffl_gridcraft_editor', 'gridcraft_editor',
        'Experimental live workbook editing: editor sessions, cell edits and the commits feed', 0, 0);
