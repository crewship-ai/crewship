-- Retain immutable history/mandates while permitting the existing workspace
-- identity-erasure operation to remove only a human attribution. The sentinel
-- is terminal; no other column, including revocation, can change with it.
DROP TRIGGER mooring_artifact_edit_immutable;
CREATE TRIGGER mooring_artifact_edit_immutable BEFORE UPDATE ON mooring_collab_artifact_edits
WHEN NOT (OLD.human_user_id IS NOT 'erased' AND NEW.human_user_id IS 'erased'
 AND NEW.page_id IS OLD.page_id
 AND NEW.workspace_id IS OLD.workspace_id
 AND NEW.application_id IS OLD.application_id
 AND NEW.change_id IS OLD.change_id
 AND NEW.membership_hash IS OLD.membership_hash
 AND NEW.first_revision IS OLD.first_revision
 AND NEW.last_revision IS OLD.last_revision
 AND NEW.host_session_id IS OLD.host_session_id
 AND NEW.actor_kind IS OLD.actor_kind
 AND NEW.agent_id IS OLD.agent_id
 AND NEW.crew_id IS OLD.crew_id
 AND NEW.chat_id IS OLD.chat_id
 AND NEW.message_id IS OLD.message_id
 AND NEW.work_id IS OLD.work_id
 AND NEW.run_id IS OLD.run_id
 AND NEW.attempt_number IS OLD.attempt_number
 AND NEW.attempt_generation IS OLD.attempt_generation
 AND NEW.created_at IS OLD.created_at)
BEGIN SELECT RAISE(ABORT,'artifact edit attribution is immutable'); END;

-- Standalone revocation keeps its original terminal trigger. Including
-- revoked_at in the erasure comparison prevents that exception changing it.
DROP TRIGGER mooring_intent_immutable;
CREATE TRIGGER mooring_intent_immutable BEFORE UPDATE OF page_id,mandate_id,run_id,human_user_id,page_version,policy_version,generation,base_rev,command,fingerprint ON mooring_intents
WHEN NOT (OLD.human_user_id IS NOT 'erased' AND NEW.human_user_id IS 'erased'
 AND NEW.page_id IS OLD.page_id
 AND NEW.mandate_id IS OLD.mandate_id
 AND NEW.run_id IS OLD.run_id
 AND NEW.page_version IS OLD.page_version
 AND NEW.policy_version IS OLD.policy_version
 AND NEW.generation IS OLD.generation
 AND NEW.base_rev IS OLD.base_rev
 AND NEW.command IS OLD.command
 AND NEW.fingerprint IS OLD.fingerprint
 AND NEW.revoked_at IS OLD.revoked_at)
BEGIN SELECT RAISE(ABORT,'Mooring mandate identity is immutable'); END;
