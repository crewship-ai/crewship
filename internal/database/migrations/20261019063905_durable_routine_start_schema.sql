-- Schema preparation only: legacy rows retain NULL identity/phase values.
-- Planned execution identity is separate from confirmed fired_run_id.
ALTER TABLE pending_runs ADD COLUMN run_id TEXT;
ALTER TABLE pending_runs ADD COLUMN idempotency_key TEXT;
ALTER TABLE pending_runs ADD COLUMN due_at TEXT;
ALTER TABLE pending_runs ADD COLUMN reason_code TEXT;
ALTER TABLE pipeline_runs ADD COLUMN start_phase TEXT;

-- A producer occurrence remains deduplicated across all pending lifecycle states.
CREATE UNIQUE INDEX idx_pending_runs_producer_key
ON pending_runs (workspace_id, idempotency_key)
WHERE idempotency_key IS NOT NULL;

CREATE INDEX idx_pending_runs_planned_run
ON pending_runs (workspace_id, run_id)
WHERE run_id IS NOT NULL;

-- Supports ordered, bounded startup inspection without scanning other states.
CREATE INDEX idx_pending_runs_start_recovery
ON pending_runs (status, id)
WHERE status = 'fired' AND COALESCE(fired_run_id, '') = '';
