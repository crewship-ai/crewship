CREATE TABLE restricted_workflow_provider_policies (
 job_id TEXT NOT NULL REFERENCES restricted_workflow_jobs(id) ON DELETE CASCADE,
 agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
 credential_id TEXT NOT NULL REFERENCES credentials(id) ON DELETE CASCADE,
 grant_id TEXT NOT NULL REFERENCES agent_credentials(id) ON DELETE CASCADE,
 policy_hash TEXT NOT NULL,
 max_output_tokens INTEGER NOT NULL CHECK(max_output_tokens BETWEEN 1 AND 32768),
 PRIMARY KEY(job_id,agent_id)
);
CREATE TABLE restricted_workflow_delegate_slots (
 parent_attempt_id TEXT NOT NULL REFERENCES access_attempts(id) ON DELETE CASCADE,
 job_id TEXT NOT NULL,
 agent_id TEXT NOT NULL,
 policy_hash TEXT NOT NULL,
 max_output_tokens INTEGER NOT NULL CHECK(max_output_tokens BETWEEN 1 AND 32768),
 PRIMARY KEY(parent_attempt_id,agent_id),
 FOREIGN KEY(job_id,agent_id) REFERENCES restricted_workflow_provider_policies(job_id,agent_id) ON DELETE CASCADE
);
CREATE TRIGGER restricted_workflow_provider_policy_immutable BEFORE UPDATE ON restricted_workflow_provider_policies
BEGIN SELECT RAISE(ABORT,'workflow provider policy is immutable'); END;
CREATE TRIGGER restricted_workflow_delegate_slot_immutable BEFORE UPDATE ON restricted_workflow_delegate_slots
BEGIN SELECT RAISE(ABORT,'workflow delegation slot is immutable'); END;
CREATE TRIGGER restricted_workflow_provider_policy_delete BEFORE DELETE ON restricted_workflow_provider_policies
BEGIN
 UPDATE access_attempts SET revoked_at=COALESCE(revoked_at,strftime('%Y-%m-%dT%H:%M:%fZ','now')) WHERE id IN(SELECT origin_attempt_id FROM restricted_workflow_jobs WHERE id=OLD.job_id);
 UPDATE access_attempts SET revoked_at=COALESCE(revoked_at,strftime('%Y-%m-%dT%H:%M:%fZ','now')) WHERE id IN(SELECT parent_attempt_id FROM restricted_workflow_delegate_slots WHERE job_id=OLD.job_id AND agent_id=OLD.agent_id);
END;
CREATE TRIGGER restricted_workflow_delegate_slot_delete BEFORE DELETE ON restricted_workflow_delegate_slots
BEGIN UPDATE access_attempts SET revoked_at=COALESCE(revoked_at,strftime('%Y-%m-%dT%H:%M:%fZ','now')) WHERE id=OLD.parent_attempt_id; END;
CREATE TRIGGER restricted_workflow_provider_key_update AFTER UPDATE OF id,workspace_id,encrypted_value,type,provider,status,security_level,handle_only,deleted_at,token_expires_at ON credentials
WHEN OLD.id IS NOT NEW.id OR OLD.workspace_id IS NOT NEW.workspace_id OR OLD.encrypted_value IS NOT NEW.encrypted_value
 OR OLD.type IS NOT NEW.type OR OLD.provider IS NOT NEW.provider OR OLD.status IS NOT NEW.status
 OR OLD.security_level IS NOT NEW.security_level OR OLD.handle_only IS NOT NEW.handle_only
 OR OLD.deleted_at IS NOT NEW.deleted_at OR OLD.token_expires_at IS NOT NEW.token_expires_at
BEGIN
 UPDATE access_attempts SET revoked_at=COALESCE(revoked_at,strftime('%Y-%m-%dT%H:%M:%fZ','now')) WHERE id IN(SELECT j.origin_attempt_id FROM restricted_workflow_jobs j JOIN restricted_workflow_provider_policies p ON p.job_id=j.id WHERE p.credential_id=OLD.id);
 UPDATE access_attempts SET revoked_at=COALESCE(revoked_at,strftime('%Y-%m-%dT%H:%M:%fZ','now')) WHERE id IN(SELECT s.parent_attempt_id FROM restricted_workflow_delegate_slots s JOIN restricted_workflow_provider_policies p ON p.job_id=s.job_id AND p.agent_id=s.agent_id WHERE p.credential_id=OLD.id);
