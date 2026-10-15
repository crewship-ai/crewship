-- Web/API execution is independent of chat audience/grant churn. Membership
-- identity fences remove/rejoin; only changes to execution permissions bump it.
ALTER TABLE workspace_members ADD COLUMN web_execution_revision INTEGER NOT NULL DEFAULT 1
    CHECK (web_execution_revision > 0);
CREATE TRIGGER web_execution_member_update AFTER UPDATE OF role, capabilities, access_mode ON workspace_members
WHEN OLD.role IS NOT NEW.role OR OLD.capabilities IS NOT NEW.capabilities OR OLD.access_mode IS NOT NEW.access_mode
BEGIN
    UPDATE workspace_members SET web_execution_revision=web_execution_revision+1 WHERE id=NEW.id;
END;
