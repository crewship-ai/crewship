-- A queued legacy label is not proof of absence when it still names an exec.
-- Refuse the upgrade; never enqueue this ambiguous process under a new owner.
CREATE TEMP TABLE assignment_work_upgrade_guard (ok INTEGER);
CREATE TEMP TRIGGER assignment_work_upgrade_refuse
BEFORE INSERT ON assignment_work_upgrade_guard
WHEN EXISTS(SELECT 1 FROM assignments WHERE status IN ('PENDING','QUEUED')
 AND (COALESCE(exec_id,'')!='' OR COALESCE(exec_container_id,'')!='')
 AND NOT EXISTS(SELECT 1 FROM work_items w WHERE w.source='assignment' AND w.domain_kind='assignment' AND w.workspace_id=assignments.workspace_id AND w.domain_id=assignments.id))
BEGIN
 SELECT RAISE(ABORT,'assignment work upgrade refused: queued legacy exec requires reconciliation; see docs/runbooks/assignment-work-upgrade.md');
END;
INSERT INTO assignment_work_upgrade_guard VALUES(1);
DROP TRIGGER assignment_work_upgrade_refuse;
DROP TABLE assignment_work_upgrade_guard;

-- A logical assignment has one work owner. Manual replay must create a new
-- assignment; it cannot reuse an already terminal domain row.
CREATE UNIQUE INDEX idx_work_assignment_domain ON work_items(workspace_id,domain_id)
WHERE source='assignment' AND domain_kind='assignment' AND replay_of IS NULL;

-- Only work which has not executed can be handed to the new dispatcher.
-- Legacy RUNNING rows retain their old ownership and count against admission.
INSERT INTO work_items(id,workspace_id,source,source_ref,domain_kind,domain_id,agent_id,crew_id,session_id,class,authorized_by_user_id,authorized_scope,input_json,state,generation,attempts,eligible_at,created_at,updated_at)
SELECT 'assignment:'||a.id,a.workspace_id,'assignment',a.id,'assignment',a.id,a.assigned_to_id,COALESCE(ag.crew_id,''),COALESCE(a.session_id,''),'background',COALESCE(a.created_by_user_id,''),'assignment:'||a.id,
 json_object('version',1,'assignment_id',a.id,'workspace_id',a.workspace_id,'agent_id',a.assigned_to_id,'crew_id',COALESCE(ag.crew_id,''),'task',a.task,'author_agent_id',COALESCE(a.assigned_by_id,'')),
 'queued',0,0,a.created_at,a.created_at,a.created_at
FROM assignments a JOIN agents ag ON ag.id=a.assigned_to_id AND ag.workspace_id=a.workspace_id
WHERE a.status IN ('PENDING','QUEUED')
AND NOT EXISTS(SELECT 1 FROM work_items w WHERE w.source='assignment' AND w.domain_kind='assignment' AND w.domain_id=a.id AND w.workspace_id=a.workspace_id);

ALTER TABLE assignments ADD COLUMN work_projected_at TEXT;
ALTER TABLE assignments ADD COLUMN work_projection_attempted_at TEXT;
ALTER TABLE mission_tasks ADD COLUMN completion_assignment_id TEXT;

ALTER TABLE assignments ADD COLUMN work_inbox_projected_at TEXT;

ALTER TABLE missions ADD COLUMN planning_assignment_id TEXT;
UPDATE missions SET planning_assignment_id=(SELECT a.id FROM assignments a WHERE a.workspace_id=missions.workspace_id AND a.mission_id=missions.id AND a.lead_planning=1 ORDER BY a.created_at,a.id LIMIT 1);
