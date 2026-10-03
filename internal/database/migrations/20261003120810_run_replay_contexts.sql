-- Host-side scrub context for admitted runs. No workload-facing access path.
CREATE TABLE run_replay_contexts (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    secret_values_enc TEXT NOT NULL,
    created_at TEXT NOT NULL
);
CREATE INDEX idx_run_replay_contexts_workspace ON run_replay_contexts(workspace_id);

-- Receipts survive journal compaction; deleting a workspace removes its scope.
-- No journal-entry FK: its deletion must not authorize replaying an effect.
CREATE TABLE journal_replay_receipts (
 entry_id TEXT PRIMARY KEY,
 workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 run_id TEXT NOT NULL,
 content_hash TEXT NOT NULL
);
CREATE INDEX idx_journal_replay_receipts_run ON journal_replay_receipts(workspace_id, run_id);
