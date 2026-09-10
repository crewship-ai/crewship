-- Frozen at gate creation; empty preserves legacy binary approvals and output.
ALTER TABLE pipeline_waitpoints ADD COLUMN decision_form_json TEXT NOT NULL DEFAULT '';
