-- Retain source classification when immutable journal projections outlive a
-- verified private TEXT question. A purge clears source content and retry data;
-- this nullable tombstone does not introduce another decision or grant store.
ALTER TABLE escalations ADD COLUMN deleted_at TEXT;
