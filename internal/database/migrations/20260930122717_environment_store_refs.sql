-- Backups redesign, Track E: the environment store's reference counts.
--
-- A complete container environment stores every file of the crew image's
-- `docker save` archive once, by sha256, under
-- <backups dir>/environments/blobs/sha256/<hex>. Many bundles share the same
-- base layers, so a blob may only be deleted when no bundle needs it.
--
-- environment_blobs: every blob the store holds (size, first time seen).
-- bundle_environment_refs: which bundle needs which blob. bundle_ref is the
-- bundle's file path (the backup_catalog key), or "pending:<token>" while a
-- capture is still writing its bundle — a pending ref older than a day is a
-- crashed capture and is dropped by the collector.
--
-- Retention (RotateWithPolicy / RotatePlanWithPolicy) and bundle deletion
-- drop a bundle's refs, then the collector deletes blobs with no refs left.
-- Instance bookkeeping: neither table rides a workspace bundle.
CREATE TABLE IF NOT EXISTS environment_blobs (
    digest     TEXT PRIMARY KEY,
    size       INTEGER NOT NULL DEFAULT 0,
    first_seen TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now'))
);

CREATE TABLE IF NOT EXISTS bundle_environment_refs (
    bundle_ref TEXT NOT NULL,
    digest     TEXT NOT NULL,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
    PRIMARY KEY (bundle_ref, digest)
);

CREATE INDEX IF NOT EXISTS idx_bundle_environment_refs_digest ON bundle_environment_refs(digest);
