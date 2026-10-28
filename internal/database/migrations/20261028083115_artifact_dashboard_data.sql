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
