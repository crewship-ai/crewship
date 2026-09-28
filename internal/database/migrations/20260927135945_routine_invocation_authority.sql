-- Separate trusted admission provenance from user-editable run metadata.
-- Existing work retains the membership floor until its source is known.
ALTER TABLE pending_runs ADD COLUMN invocation_authority TEXT NOT NULL DEFAULT '';
ALTER TABLE pipeline_runs ADD COLUMN invocation_authority TEXT NOT NULL DEFAULT '';
