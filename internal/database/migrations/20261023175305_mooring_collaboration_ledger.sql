-- Default-off collaboration primitive. Durable history is retained until page
-- deletion; quotas reject new admissions instead of forgetting operation IDs.
CREATE TABLE mooring_collab_documents (
 page_id TEXT PRIMARY KEY REFERENCES pages(id) ON DELETE CASCADE,
 workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 application_id TEXT NOT NULL,
 revision TEXT NOT NULL,
 checkpoint_base64 TEXT NOT NULL
);
CREATE TABLE mooring_collab_tasks (
 page_id TEXT NOT NULL REFERENCES mooring_collab_documents(page_id) ON DELETE CASCADE,
 workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 task_id TEXT NOT NULL,
 participant_ref TEXT NOT NULL,
 allow_partial INTEGER NOT NULL CHECK(allow_partial IN (0,1)),
 status TEXT NOT NULL CHECK(status IN ('running','cancelled')),
 PRIMARY KEY(page_id,task_id)
);
CREATE TABLE mooring_collab_changes (
 page_id TEXT NOT NULL REFERENCES mooring_collab_documents(page_id) ON DELETE CASCADE,
 workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 change_id TEXT NOT NULL,
 membership_hash TEXT NOT NULL,
 task_id TEXT,
 result_json TEXT NOT NULL,
 PRIMARY KEY(page_id,change_id),
 FOREIGN KEY(page_id,task_id) REFERENCES mooring_collab_tasks(page_id,task_id)
);
CREATE TABLE mooring_collab_operations (
 page_id TEXT NOT NULL,
 workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 op_id TEXT NOT NULL,
 change_id TEXT NOT NULL,
 participant_ref TEXT NOT NULL,
 fingerprint TEXT NOT NULL,
 result_json TEXT NOT NULL,
 PRIMARY KEY(page_id,op_id),
 FOREIGN KEY(page_id,change_id) REFERENCES mooring_collab_changes(page_id,change_id) ON DELETE CASCADE
);
CREATE TABLE mooring_collab_checkpoints (
 page_id TEXT NOT NULL REFERENCES mooring_collab_documents(page_id) ON DELETE CASCADE,
 workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 revision TEXT NOT NULL,
 checkpoint_base64 TEXT NOT NULL,
 decoded_bytes INTEGER NOT NULL CHECK(decoded_bytes>=0),
 PRIMARY KEY(page_id,revision)
);
CREATE TABLE mooring_collab_audit (
 id TEXT PRIMARY KEY,
 page_id TEXT NOT NULL REFERENCES mooring_collab_documents(page_id) ON DELETE CASCADE,
 workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 kind TEXT NOT NULL,
 detail_json TEXT NOT NULL
);
CREATE INDEX mooring_collab_operations_workspace ON mooring_collab_operations(workspace_id);
CREATE INDEX mooring_collab_audit_page ON mooring_collab_audit(page_id);
