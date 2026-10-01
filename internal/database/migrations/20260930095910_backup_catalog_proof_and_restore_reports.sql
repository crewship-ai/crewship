-- Backups redesign, W0 (Track A): what the catalog knows about each bundle,
-- and a durable record of every restore.
--
-- backup_catalog gains:
--   kind             full | custom | environments | instance — what the bundle
--                    holds. Every bundle written so far is a full one.
--   plan_id          the backup plan that produced it (NULL: made by hand).
--                    Retention groups by it so two plans never crowd each other.
--   pinned           1 = no retention rule ever deletes it.
--   proof_level      1 checksum recorded, 2 contents checked, 3 test restore.
--                    Never collapsed into one "restorable" flag.
--   proof_checked_at when proof_level was last raised.
--   drill_result     ok | partial | failed — the last test restore, if any.
--   drill_at / drill_report (JSON) — when, and what failed.
--   incomplete       JSON array of the manifest's contents.incomplete items:
--                    what the bundle should hold and does not.
--   run_id           the backup run that wrote it (NULL before runs existed).
--
-- No backfill beyond the defaults: every existing row is a full bundle at
-- proof level 1, unpinned, which is exactly what it is. incomplete stays
-- NULL for existing rows — "not recorded", which readers must not read as
-- "complete"; the startup catalog scan re-reads manifests and fills it.
ALTER TABLE backup_catalog ADD COLUMN kind TEXT NOT NULL DEFAULT 'full';
ALTER TABLE backup_catalog ADD COLUMN plan_id TEXT;
ALTER TABLE backup_catalog ADD COLUMN pinned INTEGER NOT NULL DEFAULT 0;
ALTER TABLE backup_catalog ADD COLUMN proof_level INTEGER NOT NULL DEFAULT 1;
ALTER TABLE backup_catalog ADD COLUMN proof_checked_at TEXT;
ALTER TABLE backup_catalog ADD COLUMN drill_result TEXT;
ALTER TABLE backup_catalog ADD COLUMN drill_at TEXT;
ALTER TABLE backup_catalog ADD COLUMN drill_report TEXT;
ALTER TABLE backup_catalog ADD COLUMN incomplete TEXT;
ALTER TABLE backup_catalog ADD COLUMN run_id TEXT;

CREATE INDEX IF NOT EXISTS idx_backup_catalog_plan ON backup_catalog(plan_id) WHERE plan_id IS NOT NULL;

-- restore_reports: every restore, dry run and drill the server runs, with the
-- full report the restore built. The admin UI used to show that report once
-- and discard it; an operator asking "what did that restore skip?" a week
-- later had nothing to read. Instance-level bookkeeping — no workspace FK, so
-- the record survives the deletion of the workspace it restored into, and it
-- never rides a bundle (see NonBackedUpTables).
--
--   result  ok | partial | failed. partial = the restore committed but the
--           report names something skipped, missing or lowered (dropped crew
--           files, clamped credential tiers, dropped columns, missing
--           attachment files, row-count shortfalls, …).
CREATE TABLE IF NOT EXISTS restore_reports (
    id            TEXT PRIMARY KEY,
    kind          TEXT NOT NULL CHECK (kind IN ('restore', 'dry_run', 'drill')),
    actor_user_id TEXT,
    bundle_path   TEXT NOT NULL,
    target        TEXT NOT NULL DEFAULT '',
    result        TEXT NOT NULL CHECK (result IN ('ok', 'partial', 'failed')),
    report        TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(report)),
    created_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE INDEX IF NOT EXISTS idx_restore_reports_created ON restore_reports(created_at);
