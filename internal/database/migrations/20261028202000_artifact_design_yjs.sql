-- Add the Design storage family without changing shipped migrations. Preserve
-- dashboard children explicitly: dropping a parent would otherwise cascade.
CREATE TEMP TABLE design_migration_heads AS SELECT * FROM artifact_version_heads;
CREATE TEMP TABLE design_migration_collections AS SELECT * FROM artifact_data_collections;
CREATE TEMP TABLE design_migration_documents AS SELECT * FROM artifact_data_documents;
DROP TABLE artifact_data_documents;
DROP TABLE artifact_data_collections;
DROP TABLE artifact_version_heads;
CREATE TABLE artifact_version_heads (
 workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 page_id TEXT NOT NULL REFERENCES pages(id) ON DELETE CASCADE,
 content_type TEXT NOT NULL DEFAULT 'artifact_project' CHECK(content_type IN ('artifact_project','wordcraft','gridcraft','design')),
 latest_version INTEGER NOT NULL CHECK(latest_version>0),
 shared_version INTEGER NOT NULL DEFAULT 0 CHECK(shared_version>=0),
 PRIMARY KEY(workspace_id,page_id)
);
INSERT INTO artifact_version_heads SELECT * FROM design_migration_heads;
CREATE TRIGGER artifact_version_head_scope BEFORE INSERT ON artifact_version_heads
WHEN NOT EXISTS(SELECT 1 FROM pages WHERE id=NEW.page_id AND workspace_id=NEW.workspace_id)
BEGIN SELECT RAISE(ABORT,'artifact page workspace mismatch'); END;
CREATE TRIGGER artifact_version_family_immutable BEFORE UPDATE OF content_type ON artifact_version_heads
WHEN OLD.content_type<>NEW.content_type
BEGIN SELECT RAISE(ABORT,'artifact content family is immutable'); END;
-- Mutable dashboard datasets live separately from immutable code manifests.
-- Page/workspace lifetime owns all data; no runtime authority is persisted here.
CREATE TABLE artifact_data_collections (
 workspace_id TEXT NOT NULL,
 page_id TEXT NOT NULL,
 name TEXT NOT NULL,
 PRIMARY KEY(workspace_id,page_id,name),
 FOREIGN KEY(workspace_id,page_id) REFERENCES artifact_version_heads(workspace_id,page_id) ON DELETE CASCADE
);
CREATE TABLE artifact_data_documents (
 workspace_id TEXT NOT NULL,
 page_id TEXT NOT NULL,
 collection TEXT NOT NULL,
 id TEXT NOT NULL,
 version INTEGER NOT NULL CHECK(typeof(version)='integer' AND version BETWEEN 1 AND 9007199254740991),
 value_json TEXT NOT NULL CHECK(json_valid(value_json) AND json_type(value_json)='object'),
 updated_by TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 PRIMARY KEY(workspace_id,page_id,collection,id),
 FOREIGN KEY(workspace_id,page_id,collection) REFERENCES artifact_data_collections(workspace_id,page_id,name) ON DELETE CASCADE
);
INSERT INTO artifact_data_collections SELECT * FROM design_migration_collections;
INSERT INTO artifact_data_documents SELECT * FROM design_migration_documents;
DROP TABLE design_migration_documents;
DROP TABLE design_migration_collections;
DROP TABLE design_migration_heads;

-- This is the current collaborative head, not another version registry.
CREATE TABLE artifact_design_documents (
 workspace_id TEXT NOT NULL,
 page_id TEXT NOT NULL,
 state BLOB NOT NULL CHECK(length(state)<=4194304),
 revision INTEGER NOT NULL DEFAULT 0 CHECK(revision>=0),
 saved_revision INTEGER NOT NULL DEFAULT 0 CHECK(saved_revision>=0),
 updated_by TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 PRIMARY KEY(workspace_id,page_id),
 FOREIGN KEY(workspace_id,page_id) REFERENCES artifact_version_heads(workspace_id,page_id) ON DELETE CASCADE
);

INSERT INTO feature_flags(id,key,description,enabled,created_at,updated_at)
VALUES('ff_artifacts_design','artifacts_design','Collaborative Design artifacts using standard Yjs',0,datetime('now'),datetime('now'))
ON CONFLICT(key) DO NOTHING;

CREATE TRIGGER artifact_design_document_family BEFORE INSERT ON artifact_design_documents
WHEN NOT EXISTS(SELECT 1 FROM artifact_version_heads
 WHERE workspace_id=NEW.workspace_id AND page_id=NEW.page_id AND content_type='design')
BEGIN SELECT RAISE(ABORT,'Design document family mismatch'); END;
