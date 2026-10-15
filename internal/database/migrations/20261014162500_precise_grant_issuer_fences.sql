-- Fence only live-to-inert transitions. BEFORE triggers inspect the old live
-- view, so already-inert shares never invalidate unrelated, newly admitted work.
DROP TRIGGER access_issuer_role;
DROP TRIGGER access_issuer_membership_insert;
DROP TRIGGER access_issuer_membership_delete;
DROP TRIGGER access_issuer_suspension;
DROP TRIGGER access_issuer_workspace;

CREATE TRIGGER access_issuer_role BEFORE UPDATE OF role,access_mode ON workspace_members
WHEN (OLD.access_mode<>'restricted' AND NEW.access_mode='restricted')
 OR (OLD.role IN ('OWNER','ADMIN') AND OLD.access_mode='trusted'
     AND (NEW.role NOT IN ('OWNER','ADMIN') OR NEW.access_mode<>'trusted'))
BEGIN
 UPDATE workspace_members SET access_revision=access_revision+1
 WHERE EXISTS(SELECT 1 FROM access_grants g
   WHERE g.member_id=workspace_members.id AND g.created_by=OLD.user_id
   AND ((OLD.access_mode<>'restricted' AND NEW.access_mode='restricted')
        OR workspace_members.workspace_id=OLD.workspace_id)
   AND EXISTS(SELECT 1 FROM live_access_grants live WHERE live.id=g.id));
END;
CREATE TRIGGER access_issuer_membership_insert BEFORE INSERT ON workspace_members
WHEN NEW.access_mode='restricted'
BEGIN
 UPDATE workspace_members SET access_revision=access_revision+1
 WHERE EXISTS(SELECT 1 FROM access_grants g
   WHERE g.member_id=workspace_members.id AND g.created_by=NEW.user_id
   AND EXISTS(SELECT 1 FROM live_access_grants live WHERE live.id=g.id));
END;
CREATE TRIGGER access_issuer_membership_delete BEFORE DELETE ON workspace_members
WHEN OLD.role IN ('OWNER','ADMIN') AND OLD.access_mode='trusted'
BEGIN
 UPDATE workspace_members SET access_revision=access_revision+1
 WHERE workspace_id=OLD.workspace_id AND EXISTS(SELECT 1 FROM access_grants g
   WHERE g.member_id=workspace_members.id AND g.created_by=OLD.user_id
   AND EXISTS(SELECT 1 FROM live_access_grants live WHERE live.id=g.id));
END;
CREATE TRIGGER access_issuer_suspension BEFORE UPDATE OF suspended_at ON users
WHEN OLD.suspended_at IS NULL AND NEW.suspended_at IS NOT NULL
BEGIN
 UPDATE workspace_members SET access_revision=access_revision+1
 WHERE EXISTS(SELECT 1 FROM access_grants g
   WHERE g.member_id=workspace_members.id AND g.created_by=OLD.id
   AND EXISTS(SELECT 1 FROM live_access_grants live WHERE live.id=g.id));
END;
CREATE TRIGGER access_issuer_workspace AFTER UPDATE OF deleted_at ON workspaces
WHEN OLD.deleted_at IS NULL AND NEW.deleted_at IS NOT NULL
BEGIN
 UPDATE workspace_members SET access_revision=access_revision+1 WHERE workspace_id=NEW.id;
END;
