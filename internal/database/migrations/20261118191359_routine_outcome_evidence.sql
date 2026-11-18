-- Pre-upgrade runs keep the legacy outcome derivation when they resume.
ALTER TABLE pipeline_runs ADD COLUMN outcome_evidence_required INTEGER NOT NULL DEFAULT 0
    CHECK (outcome_evidence_required IN (0,1));
-- Record accepted outcomes alongside outputs at the step boundary.
ALTER TABLE pipeline_run_step_outputs ADD COLUMN outcome_evidence_json TEXT;
