CREATE TABLE restricted_workflow_jobs (
 id TEXT PRIMARY KEY,
 workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 principal_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 member_id TEXT NOT NULL REFERENCES workspace_members(id) ON DELETE CASCADE,
 member_revision INTEGER NOT NULL,
 pipeline_id TEXT NOT NULL REFERENCES pipelines(id) ON DELETE CASCADE,
 recipe_hash TEXT NOT NULL,
 recipe_json TEXT NOT NULL,
 agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
 execution_profile TEXT NOT NULL CHECK(execution_profile IN ('responses_text','native_api_key')),
 chat_id TEXT NOT NULL REFERENCES chats(id) ON DELETE CASCADE,
 origin_attempt_id TEXT NOT NULL REFERENCES access_attempts(id),
 origin_handle_ciphertext TEXT NOT NULL,
 source_kind TEXT NOT NULL CHECK(source_kind IN ('manual','page')),
 page_id TEXT,
 page_action_json TEXT NOT NULL DEFAULT '',
 page_spec_hash TEXT NOT NULL DEFAULT '',
 inputs_json TEXT NOT NULL,
 idempotency_key_hash TEXT NOT NULL DEFAULT '',
 state TEXT NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','running','completed','failed','canceled')),
 outputs_json TEXT NOT NULL DEFAULT '{}',
 created_at TEXT NOT NULL,
 fire_at TEXT NOT NULL,
 expires_at TEXT NOT NULL,
 finished_at TEXT,
 CHECK((source_kind='manual' AND page_id IS NULL AND page_action_json='') OR (source_kind='page' AND page_id IS NOT NULL AND page_action_json<>''))
);
CREATE UNIQUE INDEX restricted_workflow_jobs_idempotency ON restricted_workflow_jobs(workspace_id,principal_id,idempotency_key_hash) WHERE idempotency_key_hash<>'';
CREATE INDEX restricted_workflow_jobs_due ON restricted_workflow_jobs(state,fire_at);
CREATE INDEX restricted_workflow_jobs_actor ON restricted_workflow_jobs(workspace_id,principal_id,created_at);
CREATE TRIGGER restricted_workflow_jobs_identity_immutable BEFORE UPDATE OF workspace_id,principal_id,member_id,member_revision,pipeline_id,recipe_hash,recipe_json,agent_id,execution_profile,chat_id,origin_attempt_id,origin_handle_ciphertext,source_kind,page_id,page_action_json,page_spec_hash,inputs_json,idempotency_key_hash,created_at,fire_at,expires_at ON restricted_workflow_jobs
BEGIN SELECT RAISE(ABORT,'workflow authority is immutable'); END;
CREATE TRIGGER restricted_workflow_jobs_terminal_immutable BEFORE UPDATE ON restricted_workflow_jobs
WHEN OLD.state IN ('completed','failed','canceled') AND (NEW.state IS NOT OLD.state OR NEW.outputs_json IS NOT OLD.outputs_json OR NEW.finished_at IS NOT OLD.finished_at)
BEGIN SELECT RAISE(ABORT,'workflow outcome is immutable'); END;
CREATE TRIGGER restricted_workflow_recipe_revocation AFTER UPDATE OF definition_json,status,deleted_at,workspace_id,author_crew_id ON pipelines
WHEN OLD.definition_json IS NOT NEW.definition_json OR OLD.status IS NOT NEW.status OR OLD.deleted_at IS NOT NEW.deleted_at OR OLD.workspace_id IS NOT NEW.workspace_id OR OLD.author_crew_id IS NOT NEW.author_crew_id
BEGIN
 UPDATE access_attempts SET revoked_at=COALESCE(revoked_at,strftime('%Y-%m-%dT%H:%M:%fZ','now')) WHERE id IN(SELECT origin_attempt_id FROM restricted_workflow_jobs WHERE pipeline_id=OLD.id);
