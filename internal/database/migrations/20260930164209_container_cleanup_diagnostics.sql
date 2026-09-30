-- Installation diagnostics intentionally have no workspace/owner FK: tombstone
-- deletion must not discard the evidence for surviving mounts. No user payloads.
CREATE TABLE resource_cleanup_status (
 instance_id TEXT NOT NULL,
 crew_id TEXT NOT NULL,
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
