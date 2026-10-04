-- Persist the original event-wait deadline, including already parked arms.
ALTER TABLE pipeline_signal_waits ADD COLUMN timeout_at TEXT;
-- Prefer the executed snapshot, then an immutable pinned version, then HEAD.
-- This closes the upgrade/startup delivery window before recovery can re-enter
-- each step. Missing/deleted or invalid legacy recipes use the historical 1h
-- default; boot recovery still interrupts runs whose state is not resumable.
WITH recipes AS (
    SELECT w.id, w.created_at, w.step_id,
           COALESCE(NULLIF(r.executed_definition_json, ''), v.definition_json,
                    p.definition_json, '{"steps":[]}') AS recipe
    FROM pipeline_signal_waits w
    LEFT JOIN pipeline_runs r ON r.id = w.run_id
    LEFT JOIN pipelines p ON p.id = r.pipeline_id
    LEFT JOIN pipeline_versions v ON v.pipeline_id = r.pipeline_id
                                 AND v.version = r.pipeline_version
), deadlines AS (
    SELECT id, created_at,
           COALESCE((SELECT CASE
                         WHEN CAST(json_extract(CASE WHEN step.type = 'object' THEN step.value ELSE '{}' END, '$.timeout_seconds') AS INTEGER) > 0
                         THEN CAST(json_extract(CASE WHEN step.type = 'object' THEN step.value ELSE '{}' END, '$.timeout_seconds') AS INTEGER)
                         ELSE 3600 END
                     FROM json_each(CASE WHEN json_valid(recipe) THEN recipe
                                         ELSE '{"steps":[]}' END, '$.steps') step
                     WHERE json_extract(CASE WHEN step.type = 'object' THEN step.value ELSE '{}' END, '$.id') = recipes.step_id
                     LIMIT 1), 3600) AS timeout_seconds
    FROM recipes
)
UPDATE pipeline_signal_waits
SET timeout_at = (SELECT strftime('%Y-%m-%dT%H:%M:%f000000Z', created_at,
                                 printf('+%d seconds', timeout_seconds))
                  FROM deadlines WHERE deadlines.id = pipeline_signal_waits.id);

CREATE INDEX idx_pipeline_signal_waits_deadlines
    ON pipeline_signal_waits (timeout_at, run_id) WHERE status = 'pending';

-- Cancel unresolved subscriptions in the same transaction as terminalizing a
-- run. A cancelled run must never accept a late signal or become a sweep job.
CREATE TRIGGER pipeline_signal_waits_run_terminal
AFTER UPDATE OF status ON pipeline_runs
WHEN NEW.status IN ('completed','failed','cancelled','interrupted','dry_run')
BEGIN
    UPDATE pipeline_signal_waits SET status = 'cancelled'
    WHERE run_id = NEW.id AND status IN ('pending','delivered');
END;
UPDATE pipeline_signal_waits SET status = 'cancelled'
WHERE status IN ('pending','delivered') AND run_id IN (
    SELECT id FROM pipeline_runs
    WHERE status IN ('completed','failed','cancelled','interrupted','dry_run')
);
-- Cover an arm racing just after the run's terminal transition as well.
CREATE TRIGGER pipeline_signal_waits_terminal_arm
AFTER INSERT ON pipeline_signal_waits
WHEN EXISTS (SELECT 1 FROM pipeline_runs r WHERE r.id = NEW.run_id
    AND r.status IN ('completed','failed','cancelled','interrupted','dry_run'))
BEGIN
    UPDATE pipeline_signal_waits SET status = 'cancelled' WHERE id = NEW.id;
END;