END;
CREATE TRIGGER restricted_workflow_provider_grant_update AFTER UPDATE ON agent_credentials
BEGIN
 UPDATE access_attempts SET revoked_at=COALESCE(revoked_at,strftime('%Y-%m-%dT%H:%M:%fZ','now')) WHERE id IN(SELECT j.origin_attempt_id FROM restricted_workflow_jobs j JOIN restricted_workflow_provider_policies p ON p.job_id=j.id WHERE p.agent_id IN(OLD.agent_id,NEW.agent_id));
 UPDATE access_attempts SET revoked_at=COALESCE(revoked_at,strftime('%Y-%m-%dT%H:%M:%fZ','now')) WHERE id IN(SELECT parent_attempt_id FROM restricted_workflow_delegate_slots WHERE agent_id IN(OLD.agent_id,NEW.agent_id));
END;
CREATE TRIGGER restricted_workflow_provider_grant_insert AFTER INSERT ON agent_credentials
BEGIN
 UPDATE access_attempts SET revoked_at=COALESCE(revoked_at,strftime('%Y-%m-%dT%H:%M:%fZ','now')) WHERE id IN(SELECT j.origin_attempt_id FROM restricted_workflow_jobs j JOIN restricted_workflow_provider_policies p ON p.job_id=j.id WHERE p.agent_id=NEW.agent_id);
 UPDATE access_attempts SET revoked_at=COALESCE(revoked_at,strftime('%Y-%m-%dT%H:%M:%fZ','now')) WHERE id IN(SELECT parent_attempt_id FROM restricted_workflow_delegate_slots WHERE agent_id=NEW.agent_id);
END;
CREATE TRIGGER restricted_workflow_provider_agent_update AFTER UPDATE OF llm_model,llm_provider,restricted_execution_profile,workspace_id,crew_id,slug,deleted_at ON agents
WHEN OLD.llm_model IS NOT NEW.llm_model OR OLD.llm_provider IS NOT NEW.llm_provider OR OLD.restricted_execution_profile IS NOT NEW.restricted_execution_profile OR OLD.workspace_id IS NOT NEW.workspace_id OR OLD.crew_id IS NOT NEW.crew_id OR OLD.slug IS NOT NEW.slug OR OLD.deleted_at IS NOT NEW.deleted_at
BEGIN
 UPDATE access_attempts SET revoked_at=COALESCE(revoked_at,strftime('%Y-%m-%dT%H:%M:%fZ','now')) WHERE id IN(SELECT j.origin_attempt_id FROM restricted_workflow_jobs j JOIN restricted_workflow_provider_policies p ON p.job_id=j.id WHERE p.agent_id=OLD.id);
 UPDATE access_attempts SET revoked_at=COALESCE(revoked_at,strftime('%Y-%m-%dT%H:%M:%fZ','now')) WHERE id IN(SELECT parent_attempt_id FROM restricted_workflow_delegate_slots WHERE agent_id=OLD.id);
END;

