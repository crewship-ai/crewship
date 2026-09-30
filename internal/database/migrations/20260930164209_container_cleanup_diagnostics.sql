-- Installation diagnostics intentionally have no workspace/owner FK: tombstone
-- deletion must not discard the evidence for surviving mounts. No user payloads.
CREATE TABLE resource_cleanup_status (
 instance_id TEXT NOT NULL,
 crew_id TEXT NOT NULL,
 -- Owning workspace at deletion time: the admin API shows a caller only its
 -- own workspace's rows. No FK, for the same reason as above.
 workspace_id TEXT NOT NULL DEFAULT '',
 state TEXT NOT NULL,
 observed_at TEXT NOT NULL DEFAULT '',
 complete INTEGER NOT NULL DEFAULT 0,
 remaining INTEGER NOT NULL DEFAULT 0,
 unattributed INTEGER NOT NULL DEFAULT 0,
 error_code TEXT NOT NULL DEFAULT '',
 PRIMARY KEY(instance_id,crew_id)
);
CREATE TABLE resource_cleanup_mounts (
 instance_id TEXT NOT NULL,
 crew_id TEXT NOT NULL,
 container_id TEXT NOT NULL,
 mounts_json TEXT NOT NULL,
 observed_at TEXT NOT NULL,
 PRIMARY KEY(instance_id,container_id)
);
-- One row per installation scan: freshness lives here, so per-owner rows are
-- written only when their state changes instead of on every tick.
CREATE TABLE resource_cleanup_scans (
 instance_id TEXT PRIMARY KEY,
 observed_at TEXT NOT NULL DEFAULT '',
 complete INTEGER NOT NULL DEFAULT 0,
 error_code TEXT NOT NULL DEFAULT ''
);
-- Random per-database nonce. The installation identity file is keyed by it, so
-- several databases sharing one data directory never share an identity, and a
-- database copied to another data directory never inherits one.
CREATE TABLE resource_cleanup_installation (
 id INTEGER PRIMARY KEY CHECK (id = 1),
 db_nonce TEXT NOT NULL
);
