CREATE TABLE access_context_dependencies (
 attempt_id TEXT NOT NULL REFERENCES access_attempts(id) ON DELETE CASCADE,
 context_id TEXT NOT NULL REFERENCES access_context(id) ON DELETE CASCADE,
 source_attempt_id TEXT NOT NULL REFERENCES access_attempts(id) ON DELETE CASCADE,
 scope TEXT NOT NULL,
 PRIMARY KEY(attempt_id,context_id),
 CHECK(attempt_id<>source_attempt_id)
);
CREATE INDEX access_context_dependencies_source ON access_context_dependencies(source_attempt_id);
CREATE TRIGGER access_context_dependencies_immutable BEFORE UPDATE ON access_context_dependencies BEGIN
 SELECT RAISE(ABORT,'context dependency is immutable');
END;
CREATE TRIGGER access_context_dependencies_validate BEFORE INSERT ON access_context_dependencies
BEGIN
 SELECT CASE WHEN NOT EXISTS(SELECT 1 FROM access_context c JOIN access_attempts a ON a.id=c.attempt_id JOIN access_attempts consumer ON consumer.id=NEW.attempt_id
 WHERE c.id=NEW.context_id AND c.attempt_id=NEW.source_attempt_id AND c.scope=NEW.scope AND a.revoked_at IS NULL AND consumer.revoked_at IS NULL AND consumer.completed_at IS NULL
 AND consumer.workspace_id=a.workspace_id AND consumer.principal_id=a.principal_id
 AND consumer.agent_id=a.agent_id AND consumer.chat_id=a.chat_id AND consumer.generation>a.generation
 AND consumer.member_id=a.member_id AND consumer.member_revision=a.member_revision
 AND consumer.chat_generation=a.chat_generation AND consumer.chat_revision=a.chat_revision)
 THEN RAISE(ABORT,'invalid context origin') END;
END;
CREATE TRIGGER access_context_source_revocation AFTER UPDATE OF revoked_at ON access_attempts
WHEN OLD.revoked_at IS NULL AND NEW.revoked_at IS NOT NULL
BEGIN
 UPDATE access_attempts SET revoked_at=NEW.revoked_at WHERE revoked_at IS NULL AND id IN (
 WITH RECURSIVE affected(id) AS (
 SELECT NEW.id
 UNION SELECT d.attempt_id FROM access_context_dependencies d JOIN affected a ON d.source_attempt_id=a.id
 UNION SELECT c.id FROM access_attempts c JOIN affected a ON c.parent_id=a.id
 ) SELECT id FROM affected);
END;
CREATE TRIGGER access_context_source_delete BEFORE DELETE ON access_attempts
BEGIN
 UPDATE access_attempts SET revoked_at=COALESCE(revoked_at,strftime('%Y-%m-%dT%H:%M:%fZ','now')) WHERE id IN (
 WITH RECURSIVE affected(id) AS (
 SELECT d.attempt_id FROM access_context_dependencies d WHERE d.source_attempt_id=OLD.id
 UNION SELECT d.attempt_id FROM access_context_dependencies d JOIN affected a ON d.source_attempt_id=a.id
 UNION SELECT c.id FROM access_attempts c JOIN affected a ON c.parent_id=a.id
 ) SELECT id FROM affected);
 DELETE FROM access_context WHERE attempt_id=OLD.id;
END;

CREATE TRIGGER access_context_dependency_delete BEFORE DELETE ON access_context_dependencies
BEGIN
 UPDATE access_attempts SET revoked_at=COALESCE(revoked_at,strftime('%Y-%m-%dT%H:%M:%fZ','now')) WHERE id=OLD.attempt_id;
END;
