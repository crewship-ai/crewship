-- Existing rows remain explicitly unverified; do not manufacture old proofs.
ALTER TABLE instance_audit_logs ADD COLUMN chain_seq INTEGER;
ALTER TABLE instance_audit_logs ADD COLUMN prev_hash TEXT;
ALTER TABLE instance_audit_logs ADD COLUMN entry_hash TEXT;
CREATE UNIQUE INDEX idx_instance_audit_chain_seq ON instance_audit_logs(chain_seq);
