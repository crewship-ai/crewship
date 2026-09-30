-- Backups redesign, Track C2: the instance-wide backup settings (limits,
-- heartbeat, alerts), backup keys (recipients), incidents, and off-site
-- destinations with their verified copies. All instance bookkeeping: none of
-- these tables rides a workspace bundle (NonBackedUpTables in
-- internal/backup/intent.go); an instance bundle carries them only as part
-- of the whole database file.

-- backup_settings gains Track C's columns (the single row already exists).
--   concurrency       backup runs that execute at once (the rest queue).
--   cpu_cores         zstd encoder goroutines for packing and compression.
--   disk_mbps         MB/s cap on writing bundles and staging (0 = none).
--   upload_mbps       MB/s cap on off-site uploads (0 = none).
--   heartbeat_url     pinged (GET) after every good run, so an outside
--                     service alerts when pings stop; https, public only.
--   heartbeat_*       the last ping: when, and "ok" or the error.
--   alert_channels    JSON array of extra delivery routes (see the settings
--                     API); the inbox of every instance admin always gets it.
--   alert_events      JSON {failed, incomplete, stale, offsite, drill}: which
--                     incident kinds are delivered.
--   stale_alert_hours an incident is raised when a plan's newest good backup
--                     is older than this.
--   drill_reminder    weekly | monthly | off.
ALTER TABLE backup_settings ADD COLUMN concurrency INTEGER NOT NULL DEFAULT 1 CHECK (concurrency BETWEEN 1 AND 16);
ALTER TABLE backup_settings ADD COLUMN cpu_cores INTEGER NOT NULL DEFAULT 2 CHECK (cpu_cores BETWEEN 1 AND 256);
ALTER TABLE backup_settings ADD COLUMN disk_mbps INTEGER NOT NULL DEFAULT 0 CHECK (disk_mbps >= 0);
ALTER TABLE backup_settings ADD COLUMN upload_mbps INTEGER NOT NULL DEFAULT 0 CHECK (upload_mbps >= 0);
ALTER TABLE backup_settings ADD COLUMN heartbeat_url TEXT;
ALTER TABLE backup_settings ADD COLUMN heartbeat_last_at TEXT;
ALTER TABLE backup_settings ADD COLUMN heartbeat_last_status TEXT;
ALTER TABLE backup_settings ADD COLUMN alert_channels TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(alert_channels));
ALTER TABLE backup_settings ADD COLUMN alert_events TEXT NOT NULL DEFAULT '{"failed":true,"incomplete":true,"stale":true,"offsite":true,"drill":true}' CHECK (json_valid(alert_events));
ALTER TABLE backup_settings ADD COLUMN stale_alert_hours INTEGER NOT NULL DEFAULT 36 CHECK (stale_alert_hours >= 1);
ALTER TABLE backup_settings ADD COLUMN drill_reminder TEXT NOT NULL DEFAULT 'monthly' CHECK (drill_reminder IN ('weekly', 'monthly', 'off'));

-- backup_recipients: the AGE public keys backups are encrypted to. Private
-- halves are never stored. Plans name them in recipient_ids.
CREATE TABLE backup_recipients (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 120),
    public_key TEXT NOT NULL UNIQUE,
    holder     TEXT NOT NULL DEFAULT '',
    created_by TEXT,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

-- backup_incidents: one open incident per (plan, kind); a repeat updates it
-- (count, last_at, message), the next good run or a cleared condition
-- resolves it. plan_id '' is runs made without a plan. inbox_item_ids is the
-- JSON array of inbox rows delivered to instance admins for it.
CREATE TABLE backup_incidents (
    id             TEXT PRIMARY KEY,
    plan_id        TEXT NOT NULL DEFAULT '',
    kind           TEXT NOT NULL CHECK (kind IN ('failed', 'incomplete', 'stale', 'offsite', 'drill')),
    state          TEXT NOT NULL DEFAULT 'open' CHECK (state IN ('open', 'resolved')),
    count          INTEGER NOT NULL DEFAULT 1,
    first_at       TEXT NOT NULL,
    last_at        TEXT NOT NULL,
    resolved_at    TEXT,
    message        TEXT NOT NULL DEFAULT '',
    run_id         TEXT,
    inbox_item_ids TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(inbox_item_ids))
);
CREATE UNIQUE INDEX idx_backup_incidents_open ON backup_incidents(plan_id, kind) WHERE state = 'open';
CREATE INDEX idx_backup_incidents_last ON backup_incidents(last_at);

-- backup_offsite_destinations: S3-compatible stores an instance admin added.
-- The secret access key is a vault envelope (encryption.Encrypt, listed in
-- encryption.EnvelopeColumns) and never leaves the server. Plans name a
-- destination by id in backup_plans.destinations beside "local".
CREATE TABLE backup_offsite_destinations (
    id                    TEXT PRIMARY KEY,
    name                  TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 120),
    kind                  TEXT NOT NULL DEFAULT 's3' CHECK (kind IN ('s3')),
    endpoint              TEXT NOT NULL,
    region                TEXT NOT NULL DEFAULT '',
    bucket                TEXT NOT NULL,
    prefix                TEXT NOT NULL DEFAULT '',
    access_key_id         TEXT NOT NULL,
    secret_enc            TEXT NOT NULL,
    path_style            INTEGER NOT NULL DEFAULT 0,
    allow_private_network INTEGER NOT NULL DEFAULT 0,
    last_test_at          TEXT,
    last_test_error       TEXT,
    created_by            TEXT,
    created_at            TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

-- backup_copies: an off-site copy of a bundle, written only after the upload
-- was verified (size and SHA-256 at the destination). Counted as a copy from
-- then on; retention deletes the remote object with the local bundle.
CREATE TABLE backup_copies (
    id             TEXT PRIMARY KEY,
    bundle_path    TEXT NOT NULL,
    destination_id TEXT NOT NULL,
    object_key     TEXT NOT NULL,
    size           INTEGER NOT NULL,
    sha256         TEXT NOT NULL,
    verified_at    TEXT NOT NULL,
    UNIQUE (bundle_path, destination_id)
);
CREATE INDEX idx_backup_copies_destination ON backup_copies(destination_id);
