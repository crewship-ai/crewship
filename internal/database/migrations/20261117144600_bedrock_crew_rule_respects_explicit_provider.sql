-- A crew's Bedrock account rule is a default, not an override (#5446).
--
-- 20261117120910 turned every agent created in or moved into a rule crew into
-- an OpenCode/Bedrock agent, including one created or kept on another
-- provider on purpose, and cleared the model of an agent leaving such a crew
-- while leaving it on AWS_BEDROCK: no account and no model, so its runs
-- failed. The rule now applies only to agents that are on Bedrock already or
-- have no provider chosen. Agents on another provider keep their runner,
-- provider and model; an agent leaving a rule crew keeps its model.

DROP TRIGGER IF EXISTS trg_bedrock_crew_agent_insert;
DROP TRIGGER IF EXISTS trg_bedrock_crew_agent_move;

CREATE TRIGGER trg_bedrock_crew_agent_insert AFTER INSERT ON agents
WHEN NEW.crew_id IS NOT NULL AND NEW.deleted_at IS NULL
  AND COALESCE(NEW.llm_provider,'') IN ('','AWS_BEDROCK')
BEGIN
    INSERT INTO credential_bindings
        (id,workspace_id,credential_id,scope,agent_id,slot,created_by,inherited_from_binding_id,bedrock_model_overridden)
    SELECT 'c' || lower(hex(randomblob(12))), NEW.workspace_id, parent.credential_id,
           'AGENT', NEW.id, 'AWS_BEARER_TOKEN_BEDROCK', parent.created_by, parent.id,
           -- Overridden only when the agent names a Bedrock model other
           -- than the rule's: the rule's own model stays a crew default.
           CASE WHEN COALESCE(NEW.llm_provider,'')='AWS_BEDROCK'
             AND COALESCE(NEW.llm_model,'') LIKE 'amazon-bedrock/%'
             AND NEW.llm_model <> 'amazon-bedrock/' || parent.bedrock_model THEN 1 ELSE 0 END
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
    -- A model the old crew's rule chose is replaced only by the new crew's
    -- rule; with no rule there, the agent keeps a usable model.
    UPDATE agents SET llm_model=NULL WHERE id=NEW.id
      AND EXISTS (SELECT 1 FROM credential_bindings own WHERE own.agent_id=NEW.id
          AND own.inherited_from_binding_id IS NOT NULL AND own.bedrock_model_overridden=0)
      AND EXISTS (SELECT 1 FROM credential_bindings parent WHERE parent.scope='CREW'
          AND parent.crew_id=NEW.crew_id AND parent.workspace_id=NEW.workspace_id
          AND parent.slot='AWS_BEARER_TOKEN_BEDROCK' AND parent.bedrock_model IS NOT NULL);
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
           CASE WHEN COALESCE((SELECT llm_model FROM agents WHERE id=NEW.id),'') LIKE 'amazon-bedrock/%'
             AND (SELECT llm_model FROM agents WHERE id=NEW.id) <> 'amazon-bedrock/' || parent.bedrock_model THEN 1 ELSE 0 END
    FROM credential_bindings parent
    WHERE parent.scope='CREW' AND parent.crew_id=NEW.crew_id
      AND parent.workspace_id=NEW.workspace_id AND parent.bedrock_model IS NOT NULL
      AND parent.slot='AWS_BEARER_TOKEN_BEDROCK'
      AND COALESCE((SELECT llm_provider FROM agents WHERE id=NEW.id),'') IN ('','AWS_BEDROCK')
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
