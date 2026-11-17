-- Persist an explicit crew account/model rule while materializing exact
-- per-agent payers. Existing bindings keep their meaning (NULL metadata).
ALTER TABLE credential_bindings ADD COLUMN bedrock_model TEXT
    CHECK (bedrock_model IS NULL OR
        (scope='CREW' AND slot='AWS_BEARER_TOKEN_BEDROCK' AND length(bedrock_model)>0));
ALTER TABLE credential_bindings ADD COLUMN inherited_from_binding_id TEXT
    REFERENCES credential_bindings(id) ON DELETE CASCADE
    CHECK (inherited_from_binding_id IS NULL OR
        (scope='AGENT' AND slot='AWS_BEARER_TOKEN_BEDROCK'));
ALTER TABLE credential_bindings ADD COLUMN bedrock_model_overridden INTEGER NOT NULL DEFAULT 0
    CHECK (bedrock_model_overridden IN (0,1));
CREATE INDEX idx_credential_bindings_inherited ON credential_bindings(inherited_from_binding_id);

-- The database boundary covers API creation, hiring, manifests and internal
-- creation alike. No lazy read or later run can race the account assignment.
CREATE TRIGGER trg_bedrock_crew_agent_insert AFTER INSERT ON agents
WHEN NEW.crew_id IS NOT NULL AND NEW.deleted_at IS NULL
BEGIN
    -- Leaving a crew removes only its inherited binding. Explicit per-agent
    -- assignments survive a move and are never silently replaced.
    DELETE FROM credential_bindings
    WHERE agent_id=NEW.id AND inherited_from_binding_id IS NOT NULL
      AND NOT EXISTS (
          SELECT 1 FROM credential_bindings parent
          WHERE parent.id=credential_bindings.inherited_from_binding_id
            AND parent.crew_id=NEW.crew_id AND parent.workspace_id=NEW.workspace_id
      );

    INSERT INTO credential_bindings
        (id,workspace_id,credential_id,scope,agent_id,slot,created_by,inherited_from_binding_id,bedrock_model_overridden)
    SELECT 'c' || lower(hex(randomblob(12))), NEW.workspace_id, parent.credential_id,
           'AGENT', NEW.id, 'AWS_BEARER_TOKEN_BEDROCK', parent.created_by, parent.id,
           CASE WHEN (SELECT llm_provider FROM agents WHERE id=NEW.id)='AWS_BEDROCK'
             AND COALESCE((SELECT llm_model FROM agents WHERE id=NEW.id),'') LIKE 'amazon-bedrock/%' THEN 1 ELSE 0 END
    FROM credential_bindings parent
    WHERE parent.scope='CREW' AND parent.crew_id=NEW.crew_id
      AND parent.workspace_id=NEW.workspace_id AND parent.bedrock_model IS NOT NULL
      AND parent.slot='AWS_BEARER_TOKEN_BEDROCK'
      AND NOT EXISTS (SELECT 1 FROM credential_bindings own
          WHERE own.agent_id=NEW.id AND own.scope='AGENT' AND own.slot='AWS_BEARER_TOKEN_BEDROCK')
      AND NOT EXISTS (SELECT 1 FROM agent_credentials legacy
          WHERE legacy.agent_id=NEW.id AND legacy.env_var_name='AWS_BEARER_TOKEN_BEDROCK');

    UPDATE agents SET cli_adapter='OPENCODE',llm_provider='AWS_BEDROCK',
        llm_model=CASE WHEN EXISTS(SELECT 1 FROM credential_bindings own WHERE own.agent_id=NEW.id AND own.inherited_from_binding_id IS NOT NULL AND own.bedrock_model_overridden=1)
          THEN llm_model ELSE 'amazon-bedrock/' || (
            SELECT parent.bedrock_model FROM credential_bindings own
            JOIN credential_bindings parent ON parent.id=own.inherited_from_binding_id
            WHERE own.agent_id=NEW.id AND parent.crew_id=NEW.crew_id
              AND parent.workspace_id=NEW.workspace_id) END
    WHERE id=NEW.id AND EXISTS (
        SELECT 1 FROM credential_bindings own
        JOIN credential_bindings parent ON parent.id=own.inherited_from_binding_id
        WHERE own.agent_id=NEW.id AND parent.crew_id=NEW.crew_id
          AND parent.workspace_id=NEW.workspace_id AND parent.bedrock_model IS NOT NULL);
