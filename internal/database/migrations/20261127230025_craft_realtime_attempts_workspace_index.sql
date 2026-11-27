-- Support workspace-scoped cleanup and the workspace foreign key.
CREATE INDEX craft_realtime_attempts_workspace ON craft_realtime_attempts(workspace_id);