ALTER TABLE access_attempts ADD COLUMN workflow_continuation INTEGER NOT NULL DEFAULT 0 CHECK(workflow_continuation IN (0,1));
CREATE TRIGGER access_workflow_continuation_immutable BEFORE UPDATE OF workflow_continuation ON access_attempts
BEGIN SELECT RAISE(ABORT,'workflow continuation is immutable'); END;
CREATE TRIGGER access_workflow_continuation_insert BEFORE INSERT ON access_attempts
WHEN NEW.workflow_continuation=1 AND NOT EXISTS(
 SELECT 1 FROM access_attempts parent
 JOIN restricted_workflow_delegate_slots slot ON slot.parent_attempt_id=parent.id AND slot.agent_id=NEW.agent_id
 JOIN restricted_workflow_jobs job ON job.id=slot.job_id
 JOIN access_attempts origin ON origin.id=job.origin_attempt_id
 WHERE parent.id=NEW.parent_id AND parent.parent_id IS NULL AND parent.agent_id=NEW.agent_id AND job.agent_id=NEW.agent_id
 AND parent.admission_operation='run' AND NEW.admission_operation='run' AND parent.context_audience='' AND NEW.context_audience=''
 AND parent.revoked_at IS NULL AND parent.completed_at IS NULL AND origin.revoked_at IS NULL AND job.state='running'
 AND NEW.principal_id=parent.principal_id AND NEW.workspace_id=parent.workspace_id AND NEW.chat_id=parent.chat_id
 AND NEW.member_id=parent.member_id AND NEW.member_revision=parent.member_revision
 AND job.principal_id=parent.principal_id AND job.workspace_id=parent.workspace_id AND job.chat_id=parent.chat_id
 AND job.member_id=parent.member_id AND job.member_revision=parent.member_revision
 AND NOT EXISTS(SELECT 1 FROM json_each(NEW.rights) r WHERE NOT EXISTS(SELECT 1 FROM json_each(parent.rights) p WHERE p.value=r.value))
)
BEGIN SELECT RAISE(ABORT,'workflow continuation requires original frozen slot'); END;

DROP TRIGGER access_context_delegations_validate;
CREATE TRIGGER access_context_delegations_validate BEFORE INSERT ON access_context_delegations
BEGIN
 SELECT CASE WHEN NOT EXISTS(
 SELECT 1 FROM access_context imported JOIN access_attempts child ON child.id=imported.attempt_id
 JOIN access_attempts parent ON parent.id=child.parent_id
 JOIN access_context source ON source.id=NEW.source_context_id JOIN access_attempts origin ON origin.id=source.attempt_id
 WHERE imported.id=NEW.entry_id AND imported.kind='summary' AND imported.role='derived'
 AND child.id=NEW.child_attempt_id AND parent.id=NEW.parent_attempt_id
 AND child.revoked_at IS NULL AND child.completed_at IS NULL AND parent.revoked_at IS NULL AND parent.completed_at IS NULL AND origin.revoked_at IS NULL
 AND child.admission_operation='run' AND parent.admission_operation='run' AND origin.admission_operation='run'
 AND child.context_audience='' AND parent.context_audience='' AND origin.context_audience=''
 AND imported.scope=source.scope AND child.workspace_id=origin.workspace_id AND child.principal_id=origin.principal_id
 AND child.chat_id=origin.chat_id AND child.member_id=origin.member_id AND child.member_revision=origin.member_revision
 AND child.chat_generation=origin.chat_generation AND child.chat_revision=origin.chat_revision AND origin.generation<child.generation
 AND (origin.agent_id=parent.agent_id OR origin.parent_id=parent.id)
 AND (EXISTS(SELECT 1 FROM json_each(parent.rights) r WHERE json_extract(r.value,'$.kind')='agent' AND json_extract(r.value,'$.id')=child.agent_id AND json_extract(r.value,'$.operation')='delegate')
 OR (child.workflow_continuation=1 AND child.agent_id=parent.agent_id AND parent.parent_id IS NULL
 AND EXISTS(SELECT 1 FROM restricted_workflow_delegate_slots slot JOIN restricted_workflow_jobs job ON job.id=slot.job_id
 WHERE slot.parent_attempt_id=parent.id AND slot.agent_id=child.agent_id AND job.agent_id=child.agent_id AND job.state='running')))
 AND NOT EXISTS(SELECT 1 FROM json_each(child.rights) r WHERE NOT EXISTS(SELECT 1 FROM json_each(parent.rights) p WHERE p.value=r.value))
 AND NOT EXISTS(SELECT 1 FROM json_each(origin.rights) r WHERE NOT EXISTS(SELECT 1 FROM json_each(child.rights) c WHERE c.value=r.value))
 AND EXISTS(SELECT 1 FROM json_each(imported.sources) ids WHERE ids.value=source.id)
 ) THEN RAISE(ABORT,'invalid delegated provenance') END;
END;
