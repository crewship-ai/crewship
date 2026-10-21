-- Preserve every existing row and MAC. NULL/NULL is the legacy v1 framing.
ALTER TABLE journal_entries ADD COLUMN chain_key_id TEXT;
ALTER TABLE journal_entries ADD COLUMN chain_mac_version INTEGER;
ALTER TABLE instance_audit_logs ADD COLUMN chain_key_id TEXT;
ALTER TABLE instance_audit_logs ADD COLUMN chain_mac_version INTEGER;
ALTER TABLE journal_chain_checkpoints ADD COLUMN chain_key_id TEXT;
ALTER TABLE journal_chain_checkpoints ADD COLUMN chain_mac_version INTEGER;
ALTER TABLE journal_replay_receipts ADD COLUMN chain_key_id TEXT;
ALTER TABLE journal_replay_receipts ADD COLUMN chain_mac_version INTEGER;
