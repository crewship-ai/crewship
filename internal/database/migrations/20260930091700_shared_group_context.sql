ALTER TABLE access_attempts ADD COLUMN context_audience TEXT NOT NULL DEFAULT '';
CREATE TRIGGER access_attempt_context_audience_immutable BEFORE UPDATE OF context_audience ON access_attempts
WHEN NEW.context_audience IS NOT OLD.context_audience
BEGIN
 SELECT RAISE(ABORT,'attempt context audience is immutable');
END;
DROP TRIGGER access_context_dependencies_validate;
CREATE TRIGGER access_context_dependencies_validate BEFORE INSERT ON access_context_dependencies
BEGIN
 SELECT CASE WHEN NOT EXISTS(SELECT 1 FROM access_context c JOIN access_attempts a ON a.id=c.attempt_id JOIN access_attempts consumer ON consumer.id=NEW.attempt_id
 WHERE c.id=NEW.context_id AND c.attempt_id=NEW.source_attempt_id AND c.scope=NEW.scope AND a.revoked_at IS NULL AND consumer.revoked_at IS NULL AND consumer.completed_at IS NULL
 AND consumer.workspace_id=a.workspace_id
 AND consumer.agent_id=a.agent_id AND consumer.chat_id=a.chat_id AND consumer.generation>a.generation
 AND consumer.chat_generation=a.chat_generation AND consumer.chat_revision=a.chat_revision
 AND ((consumer.context_audience='' AND a.context_audience='' AND consumer.principal_id=a.principal_id AND consumer.member_id=a.member_id AND consumer.member_revision=a.member_revision)
 OR (consumer.context_audience<>'' AND consumer.context_audience=a.context_audience AND consumer.admission_operation='chat' AND a.admission_operation='chat')))
 THEN RAISE(ABORT,'invalid context origin') END;
END;