END;
CREATE TRIGGER restricted_workflow_profile_revocation AFTER UPDATE OF restricted_execution_profile ON agents
WHEN OLD.restricted_execution_profile IS NOT NEW.restricted_execution_profile
BEGIN
 UPDATE access_attempts SET revoked_at=COALESCE(revoked_at,strftime('%Y-%m-%dT%H:%M:%fZ','now')) WHERE id IN(SELECT origin_attempt_id FROM restricted_workflow_jobs WHERE agent_id=OLD.id);
END;
CREATE TRIGGER restricted_workflow_page_spec_revocation AFTER UPDATE OF spec_json ON pages
WHEN OLD.spec_json IS NOT NEW.spec_json
BEGIN
 UPDATE access_attempts SET revoked_at=COALESCE(revoked_at,strftime('%Y-%m-%dT%H:%M:%fZ','now')) WHERE id IN(SELECT origin_attempt_id FROM restricted_workflow_jobs WHERE page_id=OLD.id);
END;
CREATE TRIGGER restricted_workflow_publication_revocation AFTER UPDATE OF version,published ON page_project_live
WHEN OLD.version IS NOT NEW.version OR OLD.published IS NOT NEW.published
BEGIN
 UPDATE access_attempts SET revoked_at=COALESCE(revoked_at,strftime('%Y-%m-%dT%H:%M:%fZ','now')) WHERE id IN(SELECT origin_attempt_id FROM restricted_workflow_jobs WHERE page_id=OLD.page_id);
END;
CREATE TRIGGER restricted_workflow_page_delete BEFORE DELETE ON pages
BEGIN
 UPDATE access_attempts SET revoked_at=COALESCE(revoked_at,strftime('%Y-%m-%dT%H:%M:%fZ','now')) WHERE id IN(SELECT origin_attempt_id FROM restricted_workflow_jobs WHERE page_id=OLD.id);
END;
CREATE TRIGGER restricted_workflow_publication_delete BEFORE DELETE ON page_project_live
BEGIN
 UPDATE access_attempts SET revoked_at=COALESCE(revoked_at,strftime('%Y-%m-%dT%H:%M:%fZ','now')) WHERE id IN(SELECT origin_attempt_id FROM restricted_workflow_jobs WHERE page_id=OLD.page_id);
END;
CREATE TRIGGER restricted_workflow_recipe_delete BEFORE DELETE ON pipelines
BEGIN
 UPDATE access_attempts SET revoked_at=COALESCE(revoked_at,strftime('%Y-%m-%dT%H:%M:%fZ','now')) WHERE id IN(SELECT origin_attempt_id FROM restricted_workflow_jobs WHERE pipeline_id=OLD.id);
END;
CREATE TRIGGER restricted_workflow_panel_revocation AFTER UPDATE OF owner_crew_id ON page_panels
WHEN OLD.owner_crew_id IS NOT NEW.owner_crew_id
BEGIN
 UPDATE access_attempts SET revoked_at=COALESCE(revoked_at,strftime('%Y-%m-%dT%H:%M:%fZ','now')) WHERE id IN(SELECT origin_attempt_id FROM restricted_workflow_jobs WHERE page_id=OLD.page_id);
END;
CREATE TRIGGER restricted_workflow_panel_delete BEFORE DELETE ON page_panels
BEGIN
 UPDATE access_attempts SET revoked_at=COALESCE(revoked_at,strftime('%Y-%m-%dT%H:%M:%fZ','now')) WHERE id IN(SELECT origin_attempt_id FROM restricted_workflow_jobs WHERE page_id=OLD.page_id);
END;
CREATE TRIGGER restricted_workflow_crew_audience_delete BEFORE DELETE ON crew_members
BEGIN
 UPDATE access_attempts SET revoked_at=COALESCE(revoked_at,strftime('%Y-%m-%dT%H:%M:%fZ','now')) WHERE id IN(SELECT j.origin_attempt_id FROM restricted_workflow_jobs j JOIN page_panels p ON p.page_id=j.page_id WHERE p.owner_crew_id=OLD.crew_id AND j.principal_id=OLD.user_id);
END;
