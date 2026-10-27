-- Immutable whole-project manifests. New versions and their blobs commit together;
-- restoring appends a manifest, and sharing never moves the latest head.
CREATE TABLE artifact_version_heads (
 workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 page_id TEXT NOT NULL REFERENCES pages(id) ON DELETE CASCADE,
 latest_version INTEGER NOT NULL CHECK(latest_version>0),
 shared_version INTEGER NOT NULL DEFAULT 0 CHECK(shared_version>=0),
 PRIMARY KEY(workspace_id,page_id)
);
CREATE TABLE artifact_version_blobs (
 workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 page_id TEXT NOT NULL REFERENCES pages(id) ON DELETE CASCADE,
 hash TEXT NOT NULL,
 content BLOB NOT NULL,
 PRIMARY KEY(workspace_id,page_id,hash)
);
CREATE TABLE artifact_versions (
 workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 page_id TEXT NOT NULL REFERENCES pages(id) ON DELETE CASCADE,
 version INTEGER NOT NULL CHECK(version>0),
 manifest_json TEXT NOT NULL CHECK(json_valid(manifest_json)),
 message TEXT NOT NULL DEFAULT '',
 actor TEXT NOT NULL,
 created_at TEXT NOT NULL,
 restored_from INTEGER NOT NULL DEFAULT 0,
 PRIMARY KEY(workspace_id,page_id,version)
);
CREATE TRIGGER artifact_version_head_scope BEFORE INSERT ON artifact_version_heads
WHEN NOT EXISTS(SELECT 1 FROM pages WHERE id=NEW.page_id AND workspace_id=NEW.workspace_id)
BEGIN SELECT RAISE(ABORT,'artifact page workspace mismatch'); END;
CREATE TRIGGER artifact_version_blob_scope BEFORE INSERT ON artifact_version_blobs
WHEN NOT EXISTS(SELECT 1 FROM pages WHERE id=NEW.page_id AND workspace_id=NEW.workspace_id)
BEGIN SELECT RAISE(ABORT,'artifact page workspace mismatch'); END;
CREATE TRIGGER artifact_version_scope BEFORE INSERT ON artifact_versions
WHEN NOT EXISTS(SELECT 1 FROM pages WHERE id=NEW.page_id AND workspace_id=NEW.workspace_id)
BEGIN SELECT RAISE(ABORT,'artifact page workspace mismatch'); END;
