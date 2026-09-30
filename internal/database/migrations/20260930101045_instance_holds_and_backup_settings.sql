-- Backups redesign, Track B: automation holds after an instance restore, and
-- the one instance-wide backup setting this track needs.
--
-- instance_holds: what `crewship recover` leaves stopped until an instance
-- admin resumes it. One row per key:
--   routines  schedules (agent cron, routine schedules, recurring issues)
--             do not fire
--   webhooks  inbound webhooks answer 503
--   queue     queued work and external retries do not start
--   all       every one of the above
-- count / detail describe what is held ("12 schedules"), for the Recovery
-- page. The running server reads the table at boot and keeps a copy in
-- memory; only the resume endpoint deletes rows. Instance bookkeeping: it
-- never rides a workspace bundle (NonBackedUpTables), and an instance bundle
-- carries it only as part of the whole database file, where recover
-- overwrites it with fresh holds anyway.
CREATE TABLE IF NOT EXISTS instance_holds (
    key        TEXT PRIMARY KEY CHECK (key IN ('routines', 'webhooks', 'queue', 'all')),
    reason     TEXT NOT NULL DEFAULT '',
    count      INTEGER NOT NULL DEFAULT 0,
    detail     TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now'))
);

-- backup_settings: the single row of instance-wide backup settings. Kept
-- minimal on purpose — Track C (plans, scheduler, limits, alerts) adds its
-- columns with its own migration. recovery_kit_enabled = 1 puts every vault
-- key version the database references inside new INSTANCE bundles (never
-- workspace or crew bundles).
CREATE TABLE IF NOT EXISTS backup_settings (
    id                   INTEGER PRIMARY KEY CHECK (id = 1),
    recovery_kit_enabled INTEGER NOT NULL DEFAULT 0,
    updated_at           TEXT,
    updated_by           TEXT
);
INSERT OR IGNORE INTO backup_settings (id, recovery_kit_enabled) VALUES (1, 0);
