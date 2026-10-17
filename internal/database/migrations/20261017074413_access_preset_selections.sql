-- Informational explicit choice only: access_grants remains canonical authority.
-- No backfill from existing independent grants and no permission changes.
CREATE TABLE access_preset_selections (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    member_id TEXT NOT NULL REFERENCES workspace_members(id) ON DELETE CASCADE,
    resource_kind TEXT NOT NULL CHECK(resource_kind IN ('routine','project')),
    project_id TEXT REFERENCES projects(id) ON DELETE CASCADE,
    routine_id TEXT REFERENCES pipelines(id) ON DELETE CASCADE,
    preset_id TEXT NOT NULL,
    preset_version INTEGER NOT NULL CHECK(preset_version>0),
    level TEXT NOT NULL,
    definition_hash TEXT NOT NULL,
    managed_operations TEXT NOT NULL CHECK(json_valid(managed_operations) AND json_type(managed_operations)='array'),
    selected_by TEXT REFERENCES users(id) ON DELETE SET NULL,
    selected_at TEXT NOT NULL,
    CHECK((resource_kind='routine' AND routine_id IS NOT NULL AND project_id IS NULL)
       OR (resource_kind='project' AND project_id IS NOT NULL AND routine_id IS NULL)),
    UNIQUE(member_id,project_id), UNIQUE(member_id,routine_id)
);
CREATE TRIGGER access_preset_binding_insert BEFORE INSERT ON access_preset_selections
WHEN NOT EXISTS(SELECT 1 FROM workspace_members WHERE id=NEW.member_id AND workspace_id=NEW.workspace_id)
  OR (NEW.resource_kind='project' AND NOT EXISTS(SELECT 1 FROM projects WHERE id=NEW.project_id AND workspace_id=NEW.workspace_id))
  OR (NEW.resource_kind='routine' AND NOT EXISTS(SELECT 1 FROM pipelines WHERE id=NEW.routine_id AND workspace_id=NEW.workspace_id))
BEGIN SELECT RAISE(ABORT,'preset selection workspace mismatch'); END;
CREATE TRIGGER access_preset_binding_update BEFORE UPDATE ON access_preset_selections
WHEN NOT EXISTS(SELECT 1 FROM workspace_members WHERE id=NEW.member_id AND workspace_id=NEW.workspace_id)
  OR (NEW.resource_kind='project' AND NOT EXISTS(SELECT 1 FROM projects WHERE id=NEW.project_id AND workspace_id=NEW.workspace_id))
  OR (NEW.resource_kind='routine' AND NOT EXISTS(SELECT 1 FROM pipelines WHERE id=NEW.routine_id AND workspace_id=NEW.workspace_id))
BEGIN SELECT RAISE(ABORT,'preset selection workspace mismatch'); END;
-- Only actual changes to this choice's managed subset invalidate its label.
-- Issuer liveness changes revisions, not stored grants, and retain provenance.
CREATE TRIGGER access_preset_grant_insert AFTER INSERT ON access_grants BEGIN
    DELETE FROM access_preset_selections
    WHERE member_id=NEW.member_id AND resource_kind=NEW.resource_kind
      AND COALESCE(project_id,routine_id)=COALESCE(NEW.project_id,NEW.routine_id)
      AND EXISTS(SELECT 1 FROM json_each(managed_operations) WHERE value=NEW.operation);
END;
CREATE TRIGGER access_preset_grant_delete AFTER DELETE ON access_grants BEGIN
    DELETE FROM access_preset_selections
    WHERE member_id=OLD.member_id AND resource_kind=OLD.resource_kind
      AND COALESCE(project_id,routine_id)=COALESCE(OLD.project_id,OLD.routine_id)
      AND EXISTS(SELECT 1 FROM json_each(managed_operations) WHERE value=OLD.operation);
END;
CREATE TRIGGER access_preset_grant_update AFTER UPDATE ON access_grants
WHEN OLD.member_id IS NOT NEW.member_id OR OLD.resource_kind IS NOT NEW.resource_kind
  OR OLD.project_id IS NOT NEW.project_id OR OLD.routine_id IS NOT NEW.routine_id
  OR OLD.operation IS NOT NEW.operation OR OLD.created_by IS NOT NEW.created_by
  OR OLD.created_at IS NOT NEW.created_at
BEGIN
    DELETE FROM access_preset_selections
    WHERE ((member_id=OLD.member_id AND resource_kind=OLD.resource_kind
        AND COALESCE(project_id,routine_id)=COALESCE(OLD.project_id,OLD.routine_id)
        AND EXISTS(SELECT 1 FROM json_each(managed_operations) WHERE value=OLD.operation))
       OR (member_id=NEW.member_id AND resource_kind=NEW.resource_kind
        AND COALESCE(project_id,routine_id)=COALESCE(NEW.project_id,NEW.routine_id)
        AND EXISTS(SELECT 1 FROM json_each(managed_operations) WHERE value=NEW.operation)));
END;
