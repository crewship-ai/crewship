-- Immutable private issue origin binding. This is runtime authority, not an
-- issue journal or a public receipt. The job FK is deferred for atomic enqueue.
CREATE TABLE restricted_preflight_reservations (
 origin_attempt_id TEXT PRIMARY KEY REFERENCES access_attempts(id),
 workflow_id TEXT NOT NULL UNIQUE REFERENCES restricted_workflow_jobs(id) ON DELETE CASCADE DEFERRABLE INITIALLY DEFERRED,
 workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 principal_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 member_id TEXT NOT NULL REFERENCES workspace_members(id) ON DELETE CASCADE,
 member_revision INTEGER NOT NULL,
 agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
 issue_id TEXT NOT NULL REFERENCES missions(id) ON DELETE CASCADE,
 project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
 source_hash TEXT NOT NULL CHECK(length(source_hash)=64),
 authority_hash TEXT NOT NULL CHECK(length(authority_hash)=64),
 recipe_hash TEXT NOT NULL,
 work_revision INTEGER NOT NULL,
 brief_revision INTEGER NOT NULL,
 created_at TEXT NOT NULL DEFAULT(strftime('%Y-%m-%dT%H:%M:%fZ','now')),
 UNIQUE(issue_id,principal_id,source_hash,authority_hash,recipe_hash)
);
CREATE INDEX restricted_preflight_issue ON restricted_preflight_reservations(issue_id);
CREATE TRIGGER restricted_preflight_immutable BEFORE UPDATE ON restricted_preflight_reservations
BEGIN SELECT RAISE(ABORT,'private source binding is immutable'); END;
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
CREATE TRIGGER restricted_preflight_job BEFORE INSERT ON restricted_workflow_jobs
WHEN NEW.source_facet='issue' AND NOT EXISTS(SELECT 1 FROM restricted_preflight_reservations r
 WHERE r.workflow_id=NEW.id AND r.origin_attempt_id=NEW.origin_attempt_id AND r.workspace_id=NEW.workspace_id
 AND r.principal_id=NEW.principal_id AND r.member_id=NEW.member_id AND r.member_revision=NEW.member_revision
 AND r.agent_id=NEW.agent_id AND r.recipe_hash=NEW.recipe_hash)
BEGIN SELECT RAISE(ABORT,'private source binding missing'); END;
CREATE TRIGGER restricted_preflight_delete BEFORE DELETE ON restricted_preflight_reservations
BEGIN UPDATE access_attempts SET revoked_at=COALESCE(revoked_at,strftime('%Y-%m-%dT%H:%M:%fZ','now')) WHERE id=OLD.origin_attempt_id; END;
CREATE TRIGGER restricted_preflight_issue_changed AFTER UPDATE OF project_id,status,delegate_agent_id,crew_id,workspace_id,title,description,owner_user_id,assignee_type,assignee_id,lead_agent_id ON missions
WHEN NEW.project_id IS NOT OLD.project_id OR NEW.status IS NOT OLD.status OR NEW.delegate_agent_id IS NOT OLD.delegate_agent_id
 OR NEW.crew_id IS NOT OLD.crew_id OR NEW.workspace_id IS NOT OLD.workspace_id OR NEW.title IS NOT OLD.title OR NEW.description IS NOT OLD.description
 OR NEW.owner_user_id IS NOT OLD.owner_user_id OR NEW.assignee_type IS NOT OLD.assignee_type OR NEW.assignee_id IS NOT OLD.assignee_id OR NEW.lead_agent_id IS NOT OLD.lead_agent_id
BEGIN UPDATE access_attempts SET revoked_at=COALESCE(revoked_at,strftime('%Y-%m-%dT%H:%M:%fZ','now')) WHERE id IN(SELECT origin_attempt_id FROM restricted_preflight_reservations WHERE issue_id=NEW.id); END;
CREATE TRIGGER restricted_preflight_work_changed AFTER UPDATE ON issue_work
BEGIN UPDATE access_attempts SET revoked_at=COALESCE(revoked_at,strftime('%Y-%m-%dT%H:%M:%fZ','now')) WHERE id IN(SELECT origin_attempt_id FROM restricted_preflight_reservations WHERE issue_id=NEW.mission_id); END;
CREATE TRIGGER restricted_preflight_project_changed AFTER UPDATE OF status,workspace_id ON projects
WHEN NEW.status IS NOT OLD.status OR NEW.workspace_id IS NOT OLD.workspace_id
BEGIN UPDATE access_attempts SET revoked_at=COALESCE(revoked_at,strftime('%Y-%m-%dT%H:%M:%fZ','now')) WHERE id IN(SELECT origin_attempt_id FROM restricted_preflight_reservations WHERE project_id=NEW.id); END;
CREATE TRIGGER restricted_preflight_assignment_insert BEFORE INSERT ON assignments
WHEN NEW.status NOT IN ('COMPLETED','FAILED','CANCELLED','TIMEOUT') AND EXISTS(SELECT 1 FROM restricted_preflight_reservations r JOIN restricted_workflow_jobs j ON j.id=r.workflow_id WHERE (r.issue_id=NEW.mission_id OR r.issue_id=NEW.chat_id OR r.issue_id=NEW.group_id) AND j.state IN ('pending','running'))
BEGIN SELECT RAISE(ABORT,'issue is busy'); END;
CREATE TRIGGER restricted_preflight_assignment_start BEFORE UPDATE OF status,mission_id,chat_id,group_id ON assignments
WHEN NEW.status NOT IN ('COMPLETED','FAILED','CANCELLED','TIMEOUT') AND EXISTS(SELECT 1 FROM restricted_preflight_reservations r JOIN restricted_workflow_jobs j ON j.id=r.workflow_id WHERE (r.issue_id=NEW.mission_id OR r.issue_id=NEW.chat_id OR r.issue_id=NEW.group_id) AND j.state IN ('pending','running'))
BEGIN SELECT RAISE(ABORT,'issue is busy'); END;
CREATE TRIGGER restricted_preflight_execution_insert BEFORE INSERT ON issue_executions
WHEN NEW.stage IN ('working','reviewing') AND EXISTS(SELECT 1 FROM restricted_preflight_reservations r JOIN restricted_workflow_jobs j ON j.id=r.workflow_id WHERE r.issue_id=NEW.mission_id AND j.state IN ('pending','running'))
BEGIN SELECT RAISE(ABORT,'issue is busy'); END;
CREATE TRIGGER restricted_preflight_execution_start BEFORE UPDATE OF stage,mission_id ON issue_executions
WHEN NEW.stage IN ('working','reviewing') AND EXISTS(SELECT 1 FROM restricted_preflight_reservations r JOIN restricted_workflow_jobs j ON j.id=r.workflow_id WHERE r.issue_id=NEW.mission_id AND j.state IN ('pending','running'))
BEGIN SELECT RAISE(ABORT,'issue is busy'); END;
