-- A human action has one durable copy even after a lost HTTP response.
-- Source and destination IDs deliberately survive Page deletion: retry must
-- never recreate a deleted copy or turn an old key into a second copy.
CREATE TABLE document_copy_receipts (
 workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 user_id TEXT NOT NULL,
 mutation_id TEXT NOT NULL,
 request_digest TEXT NOT NULL CHECK(length(request_digest)=64),
 source_page_id TEXT NOT NULL,
 result_page_id TEXT NOT NULL,
 result_slug TEXT NOT NULL,
 created_at TEXT NOT NULL,
 PRIMARY KEY(workspace_id,user_id,mutation_id)
);
CREATE TRIGGER document_copy_receipts_immutable BEFORE UPDATE ON document_copy_receipts
BEGIN SELECT RAISE(ABORT,'document copy receipt is immutable'); END;
