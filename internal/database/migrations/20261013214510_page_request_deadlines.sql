-- This is a job admission deadline, never evidence that its runtime stopped.
-- Empty legacy values are derived explicitly from durable admission timestamps.
ALTER TABLE page_artifact_requests ADD COLUMN deadline_at TEXT NOT NULL DEFAULT '';
ALTER TABLE page_board_requests ADD COLUMN deadline_at TEXT NOT NULL DEFAULT '';
