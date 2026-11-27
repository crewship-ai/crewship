-- Keep the original native profile immutable and the source workbook recoverable
-- for the life of the Page. Retention cannot remove its manifest or its blob.
CREATE TABLE craft_workbook_migrations (
 page_id TEXT PRIMARY KEY REFERENCES craft_native_documents(page_id) ON DELETE CASCADE,
 workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 source_version INTEGER NOT NULL CHECK(source_version > 0),
 source_hash TEXT NOT NULL CHECK(length(source_hash) = 64),
 workbook_version INTEGER NOT NULL CHECK(workbook_version = source_version + 1),
 engine_revision TEXT NOT NULL CHECK(length(engine_revision) = 40),
 engine_sha256 TEXT NOT NULL CHECK(length(engine_sha256) = 64),
 editor_bundle TEXT NOT NULL CHECK(length(editor_bundle) = 64),
 FOREIGN KEY(workspace_id,page_id,source_version)
  REFERENCES artifact_versions(workspace_id,page_id,version) DEFERRABLE INITIALLY DEFERRED,
 FOREIGN KEY(workspace_id,page_id,source_hash)
  REFERENCES artifact_version_blobs(workspace_id,page_id,hash) DEFERRABLE INITIALLY DEFERRED
);
CREATE TRIGGER craft_workbook_migration_immutable BEFORE UPDATE ON craft_workbook_migrations
BEGIN SELECT RAISE(ABORT,'Craft workbook migration is immutable'); END;
