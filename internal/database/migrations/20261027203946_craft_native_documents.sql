-- Only explicitly initialized native profiles use this table. Existing document
-- bytes and engine pins are not converted.
CREATE TABLE craft_native_documents (
 page_id TEXT PRIMARY KEY REFERENCES pages(id) ON DELETE CASCADE,
 workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 application_id TEXT NOT NULL CHECK(application_id IN ('wordcraft','gridcraft')),
 engine_revision TEXT NOT NULL CHECK(length(engine_revision)=40 AND engine_revision NOT GLOB '*[^0-9a-f]*'),
 engine_sha256 TEXT NOT NULL CHECK(length(engine_sha256)=64 AND engine_sha256 NOT GLOB '*[^0-9a-f]*'),
 editor_bundle TEXT NOT NULL CHECK(length(editor_bundle)=64 AND editor_bundle NOT GLOB '*[^0-9a-f]*')
);
CREATE INDEX craft_native_documents_workspace ON craft_native_documents(workspace_id);
CREATE TRIGGER craft_native_profile_immutable BEFORE UPDATE ON craft_native_documents
BEGIN SELECT RAISE(ABORT,'Native Craft profile is immutable'); END;
