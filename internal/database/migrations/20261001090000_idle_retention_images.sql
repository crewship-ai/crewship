-- Idle cache-image retention: when this installation first saw each cached
-- image unused by every container on the daemon. An image is evicted only
-- after it has stayed unused for the configured window; any use resets it.
-- Installation-local bookkeeping, no FK, never part of a workspace backup.
CREATE TABLE resource_retention_images (
 instance_id TEXT NOT NULL,
 image_id TEXT NOT NULL,
 first_unused_at TEXT NOT NULL,
 PRIMARY KEY(instance_id,image_id)
);
