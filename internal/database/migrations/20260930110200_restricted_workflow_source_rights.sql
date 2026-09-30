ALTER TABLE restricted_workflow_jobs ADD COLUMN additional_rights_json TEXT NOT NULL DEFAULT 'null';
ALTER TABLE restricted_workflow_jobs ADD COLUMN source_facet TEXT NOT NULL DEFAULT '' CHECK(source_facet IN ('','issue'));
CREATE TRIGGER restricted_workflow_source_identity_immutable BEFORE UPDATE OF additional_rights_json,source_facet ON restricted_workflow_jobs
BEGIN SELECT RAISE(ABORT,'workflow source authority is immutable'); END;
