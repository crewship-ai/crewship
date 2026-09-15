-- GitHub delivery IDs are unsigned. Atomically collapse identical verified bodies.
CREATE UNIQUE INDEX IF NOT EXISTS routine_receipts_github_content
ON routine_webhook_receipts(workspace_id, endpoint_id, body_sha256) WHERE profile = 'github';

ALTER TABLE pipeline_webhooks ADD COLUMN ingress_profile TEXT NOT NULL DEFAULT 'crewship' CHECK (ingress_profile IN ('crewship','github'));
