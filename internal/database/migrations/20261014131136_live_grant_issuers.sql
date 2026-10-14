-- Grants are usable only while their issuer remains a live trusted admin.
-- Stored shares become inert when the issuer loses authority, as in Pages.
-- Fence admitted work permanently even if the issuer later regains authority.
CREATE VIEW live_access_grants AS
SELECT g.* FROM access_grants g
JOIN workspace_members recipient ON recipient.id=g.member_id
JOIN workspaces w ON w.id=recipient.workspace_id AND w.deleted_at IS NULL
JOIN workspace_members issuer ON issuer.user_id=g.created_by AND issuer.workspace_id=recipient.workspace_id
JOIN users actor ON actor.id=issuer.user_id
WHERE issuer.role IN ('OWNER','ADMIN') AND issuer.access_mode='trusted'
AND actor.suspended_at IS NULL
AND NOT EXISTS(SELECT 1 FROM workspace_members ceiling WHERE ceiling.user_id=actor.id AND ceiling.access_mode='restricted');

-- Preserve all stored shares and metadata; fence work backed by an inert issuer.
UPDATE workspace_members SET access_revision=access_revision+1
WHERE id IN (SELECT member_id FROM access_grants WHERE id NOT IN (SELECT id FROM live_access_grants));

CREATE TRIGGER access_issuer_role AFTER UPDATE OF role,access_mode ON workspace_members
BEGIN
    UPDATE workspace_members SET access_revision=access_revision+1
    WHERE id IN (SELECT member_id FROM access_grants WHERE created_by=NEW.user_id
    AND id NOT IN (SELECT id FROM live_access_grants));
END;
CREATE TRIGGER access_issuer_membership_insert AFTER INSERT ON workspace_members
WHEN NEW.access_mode='restricted'
BEGIN
    UPDATE workspace_members SET access_revision=access_revision+1
    WHERE id IN (SELECT member_id FROM access_grants WHERE created_by=NEW.user_id);
END;
CREATE TRIGGER access_issuer_membership_delete AFTER DELETE ON workspace_members
BEGIN
    UPDATE workspace_members SET access_revision=access_revision+1
    WHERE id IN (SELECT member_id FROM access_grants WHERE created_by=OLD.user_id
    AND id NOT IN (SELECT id FROM live_access_grants));
END;
CREATE TRIGGER access_issuer_suspension AFTER UPDATE OF suspended_at ON users
WHEN NEW.suspended_at IS NOT NULL
BEGIN
    UPDATE workspace_members SET access_revision=access_revision+1
    WHERE id IN (SELECT member_id FROM access_grants WHERE created_by=NEW.id);
END;
CREATE TRIGGER access_issuer_delete BEFORE DELETE ON users
BEGIN
    DELETE FROM access_grants WHERE created_by=OLD.id;
END;
CREATE TRIGGER access_issuer_workspace AFTER UPDATE OF deleted_at ON workspaces
WHEN NEW.deleted_at IS NOT NULL
BEGIN
    UPDATE workspace_members SET access_revision=access_revision+1 WHERE workspace_id=NEW.id;
END;

DROP TRIGGER restricted_preflight_origin;
CREATE TRIGGER restricted_preflight_origin BEFORE INSERT ON restricted_preflight_reservations
WHEN NOT EXISTS(SELECT 1 FROM access_attempts a JOIN workspace_members wm ON wm.id=a.member_id
 JOIN missions m ON m.id=NEW.issue_id JOIN issue_work iw ON iw.mission_id=m.id
 JOIN projects p ON p.id=NEW.project_id
 WHERE a.id=NEW.origin_attempt_id AND a.workspace_id=NEW.workspace_id AND a.principal_id=NEW.principal_id
 AND a.member_id=NEW.member_id AND a.member_revision=NEW.member_revision AND a.agent_id=NEW.agent_id
 AND a.completed_at IS NOT NULL AND a.revoked_at IS NULL AND wm.access_mode='restricted' AND wm.access_revision=a.member_revision
 AND m.workspace_id=a.workspace_id AND m.delegate_agent_id=a.agent_id AND m.project_id=p.id AND p.workspace_id=a.workspace_id
 AND m.mission_type='issue' AND m.status='TODO' AND iw.mode='agent' AND iw.revision=NEW.work_revision AND iw.brief_revision=NEW.brief_revision
 AND EXISTS(SELECT 1 FROM json_each(a.rights) j WHERE json_extract(j.value,'$.kind')='project' AND json_extract(j.value,'$.id')=p.id AND json_extract(j.value,'$.operation')='read')
 AND EXISTS(SELECT 1 FROM live_access_grants g WHERE g.member_id=a.member_id AND g.project_id=p.id AND g.resource_kind='project' AND g.operation='read'))
BEGIN SELECT RAISE(ABORT,'private source authority denied'); END;
