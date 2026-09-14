-- Routine webhook receipts get an age, a retention window and enough identity
-- to be read back without inventing a work item.
--
-- The contract (docs/prd/WEBHOOKS-AGENT-PARALLELISM-IMPLEMENTATION-1-0.md §6)
-- keeps dedup keys and receipts for at least 30 days from acceptance. The
-- table shipped without a timestamp, so nothing could expire it and nothing
-- could list it in order. Once a receipt expires, a redelivery of the same
-- identifier is new work again — the contract promises no replay suppression
-- past the window, and the sweeper that removes expired rows is that promise.
ALTER TABLE routine_webhook_receipts ADD COLUMN received_at TEXT NOT NULL DEFAULT '';
ALTER TABLE routine_webhook_receipts ADD COLUMN dedup_expires_at TEXT NOT NULL DEFAULT '';
ALTER TABLE routine_webhook_receipts ADD COLUMN body_bytes INTEGER NOT NULL DEFAULT 0;
ALTER TABLE routine_webhook_receipts ADD COLUMN profile TEXT NOT NULL DEFAULT '';

-- Rows written before this migration have no known age. They are dated from
-- the migration itself and get the full window from here, so a receipt whose
-- real age is unknown is never expired early — the safe error for a dedup key
-- is to live too long, not to vanish and let a delivery run twice.
UPDATE routine_webhook_receipts
   SET received_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
 WHERE received_at = '';
UPDATE routine_webhook_receipts
   SET dedup_expires_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now', '+30 days')
 WHERE dedup_expires_at = '';

CREATE INDEX idx_routine_webhook_receipts_list
    ON routine_webhook_receipts(workspace_id, received_at, id);
CREATE INDEX idx_routine_webhook_receipts_expiry
    ON routine_webhook_receipts(dedup_expires_at);
