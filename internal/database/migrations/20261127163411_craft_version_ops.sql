-- The operations an agent change of a workbook Sheet applied, saved with the version they made,
-- so an editor holding unsaved work replays them over its own state instead of
-- reloading. ops and inverse carry cell content: they are never logged
-- or audited and go with the version (same retention). Additive, no backfill: a version without
-- a row is not replayable.
CREATE TABLE IF NOT EXISTS craft_version_ops (
 workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 page_id TEXT NOT NULL REFERENCES pages(id) ON DELETE CASCADE,
 version INTEGER NOT NULL CHECK(version > 1),
 base_version INTEGER NOT NULL CHECK(base_version > 0 AND base_version < version),
 actor_kind TEXT NOT NULL CHECK(actor_kind IN ('agent','human','system')),
 actor_id TEXT NOT NULL,
 actor_name TEXT NOT NULL DEFAULT '',
 operation_id TEXT NOT NULL,
 engine_pin TEXT NOT NULL,
 calculate TEXT NOT NULL CHECK(calculate IN ('saved','calculated')),
 replayable INTEGER NOT NULL CHECK(replayable IN (0,1)),
 ops TEXT NOT NULL,
 inverse TEXT NOT NULL DEFAULT '[]',
 digest TEXT,
 created_at TEXT NOT NULL,
 PRIMARY KEY(workspace_id, page_id, version),
 UNIQUE(workspace_id, page_id, operation_id)
);
CREATE INDEX IF NOT EXISTS craft_version_ops_page ON craft_version_ops(page_id);
CREATE TRIGGER IF NOT EXISTS craft_version_ops_immutable BEFORE UPDATE ON craft_version_ops
BEGIN SELECT RAISE(ABORT,'craft version operations are immutable'); END;
