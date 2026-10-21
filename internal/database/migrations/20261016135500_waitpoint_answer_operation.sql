-- The existing source owns the answer and retry receipt; no second ledger.
ALTER TABLE pipeline_waitpoints ADD COLUMN answer_operation_key TEXT;
ALTER TABLE pipeline_waitpoints ADD COLUMN answer_operation_hash TEXT;
