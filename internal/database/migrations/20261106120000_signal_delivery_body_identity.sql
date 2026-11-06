-- A signal delivery is identified by what the signature covers: the webhook
-- and the sha256 of the raw body. The sender-supplied delivery id is unsigned,
-- so it only detects a conflicting reuse. complete = 0 marks a delivery whose
-- wait claiming failed part way; a retry of the same body finishes the
-- remainder instead of being reported as a duplicate.
ALTER TABLE pipeline_signal_deliveries ADD COLUMN body_sha256 TEXT NOT NULL DEFAULT '';
ALTER TABLE pipeline_signal_deliveries ADD COLUMN complete INTEGER NOT NULL DEFAULT 1;
CREATE UNIQUE INDEX IF NOT EXISTS idx_pipeline_signal_deliveries_body
    ON pipeline_signal_deliveries (webhook_id, body_sha256) WHERE body_sha256 <> '';
