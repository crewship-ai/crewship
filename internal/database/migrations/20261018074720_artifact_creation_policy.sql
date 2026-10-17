CREATE TABLE IF NOT EXISTS workspace_artifact_creation_policy (
  workspace_id TEXT NOT NULL PRIMARY KEY REFERENCES workspaces(id) ON DELETE CASCADE,
  enabled INTEGER NOT NULL DEFAULT 0 CHECK(enabled IN (0,1)),
  version INTEGER NOT NULL CHECK(version>0),
  updated_by_user_id TEXT REFERENCES users(id) ON DELETE SET NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS workspace_artifact_creation_crews (
  workspace_id TEXT NOT NULL REFERENCES workspace_artifact_creation_policy(workspace_id) ON DELETE CASCADE,
  crew_id TEXT NOT NULL REFERENCES crews(id) ON DELETE CASCADE,
  FOREIGN KEY(workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE,
  PRIMARY KEY(workspace_id,crew_id)
);

CREATE INDEX IF NOT EXISTS idx_artifact_creation_policy_editor ON workspace_artifact_creation_policy(updated_by_user_id);
CREATE INDEX IF NOT EXISTS idx_artifact_creation_policy_crew ON workspace_artifact_creation_crews(crew_id);
