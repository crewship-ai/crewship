-- Anchored discussion on a native Docs page. A root comment carries the
-- engine-validated anchor JSON; a reply names its root and carries none.
-- Comments never change document content, revision, epoch or generation.
CREATE TABLE document_comments (
 workspace_id TEXT NOT NULL, page_id TEXT NOT NULL,
 id TEXT NOT NULL CHECK(length(id) BETWEEN 1 AND 128),
 parent_id TEXT CHECK(parent_id IS NULL OR length(parent_id) BETWEEN 1 AND 128),
 body TEXT NOT NULL CHECK(length(body) BETWEEN 1 AND 10000),
 anchor_json TEXT NOT NULL CHECK(length(anchor_json) <= 8192),
 actor TEXT NOT NULL,
 rev INTEGER NOT NULL CHECK(rev >= 0),
 version INTEGER NOT NULL DEFAULT 1 CHECK(version >= 1),
 resolved INTEGER NOT NULL DEFAULT 0 CHECK(resolved IN (0,1)),
 created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
 PRIMARY KEY(workspace_id,page_id,id),
 CHECK((parent_id IS NULL) = (anchor_json != '')),
 CHECK(parent_id IS NULL OR resolved = 0),
 FOREIGN KEY(workspace_id,page_id) REFERENCES documents(workspace_id,page_id) ON DELETE CASCADE,
 FOREIGN KEY(workspace_id,page_id,parent_id) REFERENCES document_comments(workspace_id,page_id,id) ON DELETE CASCADE
);
CREATE INDEX idx_document_comments_thread ON document_comments(workspace_id,page_id,parent_id);
