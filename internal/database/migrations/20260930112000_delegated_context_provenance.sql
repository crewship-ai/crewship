CREATE TABLE access_context_delegations (
 entry_id TEXT NOT NULL REFERENCES access_context(id) ON DELETE CASCADE,
 source_context_id TEXT NOT NULL REFERENCES access_context(id) ON DELETE CASCADE,
 parent_attempt_id TEXT NOT NULL REFERENCES access_attempts(id) ON DELETE CASCADE,
 child_attempt_id TEXT NOT NULL REFERENCES access_attempts(id) ON DELETE CASCADE,
 PRIMARY KEY(entry_id,source_context_id),
 CHECK(entry_id<>source_context_id),
 CHECK(parent_attempt_id<>child_attempt_id)
);
CREATE INDEX access_context_delegations_source ON access_context_delegations(source_context_id);
CREATE TRIGGER access_context_delegations_immutable BEFORE UPDATE ON access_context_delegations
BEGIN SELECT RAISE(ABORT,'delegated provenance is immutable'); END;
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
 AND EXISTS(SELECT 1 FROM json_each(parent.rights) r WHERE json_extract(r.value,'$.kind')='agent' AND json_extract(r.value,'$.id')=child.agent_id AND json_extract(r.value,'$.operation')='delegate')
 AND NOT EXISTS(SELECT 1 FROM json_each(origin.rights) r WHERE NOT EXISTS(SELECT 1 FROM json_each(child.rights) c WHERE c.value=r.value))
 AND EXISTS(SELECT 1 FROM json_each(imported.sources) ids WHERE ids.value=source.id)
 ) THEN RAISE(ABORT,'invalid delegated provenance') END;
END;
CREATE TRIGGER access_context_delegations_delete BEFORE DELETE ON access_context_delegations
BEGIN UPDATE access_attempts SET revoked_at=COALESCE(revoked_at,strftime('%Y-%m-%dT%H:%M:%fZ','now')) WHERE id=OLD.child_attempt_id; END;
DROP TRIGGER access_context_dependencies_validate;
CREATE TRIGGER access_context_dependencies_validate BEFORE INSERT ON access_context_dependencies
BEGIN
 SELECT CASE WHEN NOT EXISTS(SELECT 1 FROM access_context c JOIN access_attempts a ON a.id=c.attempt_id JOIN access_attempts consumer ON consumer.id=NEW.attempt_id
 WHERE c.id=NEW.context_id AND c.attempt_id=NEW.source_attempt_id AND c.scope=NEW.scope AND a.revoked_at IS NULL AND consumer.revoked_at IS NULL AND consumer.completed_at IS NULL
 AND consumer.workspace_id=a.workspace_id AND consumer.chat_id=a.chat_id AND consumer.generation>a.generation
 AND consumer.chat_generation=a.chat_generation AND consumer.chat_revision=a.chat_revision
 AND ((consumer.agent_id=a.agent_id AND ((consumer.context_audience='' AND a.context_audience='' AND consumer.principal_id=a.principal_id AND consumer.member_id=a.member_id AND consumer.member_revision=a.member_revision)
 OR (consumer.context_audience<>'' AND consumer.context_audience=a.context_audience AND consumer.admission_operation='chat' AND a.admission_operation='chat')))
 OR EXISTS(SELECT 1 FROM access_context_delegations d WHERE d.child_attempt_id=consumer.id AND d.source_context_id=c.id)))
 THEN RAISE(ABORT,'invalid context origin') END;
END;
