-- A chat editor's original submission and the publication it actually made.
-- A null version is a retirement and prevents a late save with the same ID.
CREATE TABLE craft_realtime_attempts (
 page_id TEXT NOT NULL REFERENCES pages(id) ON DELETE CASCADE,
 workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 session_id TEXT NOT NULL,
 attempt_id TEXT NOT NULL,
 boot_id TEXT NOT NULL,
 deadline_ns INTEGER NOT NULL,
 submitted_base INTEGER NOT NULL,
 editor_revision TEXT NOT NULL,
 submitted_hash TEXT NOT NULL,
 actual_base INTEGER,
 version INTEGER,
 content_hash TEXT,
 metadata TEXT,
 PRIMARY KEY(page_id,session_id,attempt_id)
);
CREATE INDEX craft_realtime_attempts_expiry ON craft_realtime_attempts(boot_id,deadline_ns);

-- Native chat publications carry the original chat and, for an agent, run.
-- Older workbook journals stay readable and do not prove a real-time merge.
ALTER TABLE craft_version_ops ADD COLUMN chat_id TEXT;
ALTER TABLE craft_version_ops ADD COLUMN run_id TEXT;
