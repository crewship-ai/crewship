-- Durable projection receipts are independent of journal retention. A lost
-- source acknowledgement must not append a second row after its first row was
-- pruned, nor redeliver the projection to commit observers. No cascading FK or
-- automatic expiry: receipts remain authoritative after visible rows expire.
CREATE TABLE journal_replay_receipts (
    id TEXT PRIMARY KEY,
    fingerprint TEXT NOT NULL,
    workspace_id TEXT NOT NULL,
    agent_id TEXT NOT NULL,
    trace_id TEXT NOT NULL,
    entry_type TEXT NOT NULL,
    terminal_exit_code INTEGER
);
