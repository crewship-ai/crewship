ALTER TABLE access_attempts ADD COLUMN admission_operation TEXT NOT NULL DEFAULT 'run' CHECK(admission_operation IN ('chat','run'));
CREATE TRIGGER access_attempt_operation_immutable BEFORE UPDATE OF admission_operation ON access_attempts
WHEN NEW.admission_operation IS NOT OLD.admission_operation
BEGIN
 SELECT RAISE(ABORT,'attempt operation is immutable');
END;
ALTER TABLE agents ADD COLUMN restricted_execution_profile TEXT NOT NULL DEFAULT 'disabled' CHECK(restricted_execution_profile IN ('disabled','responses_text','native_api_key'));
ALTER TABLE restricted_provider_bindings ADD COLUMN execution_profile TEXT NOT NULL DEFAULT 'disabled' CHECK(execution_profile IN ('disabled','responses_text','native_api_key'));
CREATE TRIGGER restricted_execution_profile_revokes AFTER UPDATE OF restricted_execution_profile ON agents
WHEN NEW.restricted_execution_profile IS NOT OLD.restricted_execution_profile
BEGIN
 UPDATE access_attempts SET revoked_at=COALESCE(revoked_at,strftime('%Y-%m-%dT%H:%M:%fZ','now'))
 WHERE agent_id=OLD.id AND EXISTS(SELECT 1 FROM restricted_provider_bindings b WHERE b.attempt_id=access_attempts.id);
END;
CREATE TRIGGER restricted_binding_profile_immutable BEFORE UPDATE OF execution_profile ON restricted_provider_bindings
WHEN NEW.execution_profile IS NOT OLD.execution_profile
BEGIN
 SELECT RAISE(ABORT,'provider profile is immutable');
END;
