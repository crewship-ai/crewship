-- Audit identity, intentionally not a live FK: deleting a credential must
-- not erase historical attribution. NULL means unknown; never guess an old
-- row's payer by matching a provider or subscription plan name.
ALTER TABLE cost_ledger ADD COLUMN credential_id TEXT;
CREATE INDEX idx_cost_credential_ts ON cost_ledger(workspace_id, credential_id, ts);
