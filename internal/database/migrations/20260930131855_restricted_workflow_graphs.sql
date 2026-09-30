ALTER TABLE restricted_workflow_jobs ADD COLUMN graph_json TEXT NOT NULL DEFAULT '';
ALTER TABLE restricted_workflow_jobs ADD COLUMN graph_hash TEXT NOT NULL DEFAULT '';
ALTER TABLE restricted_workflow_jobs ADD COLUMN proofs_json TEXT NOT NULL DEFAULT '{}';
ALTER TABLE restricted_workflow_provider_policies ADD COLUMN expires_at TEXT NOT NULL DEFAULT '';
CREATE TRIGGER restricted_workflow_graph_immutable BEFORE UPDATE OF graph_json,graph_hash ON restricted_workflow_jobs
BEGIN SELECT RAISE(ABORT,'workflow graph is immutable'); END;
CREATE TRIGGER restricted_workflow_proofs_terminal BEFORE UPDATE OF proofs_json ON restricted_workflow_jobs
WHEN OLD.state IN ('completed','failed','canceled')
BEGIN SELECT RAISE(ABORT,'workflow proofs are immutable'); END;
CREATE TABLE restricted_workflow_recipe_bindings (
 job_id TEXT NOT NULL REFERENCES restricted_workflow_jobs(id) ON DELETE CASCADE,
 pipeline_id TEXT NOT NULL REFERENCES pipelines(id) ON DELETE CASCADE,
 recipe_hash TEXT NOT NULL,
 recipe_json TEXT NOT NULL,
 author_crew_id TEXT,
 PRIMARY KEY(job_id,pipeline_id)
);
CREATE TRIGGER restricted_workflow_recipe_binding_immutable BEFORE UPDATE ON restricted_workflow_recipe_bindings
BEGIN SELECT RAISE(ABORT,'workflow recipe binding is immutable'); END;
CREATE TRIGGER restricted_workflow_recipe_binding_delete BEFORE DELETE ON restricted_workflow_recipe_bindings
BEGIN UPDATE access_attempts SET revoked_at=COALESCE(revoked_at,strftime('%Y-%m-%dT%H:%M:%fZ','now')) WHERE id IN(SELECT origin_attempt_id FROM restricted_workflow_jobs WHERE id=OLD.job_id); END;
CREATE TRIGGER restricted_workflow_nested_recipe_update AFTER UPDATE OF definition_json,status,deleted_at,workspace_id,author_crew_id ON pipelines
WHEN OLD.definition_json IS NOT NEW.definition_json OR OLD.status IS NOT NEW.status OR OLD.deleted_at IS NOT NEW.deleted_at OR OLD.workspace_id IS NOT NEW.workspace_id OR OLD.author_crew_id IS NOT NEW.author_crew_id
BEGIN UPDATE access_attempts SET revoked_at=COALESCE(revoked_at,strftime('%Y-%m-%dT%H:%M:%fZ','now')) WHERE id IN(SELECT j.origin_attempt_id FROM restricted_workflow_jobs j JOIN restricted_workflow_recipe_bindings b ON b.job_id=j.id WHERE b.pipeline_id=OLD.id); END;
