-- Replay metadata belongs to the existing authoritative answer row. A source
-- resolved through Inbox has no operation key and remains the same winner.
ALTER TABLE escalations ADD COLUMN answer_operation_key TEXT;
ALTER TABLE escalations ADD COLUMN answer_operation_hash TEXT;

-- Preserve resolved_by as the existing user/system discriminator.
ALTER TABLE escalations ADD COLUMN resolved_by_user_id TEXT REFERENCES users(id) ON DELETE SET NULL;
CREATE INDEX idx_escalations_resolved_by_user_id ON escalations(resolved_by_user_id) WHERE resolved_by_user_id IS NOT NULL;
