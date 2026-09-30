-- A provider choice belongs to one admitted attempt, not to client metadata.
-- Credential/grant IDs deliberately remain tombstones after deletion. Removing
-- a binding must never turn a prepared network launch into an offline launch.
CREATE TABLE restricted_provider_bindings (
    attempt_id TEXT PRIMARY KEY REFERENCES access_attempts(id) ON DELETE CASCADE,
    credential_id TEXT NOT NULL,
    grant_id TEXT NOT NULL,
    credential_revision TEXT NOT NULL,
    model TEXT NOT NULL,
    max_output_tokens INTEGER NOT NULL CHECK (max_output_tokens BETWEEN 1 AND 32768)
);
CREATE INDEX restricted_provider_credential ON restricted_provider_bindings(credential_id);
CREATE INDEX restricted_provider_grant ON restricted_provider_bindings(grant_id);
CREATE TRIGGER restricted_provider_before_launch BEFORE INSERT ON restricted_provider_bindings
WHEN EXISTS (SELECT 1 FROM restricted_launches WHERE attempt_id=NEW.attempt_id)
BEGIN
    SELECT RAISE(ABORT, 'provider binding must precede launch preparation');
END;
CREATE TRIGGER restricted_provider_immutable BEFORE UPDATE ON restricted_provider_bindings BEGIN
    SELECT RAISE(ABORT, 'restricted provider binding is immutable');
END;
CREATE TRIGGER restricted_provider_remove BEFORE DELETE ON restricted_provider_bindings BEGIN
    UPDATE access_attempts SET revoked_at=COALESCE(revoked_at,strftime('%Y-%m-%dT%H:%M:%fZ','now')) WHERE id=OLD.attempt_id;
END;

-- Invalidate in the mutation transaction, including revoke/regrant between two
-- controller polls. Checking only the current ACTIVE flag cannot provide this.
CREATE TRIGGER restricted_provider_credential_delete BEFORE DELETE ON credentials BEGIN
    UPDATE access_attempts SET revoked_at=COALESCE(revoked_at,strftime('%Y-%m-%dT%H:%M:%fZ','now'))
    WHERE id IN (SELECT attempt_id FROM restricted_provider_bindings WHERE credential_id=OLD.id);
END;
CREATE TRIGGER restricted_provider_credential_insert BEFORE INSERT ON credentials BEGIN
    UPDATE access_attempts SET revoked_at=COALESCE(revoked_at,strftime('%Y-%m-%dT%H:%M:%fZ','now'))
    WHERE id IN (SELECT attempt_id FROM restricted_provider_bindings WHERE credential_id=NEW.id);
END;
CREATE TRIGGER restricted_provider_credential_update AFTER UPDATE OF
    id,workspace_id,encrypted_value,type,provider,status,security_level,handle_only,deleted_at,token_expires_at
ON credentials WHEN OLD.id IS NOT NEW.id OR OLD.workspace_id IS NOT NEW.workspace_id
    OR OLD.encrypted_value IS NOT NEW.encrypted_value OR OLD.type IS NOT NEW.type
    OR OLD.provider IS NOT NEW.provider OR OLD.status IS NOT NEW.status
    OR OLD.security_level IS NOT NEW.security_level OR OLD.handle_only IS NOT NEW.handle_only
    OR OLD.deleted_at IS NOT NEW.deleted_at OR OLD.token_expires_at IS NOT NEW.token_expires_at
BEGIN
    UPDATE access_attempts SET revoked_at=COALESCE(revoked_at,strftime('%Y-%m-%dT%H:%M:%fZ','now'))
    WHERE id IN (SELECT attempt_id FROM restricted_provider_bindings WHERE credential_id=OLD.id);
END;
CREATE TRIGGER restricted_provider_grant_delete BEFORE DELETE ON agent_credentials BEGIN
    UPDATE access_attempts SET revoked_at=COALESCE(revoked_at,strftime('%Y-%m-%dT%H:%M:%fZ','now'))
    WHERE id IN (SELECT attempt_id FROM restricted_provider_bindings WHERE grant_id=OLD.id);
END;
CREATE TRIGGER restricted_provider_grant_insert BEFORE INSERT ON agent_credentials BEGIN
    UPDATE access_attempts SET revoked_at=COALESCE(revoked_at,strftime('%Y-%m-%dT%H:%M:%fZ','now'))
    WHERE id IN (SELECT attempt_id FROM restricted_provider_bindings WHERE grant_id=NEW.id);
END;
CREATE TRIGGER restricted_provider_grant_update AFTER UPDATE ON agent_credentials BEGIN
    UPDATE access_attempts SET revoked_at=COALESCE(revoked_at,strftime('%Y-%m-%dT%H:%M:%fZ','now'))
    WHERE id IN (SELECT attempt_id FROM restricted_provider_bindings WHERE grant_id=OLD.id);
END;
CREATE TRIGGER restricted_provider_agent_update AFTER UPDATE OF llm_provider,llm_model,workspace_id,deleted_at ON agents
WHEN OLD.llm_provider IS NOT NEW.llm_provider OR OLD.llm_model IS NOT NEW.llm_model
    OR OLD.workspace_id IS NOT NEW.workspace_id OR OLD.deleted_at IS NOT NEW.deleted_at
BEGIN
    UPDATE access_attempts SET revoked_at=COALESCE(revoked_at,strftime('%Y-%m-%dT%H:%M:%fZ','now'))
    WHERE agent_id=OLD.id AND id IN (SELECT attempt_id FROM restricted_provider_bindings);
END;
