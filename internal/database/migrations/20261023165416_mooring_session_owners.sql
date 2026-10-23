-- Additive, unused until the separate collaboration owner feature is enabled.
-- A document has one owner across application profiles, not one per application.
CREATE TABLE mooring_session_writer_fence (
 id INTEGER PRIMARY KEY CHECK(id=1),
 seq INTEGER NOT NULL DEFAULT 0
);
INSERT INTO mooring_session_writer_fence(id) VALUES(1);
CREATE TABLE mooring_session_owners (
 page_id TEXT PRIMARY KEY REFERENCES pages(id) ON DELETE CASCADE,
 workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 application_id TEXT NOT NULL,
 owner_id TEXT NOT NULL,
 generation INTEGER NOT NULL CHECK(generation>0),
 token_hash BLOB NOT NULL CHECK(length(token_hash)=32),
 boot_id TEXT NOT NULL,
 expires_ns INTEGER NOT NULL CHECK(expires_ns>=0)
);
CREATE INDEX mooring_session_owners_workspace ON mooring_session_owners(workspace_id);
