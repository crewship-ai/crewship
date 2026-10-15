ALTER TABLE documents ADD COLUMN generation INTEGER NOT NULL DEFAULT 0;
CREATE TABLE document_comments (
 workspace_id TEXT NOT NULL, page_id TEXT NOT NULL, id TEXT NOT NULL,
 body TEXT NOT NULL, anchor_json TEXT NOT NULL, actor TEXT NOT NULL,
 rev INTEGER NOT NULL, version INTEGER NOT NULL DEFAULT 1,
 resolved INTEGER NOT NULL DEFAULT 0,
 created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
 PRIMARY KEY(workspace_id,page_id,id),
 FOREIGN KEY(workspace_id,page_id) REFERENCES documents(workspace_id,page_id) ON DELETE CASCADE
);
ALTER TABLE document_writer_fence ADD COLUMN boot_id TEXT NOT NULL DEFAULT '';
