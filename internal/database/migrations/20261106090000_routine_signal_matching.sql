-- Routine event waits: payload filter, correlation key, webhook signal delivery (#5045).
--
-- A wait(event) step stores its rendered event_filter and correlation_key at
-- arm time so a delivery can be matched in SQL/Go without re-rendering the
-- routine. Both are NULL for waits armed before this migration: NULL filter
-- accepts any signal (unchanged behaviour), NULL key is never correlated.
ALTER TABLE pipeline_signal_waits ADD COLUMN event_filter TEXT;
ALTER TABLE pipeline_signal_waits ADD COLUMN correlation_key TEXT;
CREATE INDEX IF NOT EXISTS idx_pipeline_signal_waits_correlation
    ON pipeline_signal_waits (workspace_id, event_type, correlation_key)
    WHERE status = 'pending' AND correlation_key IS NOT NULL;

-- A webhook may deliver a signal instead of starting a run. Both columns are
-- set together or not at all (enforced by the API); NULL = classic start-a-run.
ALTER TABLE pipeline_webhooks ADD COLUMN signal_event_type TEXT;
ALTER TABLE pipeline_webhooks ADD COLUMN signal_correlation_path TEXT;

-- One row per signal delivery attempt that reached verification, keyed by the
-- sender's delivery id so a redelivery answers with the original outcome and
-- never signals a second wait. It is also the operator-visible record of
-- deliveries that matched nothing.
CREATE TABLE IF NOT EXISTS pipeline_signal_deliveries (
    webhook_id   TEXT NOT NULL,
    delivery_id  TEXT NOT NULL,
    workspace_id TEXT NOT NULL,
    outcome      TEXT NOT NULL,           -- 'delivered' | 'ignored'
    reason       TEXT NOT NULL DEFAULT '',
    run_ids      TEXT NOT NULL DEFAULT '[]',
    received_at  TEXT NOT NULL,
    PRIMARY KEY (webhook_id, delivery_id)
);
CREATE INDEX IF NOT EXISTS idx_pipeline_signal_deliveries_received
    ON pipeline_signal_deliveries (received_at);