END;

CREATE TRIGGER trg_bedrock_crew_agent_move AFTER UPDATE OF crew_id,deleted_at ON agents
WHEN NEW.deleted_at IS NULL AND (NEW.crew_id IS NOT OLD.crew_id
    OR OLD.deleted_at IS NOT NULL)
BEGIN
    -- An inherited default model belongs to the old crew. Keep only an
    -- individually overridden model when moving to another account rule.
    UPDATE agents SET llm_model=NULL WHERE id=NEW.id AND EXISTS (
        SELECT 1 FROM credential_bindings own WHERE own.agent_id=NEW.id
          AND own.inherited_from_binding_id IS NOT NULL AND own.bedrock_model_overridden=0);
    -- Leaving a crew removes only its inherited binding. Explicit per-agent
    -- assignments survive a move and are never silently replaced.
    DELETE FROM credential_bindings
    WHERE agent_id=NEW.id AND inherited_from_binding_id IS NOT NULL
      AND NOT EXISTS (
          SELECT 1 FROM credential_bindings parent
          WHERE parent.id=credential_bindings.inherited_from_binding_id
            AND parent.crew_id=NEW.crew_id AND parent.workspace_id=NEW.workspace_id
      );

    INSERT INTO credential_bindings
        (id,workspace_id,credential_id,scope,agent_id,slot,created_by,inherited_from_binding_id,bedrock_model_overridden)
    SELECT 'c' || lower(hex(randomblob(12))), NEW.workspace_id, parent.credential_id,
           'AGENT', NEW.id, 'AWS_BEARER_TOKEN_BEDROCK', parent.created_by, parent.id,
           CASE WHEN (SELECT llm_provider FROM agents WHERE id=NEW.id)='AWS_BEDROCK'
             AND COALESCE((SELECT llm_model FROM agents WHERE id=NEW.id),'') LIKE 'amazon-bedrock/%' THEN 1 ELSE 0 END
    FROM credential_bindings parent
    WHERE parent.scope='CREW' AND parent.crew_id=NEW.crew_id
      AND parent.workspace_id=NEW.workspace_id AND parent.bedrock_model IS NOT NULL
      AND parent.slot='AWS_BEARER_TOKEN_BEDROCK'
      AND NOT EXISTS (SELECT 1 FROM credential_bindings own
          WHERE own.agent_id=NEW.id AND own.scope='AGENT' AND own.slot='AWS_BEARER_TOKEN_BEDROCK')
      AND NOT EXISTS (SELECT 1 FROM agent_credentials legacy
          WHERE legacy.agent_id=NEW.id AND legacy.env_var_name='AWS_BEARER_TOKEN_BEDROCK');

    UPDATE agents SET cli_adapter='OPENCODE',llm_provider='AWS_BEDROCK',
        llm_model=CASE WHEN EXISTS(SELECT 1 FROM credential_bindings own WHERE own.agent_id=NEW.id AND own.inherited_from_binding_id IS NOT NULL AND own.bedrock_model_overridden=1)
          THEN llm_model ELSE 'amazon-bedrock/' || (
            SELECT parent.bedrock_model FROM credential_bindings own
            JOIN credential_bindings parent ON parent.id=own.inherited_from_binding_id
            WHERE own.agent_id=NEW.id AND parent.crew_id=NEW.crew_id
              AND parent.workspace_id=NEW.workspace_id) END
    WHERE id=NEW.id AND EXISTS (
        SELECT 1 FROM credential_bindings own
        JOIN credential_bindings parent ON parent.id=own.inherited_from_binding_id
        WHERE own.agent_id=NEW.id AND parent.crew_id=NEW.crew_id
          AND parent.workspace_id=NEW.workspace_id AND parent.bedrock_model IS NOT NULL);
