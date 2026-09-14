-- Routine execution belongs to the pipeline engine, not the agent work queue.
CREATE TABLE routine_webhook_receipts (
 id TEXT PRIMARY KEY,
 workspace_id TEXT NOT NULL,
 endpoint_id TEXT NOT NULL,
 source_delivery_id TEXT NOT NULL,
 body_sha256 TEXT NOT NULL,
 run_id TEXT NOT NULL,
 UNIQUE(workspace_id, endpoint_id, source_delivery_id)
);

-- Preserve receipts from earlier development builds; redelivery must not
-- repeat a pipeline just because its bookkeeping owner changed.
INSERT INTO routine_webhook_receipts
 (id, workspace_id, endpoint_id, source_delivery_id, body_sha256, run_id)
SELECT d.id, d.workspace_id, d.endpoint_id, d.source_delivery_id, d.body_sha256, w.domain_id
FROM webhook_deliveries d JOIN work_items w ON w.id = d.work_id
WHERE d.endpoint_kind = 'routine' AND w.source = 'webhook'
 AND w.domain_kind = 'pipeline_run' AND w.domain_id <> '';

-- Old phantom work never had a dispatcher owner. Do not pretend it failed or
-- was cancelled: preserve its history and require inspection of the pipeline
-- run. Fence at generation 1 so the operator resolve API can settle it.
INSERT INTO work_events (work_id, seq, at, from_state, to_state, generation, reason)
SELECT w.id, COALESCE((SELECT MAX(e.seq) FROM work_events e WHERE e.work_id = w.id), 0) + 1,
 strftime('%Y-%m-%dT%H:%M:%fZ', 'now'), w.state, 'needs_reconciliation', 1,
 'routine receipt ownership migrated; inspect the pipeline run before resolving this legacy work item'
FROM work_items w WHERE w.source = 'webhook' AND w.domain_kind = 'pipeline_run'
 AND w.state = 'queued' AND w.generation = 0 AND w.attempts = 0
 AND NOT EXISTS (SELECT 1 FROM work_attempts a WHERE a.work_id = w.id);
UPDATE work_items SET state = 'needs_reconciliation', generation = 1,
 updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now'),
 state_reason = 'routine receipt ownership migrated; inspect the pipeline run before resolving this legacy work item'
WHERE source = 'webhook' AND domain_kind = 'pipeline_run'
 AND state = 'queued' AND generation = 0 AND attempts = 0
 AND NOT EXISTS (SELECT 1 FROM work_attempts a WHERE a.work_id = work_items.id);
