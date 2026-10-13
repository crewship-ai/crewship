CREATE TABLE artifact_workspace_writes (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    created_at TEXT NOT NULL
);
CREATE INDEX idx_artifact_workspace_writes_recent ON artifact_workspace_writes(workspace_id, created_at);

-- Preserve the rolling quota when upgrading existing artifact installations.
INSERT INTO artifact_workspace_writes(id, workspace_id, created_at)
SELECT r.id, p.workspace_id, r.created_at
FROM page_artifact_revisions r JOIN pages p ON p.id=r.page_id
WHERE r.created_at > strftime('%Y-%m-%dT%H:%M:%fZ', 'now', '-1 hour');
