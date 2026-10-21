-- Account-wide Guest mode and workspace administration are mutually exclusive.
-- Existing rows remain intact; new writes cannot create or extend a conflict.
CREATE TRIGGER guest_administrator_insert BEFORE INSERT ON workspace_members
WHEN (NEW.role IN ('OWNER','ADMIN') AND
       (NEW.access_mode='restricted' OR EXISTS (
        SELECT 1 FROM workspace_members WHERE user_id=NEW.user_id AND access_mode='restricted')))
  OR (NEW.access_mode='restricted' AND EXISTS (
       SELECT 1 FROM workspace_members WHERE user_id=NEW.user_id AND role IN ('OWNER','ADMIN')))
BEGIN SELECT RAISE(ABORT,'guest administrator conflict'); END;

CREATE TRIGGER guest_administrator_update BEFORE UPDATE OF user_id,role,access_mode ON workspace_members
WHEN (NEW.role IN ('OWNER','ADMIN') AND
       (NEW.access_mode='restricted' OR EXISTS (
        SELECT 1 FROM workspace_members WHERE user_id=NEW.user_id AND id!=OLD.id AND access_mode='restricted')))
  OR (NEW.access_mode='restricted' AND EXISTS (
       SELECT 1 FROM workspace_members WHERE user_id=NEW.user_id AND id!=OLD.id AND role IN ('OWNER','ADMIN')))
BEGIN SELECT RAISE(ABORT,'guest administrator conflict'); END;
