-- Extend the existing resource policy; preserve shipped grants and revisions.
DROP TRIGGER restricted_preflight_origin;
DROP TRIGGER access_grant_insert;
DROP TRIGGER access_grant_delete;
DROP TRIGGER access_grant_update;
DROP TRIGGER access_agent_delete;
DROP TRIGGER access_agent_scope;
DROP TRIGGER access_project_delete;
DROP TRIGGER access_project_scope;
CREATE TABLE access_grants_next (
    id TEXT PRIMARY KEY,
    member_id TEXT NOT NULL REFERENCES workspace_members(id) ON DELETE CASCADE,
    resource_kind TEXT NOT NULL CHECK (resource_kind IN ('agent','project','routine')),
    agent_id TEXT REFERENCES agents(id) ON DELETE CASCADE,
    project_id TEXT REFERENCES projects(id) ON DELETE CASCADE,
    routine_id TEXT REFERENCES pipelines(id) ON DELETE CASCADE,
    operation TEXT NOT NULL,
    created_by TEXT NOT NULL REFERENCES users(id),
    created_at TEXT NOT NULL,
    CHECK ((resource_kind='agent' AND agent_id IS NOT NULL AND project_id IS NULL AND routine_id IS NULL)
        OR (resource_kind='project' AND project_id IS NOT NULL AND agent_id IS NULL AND routine_id IS NULL)
        OR (resource_kind='routine' AND routine_id IS NOT NULL AND agent_id IS NULL AND project_id IS NULL)),
    UNIQUE (member_id, agent_id, operation),
    UNIQUE (member_id, project_id, operation),
    UNIQUE (member_id, routine_id, operation)
);


INSERT INTO access_grants_next(id,member_id,resource_kind,agent_id,project_id,operation,created_by,created_at)
SELECT id,member_id,resource_kind,agent_id,project_id,operation,created_by,created_at FROM access_grants;
DROP TABLE access_grants;
ALTER TABLE access_grants_next RENAME TO access_grants;

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
 AND EXISTS(SELECT 1 FROM access_grants g WHERE g.member_id=a.member_id AND g.project_id=p.id AND g.resource_kind='project' AND g.operation='read'))
BEGIN SELECT RAISE(ABORT,'private source authority denied'); END;

CREATE TRIGGER access_grant_insert AFTER INSERT ON access_grants BEGIN
    UPDATE workspace_members SET access_revision=access_revision+1 WHERE id=NEW.member_id;
END;

CREATE TRIGGER access_grant_delete AFTER DELETE ON access_grants BEGIN
    UPDATE workspace_members SET access_revision=access_revision+1 WHERE id=OLD.member_id;
END;

CREATE TRIGGER access_grant_update AFTER UPDATE ON access_grants BEGIN
    UPDATE workspace_members SET access_revision=access_revision+1 WHERE id IN (OLD.member_id,NEW.member_id);
END;

CREATE TRIGGER access_agent_delete BEFORE DELETE ON agents BEGIN
    DELETE FROM access_grants WHERE resource_kind='agent' AND agent_id=OLD.id;
END;

CREATE TRIGGER access_agent_scope AFTER UPDATE OF deleted_at,workspace_id ON agents
WHEN NEW.deleted_at IS NOT NULL OR OLD.workspace_id IS NOT NEW.workspace_id BEGIN
    DELETE FROM access_grants WHERE resource_kind='agent' AND agent_id=OLD.id;
END;

CREATE TRIGGER access_project_delete BEFORE DELETE ON projects BEGIN
    DELETE FROM access_grants WHERE resource_kind='project' AND project_id=OLD.id;
END;

CREATE TRIGGER access_project_scope AFTER UPDATE OF workspace_id ON projects
WHEN OLD.workspace_id IS NOT NEW.workspace_id BEGIN
    DELETE FROM access_grants WHERE resource_kind='project' AND project_id=OLD.id;
END;

CREATE INDEX access_grants_agent ON access_grants(agent_id);
CREATE INDEX access_grants_project ON access_grants(project_id);
CREATE INDEX access_grants_creator ON access_grants(created_by);
CREATE INDEX access_grants_routine ON access_grants(routine_id);
CREATE TRIGGER access_routine_delete BEFORE DELETE ON pipelines BEGIN
    DELETE FROM access_grants WHERE resource_kind='routine' AND routine_id=OLD.id;
END;
CREATE TRIGGER access_routine_scope AFTER UPDATE OF deleted_at,workspace_id ON pipelines
WHEN NEW.deleted_at IS NOT NULL OR OLD.workspace_id IS NOT NEW.workspace_id BEGIN
    DELETE FROM access_grants WHERE resource_kind='routine' AND routine_id=OLD.id;
END;
-- Existing private jobs did not bind routine authority. Do not revive them
-- merely by adding a grant after the rollout.
UPDATE workspace_members SET access_revision=access_revision+1 WHERE access_mode='restricted';

ALTER TABLE workspace_invitations ADD COLUMN access_mode TEXT NOT NULL DEFAULT 'trusted' CHECK(access_mode IN ('trusted','restricted'));
ALTER TABLE workspace_invitations ADD COLUMN rights_json TEXT NOT NULL DEFAULT '[]' CHECK(json_valid(rights_json) AND json_type(rights_json)='array');
