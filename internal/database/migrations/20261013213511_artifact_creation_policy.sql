-- Administrative policy supplements, never replaces, human run opt-in and
-- existing Page grants. Missing rows are disabled; existing workspaces stay OFF.
CREATE TABLE workspace_artifact_creation_policy (
 workspace_id TEXT NOT NULL PRIMARY KEY REFERENCES workspaces(id) ON DELETE CASCADE,
 enabled INTEGER NOT NULL DEFAULT 0 CHECK(enabled IN (0,1)),
 version INTEGER NOT NULL CHECK(version>0),
 updated_by_user_id TEXT REFERENCES users(id) ON DELETE SET NULL,
 updated_at TEXT NOT NULL
);
CREATE INDEX idx_artifact_creation_policy_editor ON workspace_artifact_creation_policy(updated_by_user_id);
CREATE TABLE workspace_artifact_creation_crews (
 workspace_id TEXT NOT NULL REFERENCES workspace_artifact_creation_policy(workspace_id) ON DELETE CASCADE,
 crew_id TEXT NOT NULL REFERENCES crews(id) ON DELETE CASCADE,
 -- The direct workspace edge also preserves its identity namespace on fork.
 FOREIGN KEY(workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE,
 PRIMARY KEY(workspace_id,crew_id)
);
CREATE INDEX idx_artifact_creation_policy_crew ON workspace_artifact_creation_crews(crew_id);
CREATE TRIGGER artifact_creation_policy_crew_scope BEFORE INSERT ON workspace_artifact_creation_crews
WHEN NOT EXISTS(SELECT 1 FROM crews WHERE id=NEW.crew_id AND workspace_id=NEW.workspace_id)
BEGIN SELECT RAISE(ABORT,'artifact creation policy crew must belong to workspace'); END;
CREATE TRIGGER artifact_creation_policy_crew_scope_update BEFORE UPDATE ON workspace_artifact_creation_crews
WHEN NOT EXISTS(SELECT 1 FROM crews WHERE id=NEW.crew_id AND workspace_id=NEW.workspace_id)
BEGIN SELECT RAISE(ABORT,'artifact creation policy crew must belong to workspace'); END;
