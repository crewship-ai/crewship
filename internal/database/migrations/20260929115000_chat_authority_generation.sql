-- A queued human message must not regain authority after a revoke/restore or
-- after a chat ID is reused. These are server-owned admission fences.
ALTER TABLE chats ADD COLUMN authority_generation TEXT NOT NULL DEFAULT '';
ALTER TABLE chats ADD COLUMN authority_revision INTEGER NOT NULL DEFAULT 1;
UPDATE chats SET authority_generation=lower(hex(randomblob(16)));
CREATE TRIGGER chat_authority_insert AFTER INSERT ON chats BEGIN
    UPDATE chats SET authority_generation=lower(hex(randomblob(16))) WHERE id=NEW.id;
END;
CREATE TRIGGER chat_authority_change AFTER UPDATE OF visibility,created_by,agent_id,workspace_id,origin,mode ON chats
WHEN OLD.visibility IS NOT NEW.visibility OR OLD.created_by IS NOT NEW.created_by
 OR OLD.agent_id IS NOT NEW.agent_id OR OLD.workspace_id IS NOT NEW.workspace_id
 OR OLD.origin IS NOT NEW.origin OR OLD.mode IS NOT NEW.mode
BEGIN
    UPDATE chats SET authority_revision=authority_revision+1 WHERE id=NEW.id;
END;
CREATE TRIGGER chat_authority_agent_change AFTER UPDATE OF deleted_at,workspace_id,crew_id ON agents
WHEN OLD.deleted_at IS NOT NEW.deleted_at OR OLD.workspace_id IS NOT NEW.workspace_id OR OLD.crew_id IS NOT NEW.crew_id
BEGIN
    UPDATE chats SET authority_revision=authority_revision+1 WHERE agent_id=NEW.id;
END;
CREATE TRIGGER chat_authority_workspace_change AFTER UPDATE OF deleted_at ON workspaces
WHEN OLD.deleted_at IS NOT NEW.deleted_at
BEGIN
    UPDATE chats SET authority_revision=authority_revision+1 WHERE workspace_id=NEW.id;
END;
