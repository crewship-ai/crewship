-- Source-row retry metadata; never a second answer ledger. Legacy rows remain
-- NULL and retain their existing canonical approval behavior.
ALTER TABLE approvals_queue ADD COLUMN answer_operation_key TEXT;
ALTER TABLE approvals_queue ADD COLUMN answer_operation_hash TEXT;
