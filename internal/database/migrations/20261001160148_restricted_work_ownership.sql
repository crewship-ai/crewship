-- Legacy graph roots predate shared fencing; retire their executable handles.
-- Completed origins stay as currently authorized, read-only receipt provenance.
UPDATE access_attempts SET revoked_at=COALESCE(revoked_at,strftime('%Y-%m-%dT%H:%M:%fZ','now'))
WHERE completed_at IS NULL AND chat_id IN (SELECT chat_id FROM restricted_workflow_jobs WHERE state='running');
-- Retain private domain authority and outputs; adopt only scheduling identity.
-- Legacy started work has unknown effects and is parked, never blindly replayed.
INSERT INTO work_items(id,workspace_id,source,domain_kind,domain_id,agent_id,session_id,class,state,state_reason,generation,attempts,eligible_at,deadline_at,created_at,updated_at,terminal_at)
SELECT id,workspace_id,'manual','restricted_workflow',id,agent_id,chat_id,'background',
 CASE state WHEN 'pending' THEN 'queued' WHEN 'running' THEN 'needs_reconciliation'
 WHEN 'completed' THEN 'succeeded' WHEN 'canceled' THEN 'cancelled' ELSE 'failed' END,
 CASE WHEN state='running' THEN 'Legacy private runtime requires reconciliation; external effects are unknown' ELSE '' END,
 CASE WHEN state='running' THEN 1 ELSE 0 END,CASE WHEN state='running' THEN 1 ELSE 0 END,fire_at,expires_at,created_at,created_at,finished_at
FROM restricted_workflow_jobs;
INSERT INTO work_attempts(run_id,work_id,attempt,generation,lease_owner,lease_expires_at,heartbeat_at,runtime_locator,runtime_phase,started_at,start_reason,ended_at,end_reason)
SELECT id,id,1,1,'legacy-private-adoption',updated_at,updated_at,'restricted-legacy:'||id,'starting',created_at,
 'Imported legacy private execution; no new runtime started',updated_at,'External effects unknown; operator reconciliation required'
FROM work_items WHERE domain_kind='restricted_workflow' AND state='needs_reconciliation';
INSERT INTO work_events(work_id,seq,at,from_state,to_state,generation,reason)
SELECT id,1,updated_at,'',state,generation,'Adopted private workflow scheduling identity'
FROM work_items WHERE domain_kind='restricted_workflow';
CREATE UNIQUE INDEX work_restricted_domain ON work_items(domain_kind,domain_id) WHERE domain_kind='restricted_workflow';
-- Each graph's private authority root is fenced by its common dispatch attempt.
CREATE TABLE restricted_workflow_attempt_roots (
 -- Stop identities survive removal of the authority/member/domain row.
 access_attempt_id TEXT PRIMARY KEY,
 workflow_id TEXT NOT NULL,
 run_id TEXT NOT NULL REFERENCES work_attempts(run_id) ON DELETE CASCADE,
 generation INTEGER NOT NULL
);
CREATE INDEX restricted_workflow_roots_run ON restricted_workflow_attempt_roots(run_id);
CREATE INDEX restricted_workflow_roots_workflow ON restricted_workflow_attempt_roots(workflow_id);
CREATE TRIGGER restricted_workflow_roots_immutable BEFORE UPDATE ON restricted_workflow_attempt_roots
BEGIN SELECT RAISE(ABORT,'workflow dispatch root is immutable'); END;

-- Every descendant retains its own immutable stop identity and dispatch fence.
CREATE TRIGGER restricted_workflow_descendant_binding AFTER INSERT ON access_attempts
WHEN NEW.parent_id IS NOT NULL
BEGIN
 INSERT INTO restricted_workflow_attempt_roots(access_attempt_id,workflow_id,run_id,generation)
 SELECT NEW.id,workflow_id,run_id,generation FROM restricted_workflow_attempt_roots WHERE access_attempt_id=NEW.parent_id;
END;
