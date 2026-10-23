-- Keep parent workspace/task/change deletion bounded by indexed child lookups.
-- The existing identity PKs lead with page_id; they cannot serve workspace-only
-- lookups. Reverse composite indexes also cover the task/change FK columns.
CREATE INDEX mooring_collab_documents_workspace ON mooring_collab_documents(workspace_id);
CREATE INDEX mooring_collab_tasks_workspace ON mooring_collab_tasks(workspace_id);
CREATE INDEX mooring_collab_changes_workspace ON mooring_collab_changes(workspace_id);
CREATE INDEX mooring_collab_changes_task ON mooring_collab_changes(task_id,page_id);
CREATE INDEX mooring_collab_operations_change ON mooring_collab_operations(change_id,page_id);
CREATE INDEX mooring_collab_checkpoints_workspace ON mooring_collab_checkpoints(workspace_id);
CREATE INDEX mooring_collab_audit_workspace ON mooring_collab_audit(workspace_id);
