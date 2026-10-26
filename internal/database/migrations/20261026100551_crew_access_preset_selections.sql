-- Informational crew intent only; access_grants remains canonical authority.
-- Keep the shipped project/routine metadata table unchanged so a prior binary
-- can still read its known choices after a rollback. No automatic backfill.
CREATE TABLE crew_access_preset_selections (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    member_id TEXT NOT NULL REFERENCES workspace_members(id) ON DELETE CASCADE,
    resource_kind TEXT NOT NULL CHECK(resource_kind='crew'),
    crew_id TEXT NOT NULL REFERENCES crews(id) ON DELETE CASCADE,
    preset_id TEXT NOT NULL,
    preset_version INTEGER NOT NULL CHECK(preset_version>0),
    level TEXT NOT NULL,
    definition_hash TEXT NOT NULL,
    managed_operations TEXT NOT NULL CHECK(json_valid(managed_operations) AND json_type(managed_operations)='array'),
    selected_by TEXT REFERENCES users(id) ON DELETE SET NULL,
    selected_at TEXT NOT NULL,
    UNIQUE(member_id,crew_id,preset_id,preset_version)
);
CREATE INDEX idx_crew_access_preset_workspace ON crew_access_preset_selections(workspace_id,member_id);
CREATE INDEX idx_crew_access_preset_crew ON crew_access_preset_selections(crew_id);
CREATE INDEX idx_crew_access_preset_selector ON crew_access_preset_selections(selected_by);

CREATE TRIGGER crew_access_preset_binding_insert BEFORE INSERT ON crew_access_preset_selections
WHEN NOT EXISTS(SELECT 1 FROM workspace_members WHERE id=NEW.member_id AND workspace_id=NEW.workspace_id)
  OR NOT EXISTS(SELECT 1 FROM crews WHERE id=NEW.crew_id AND workspace_id=NEW.workspace_id AND deleted_at IS NULL AND kind<>'setup')
BEGIN SELECT RAISE(ABORT,'crew preset selection workspace mismatch'); END;
CREATE TRIGGER crew_access_preset_binding_update BEFORE UPDATE ON crew_access_preset_selections
WHEN NOT EXISTS(SELECT 1 FROM workspace_members WHERE id=NEW.member_id AND workspace_id=NEW.workspace_id)
  OR NOT EXISTS(SELECT 1 FROM crews WHERE id=NEW.crew_id AND workspace_id=NEW.workspace_id AND deleted_at IS NULL AND kind<>'setup')
BEGIN SELECT RAISE(ABORT,'crew preset selection workspace mismatch'); END;

CREATE TRIGGER crew_access_preset_grant_insert AFTER INSERT ON access_grants
WHEN NEW.resource_kind='crew'
BEGIN
    DELETE FROM crew_access_preset_selections
    WHERE member_id=NEW.member_id AND crew_id=NEW.crew_id
      AND EXISTS(SELECT 1 FROM json_each(managed_operations) WHERE value=NEW.operation);
END;
CREATE TRIGGER crew_access_preset_grant_delete AFTER DELETE ON access_grants
WHEN OLD.resource_kind='crew'
BEGIN
    DELETE FROM crew_access_preset_selections
    WHERE member_id=OLD.member_id AND crew_id=OLD.crew_id
      AND EXISTS(SELECT 1 FROM json_each(managed_operations) WHERE value=OLD.operation);
END;
CREATE TRIGGER crew_access_preset_grant_update AFTER UPDATE ON access_grants
WHEN OLD.member_id IS NOT NEW.member_id OR OLD.resource_kind IS NOT NEW.resource_kind
  OR OLD.crew_id IS NOT NEW.crew_id OR OLD.operation IS NOT NEW.operation
  OR OLD.created_by IS NOT NEW.created_by OR OLD.created_at IS NOT NEW.created_at
BEGIN
    DELETE FROM crew_access_preset_selections
    WHERE ((OLD.resource_kind='crew' AND member_id=OLD.member_id AND crew_id=OLD.crew_id
        AND EXISTS(SELECT 1 FROM json_each(managed_operations) WHERE value=OLD.operation))
       OR (NEW.resource_kind='crew' AND member_id=NEW.member_id AND crew_id=NEW.crew_id
        AND EXISTS(SELECT 1 FROM json_each(managed_operations) WHERE value=NEW.operation)));
END;