END;

-- Metadata is meaningful only for a Bedrock login, and inherited copies must
-- point to the exact same account in the agent's current crew/workspace.
CREATE TRIGGER trg_bedrock_binding_metadata_insert BEFORE INSERT ON credential_bindings
BEGIN
    SELECT RAISE(ABORT,'Bedrock crew model requires a Bedrock login')
    WHERE NEW.bedrock_model IS NOT NULL AND NOT EXISTS (
        SELECT 1 FROM credentials c WHERE c.id=NEW.credential_id
          AND c.provider='AWS_BEDROCK' AND c.type='PROVIDER_LOGIN');
    SELECT RAISE(ABORT,'invalid inherited Bedrock binding')
    WHERE NEW.inherited_from_binding_id IS NOT NULL AND NOT EXISTS (
        SELECT 1 FROM credential_bindings parent JOIN agents a ON a.id=NEW.agent_id
        WHERE parent.id=NEW.inherited_from_binding_id AND parent.scope='CREW'
          AND parent.slot=NEW.slot AND parent.bedrock_model IS NOT NULL
          AND parent.credential_id=NEW.credential_id
          AND parent.workspace_id=NEW.workspace_id AND a.workspace_id=NEW.workspace_id
          AND a.crew_id=parent.crew_id);
END;

CREATE TRIGGER trg_bedrock_binding_metadata_update BEFORE UPDATE ON credential_bindings
BEGIN
    SELECT RAISE(ABORT,'Bedrock crew model requires a Bedrock login')
    WHERE NEW.bedrock_model IS NOT NULL AND NOT EXISTS (
        SELECT 1 FROM credentials c WHERE c.id=NEW.credential_id
          AND c.provider='AWS_BEDROCK' AND c.type='PROVIDER_LOGIN');
    SELECT RAISE(ABORT,'invalid inherited Bedrock binding')
    WHERE NEW.inherited_from_binding_id IS NOT NULL AND NOT EXISTS (
        SELECT 1 FROM credential_bindings parent JOIN agents a ON a.id=NEW.agent_id
        WHERE parent.id=NEW.inherited_from_binding_id AND parent.scope='CREW'
          AND parent.slot=NEW.slot AND parent.bedrock_model IS NOT NULL
          AND parent.credential_id=NEW.credential_id
          AND parent.workspace_id=NEW.workspace_id AND a.workspace_id=NEW.workspace_id
          AND a.crew_id=parent.crew_id);
END;

-- Explicit manifest/legacy grants may be written after the agent itself.
-- Replace the inferred account, retaining the explicitly requested model.
CREATE TRIGGER trg_bedrock_explicit_grant_insert AFTER INSERT ON agent_credentials
WHEN NEW.env_var_name='AWS_BEARER_TOKEN_BEDROCK'
BEGIN
    DELETE FROM credential_bindings WHERE agent_id=NEW.agent_id
      AND slot='AWS_BEARER_TOKEN_BEDROCK' AND inherited_from_binding_id IS NOT NULL;
END;
CREATE TRIGGER trg_bedrock_explicit_grant_update
AFTER UPDATE OF agent_id,credential_id,env_var_name ON agent_credentials
WHEN NEW.env_var_name='AWS_BEARER_TOKEN_BEDROCK'
BEGIN
    DELETE FROM credential_bindings WHERE agent_id=NEW.agent_id
      AND slot='AWS_BEARER_TOKEN_BEDROCK' AND inherited_from_binding_id IS NOT NULL;
END;
