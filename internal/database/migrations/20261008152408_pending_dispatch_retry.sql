-- Preserve the accepted occurrence time; retry eligibility is a separate clock.
ALTER TABLE pending_runs ADD COLUMN dispatch_attempts INTEGER NOT NULL DEFAULT 0;
ALTER TABLE pending_runs ADD COLUMN last_error TEXT NOT NULL DEFAULT '';
ALTER TABLE pending_runs ADD COLUMN next_attempt_at TEXT;

-- Once attempted, a start retains its own immutable payload. A new debounce
-- trigger may open a new window while the earlier start waits for capacity.
DROP INDEX idx_pending_runs_debounce;
CREATE UNIQUE INDEX idx_pending_runs_debounce ON pending_runs (pipeline_id, debounce_key)
WHERE status='pending' AND debounce_key IS NOT NULL AND dispatch_attempts=0;

-- No historical fired row is classified as failed: an empty run ID may mean
-- Executor.Run is still executing. Exact receipt lookup exposes the ambiguity.
