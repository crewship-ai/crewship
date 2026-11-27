-- The undo of an agent change of a workbook Sheet, one per operation: a repeat answers the same
-- version. Additive, no backfill.
CREATE TABLE IF NOT EXISTS craft_version_undos (
 workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 page_id TEXT NOT NULL REFERENCES pages(id) ON DELETE CASCADE,
 operation_id TEXT NOT NULL,
 version INTEGER NOT NULL CHECK(version > 1),
 skipped INTEGER NOT NULL CHECK(skipped >= 0),
 user_id TEXT REFERENCES users(id) ON DELETE SET NULL,
 created_at TEXT NOT NULL,
 PRIMARY KEY(workspace_id, page_id, operation_id)
);
CREATE INDEX IF NOT EXISTS craft_version_undos_page ON craft_version_undos(page_id);
CREATE INDEX IF NOT EXISTS craft_version_undos_user ON craft_version_undos(user_id);
