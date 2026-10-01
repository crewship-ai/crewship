-- A crashed filesystem snapshot must not allow a controller to restart writers.
CREATE TABLE service_backup_fences (
 crew_id TEXT PRIMARY KEY REFERENCES crews(id) ON DELETE CASCADE,
 workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 token TEXT NOT NULL CHECK(length(token)=32),
 created_at TEXT NOT NULL,
 operation TEXT NOT NULL CHECK(operation IN ('backup','restore'))
);
CREATE TRIGGER service_backup_fence_intent_insert
BEFORE INSERT ON service_runtime_intents
WHEN EXISTS (SELECT 1 FROM service_backup_fences WHERE crew_id=NEW.crew_id)
BEGIN SELECT RAISE(ABORT,'service_backup_maintenance'); END;
CREATE TRIGGER service_backup_fence_intent_update
BEFORE UPDATE OF desired_state,version ON service_runtime_intents
WHEN EXISTS (SELECT 1 FROM service_backup_fences WHERE crew_id=OLD.crew_id)
BEGIN SELECT RAISE(ABORT,'service_backup_maintenance'); END;
CREATE TRIGGER service_backup_fence_intent_delete
BEFORE DELETE ON service_runtime_intents
WHEN EXISTS (SELECT 1 FROM service_backup_fences WHERE crew_id=OLD.crew_id)
BEGIN SELECT RAISE(ABORT,'service_backup_maintenance'); END;
CREATE TRIGGER service_backup_fence_configuration
BEFORE UPDATE OF services_json,workspace_id,slug ON crews
WHEN EXISTS (SELECT 1 FROM service_backup_fences WHERE crew_id=OLD.id)
BEGIN SELECT RAISE(ABORT,'service_backup_maintenance'); END;
CREATE TRIGGER service_backup_fence_crew_delete
BEFORE DELETE ON crews
WHEN EXISTS (SELECT 1 FROM service_backup_fences WHERE crew_id=OLD.id)
BEGIN SELECT RAISE(ABORT,'service_backup_maintenance'); END;
CREATE TRIGGER service_backup_fence_crew_retire
BEFORE UPDATE OF deleted_at ON crews
WHEN EXISTS (SELECT 1 FROM service_backup_fences WHERE crew_id=OLD.id)
BEGIN SELECT RAISE(ABORT,'service_backup_maintenance'); END;

CREATE TRIGGER service_backup_fence_workspace_delete
BEFORE DELETE ON workspaces
WHEN EXISTS (SELECT 1 FROM service_backup_fences WHERE workspace_id=OLD.id)
BEGIN SELECT RAISE(ABORT,'service_backup_maintenance'); END;
