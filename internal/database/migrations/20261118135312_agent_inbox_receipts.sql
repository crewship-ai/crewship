-- Read receipts are evidence over canonical work, never a dispatch queue.
CREATE TABLE agent_inbox_receipts (
 workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
 source_kind TEXT NOT NULL CHECK(source_kind IN ('channel_message','chat_message','assignment')),
 source_id TEXT NOT NULL,
 run_id TEXT NOT NULL,
 read_at TEXT NOT NULL,
 source_sha256 TEXT NOT NULL,
 source_revision TEXT NOT NULL DEFAULT '',
 PRIMARY KEY(workspace_id,agent_id,source_kind,source_id)
);
-- Runtime evidence is excluded from portable workspace restore. Hard-deleting
-- its local parent must remove it before a replacement can reuse parent IDs.
-- The composite receipt PK starts with workspace_id, not agent_id.
CREATE INDEX idx_agent_inbox_receipts_agent ON agent_inbox_receipts(agent_id);
ALTER TABLE workspace_conversation_agent_jobs ADD COLUMN read_authority_json TEXT;
ALTER TABLE workspace_conversation_agent_jobs ADD COLUMN read_source_sha256 TEXT;
-- A host-only commitment to private canonical source bytes at run admission.
-- No body, queue state or public feed metadata lives here.
CREATE TABLE agent_inbox_run_sources (
 run_id TEXT PRIMARY KEY,
 workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
 source_kind TEXT NOT NULL CHECK(source_kind='chat_message'),
 source_id TEXT NOT NULL,
 source_sha256 TEXT NOT NULL,
 created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

-- Both parent foreign-key checks must use a leading child index instead of
-- scanning all retained private run commitments while holding the write lock.
CREATE INDEX idx_agent_inbox_run_sources_workspace ON agent_inbox_run_sources(workspace_id);
CREATE INDEX idx_agent_inbox_run_sources_agent ON agent_inbox_run_sources(agent_id);

INSERT INTO feature_flags (id,key,description,enabled,percentage)
VALUES ('ffl_agent_human_inbox','agent_human_inbox','Human inbox UI and source-bound agent read receipts',0,0);

-- Work retention purges attempts when their durable work expires. Remove
-- their optional Inbox evidence in the same purge transaction.
CREATE INDEX idx_agent_inbox_receipts_run ON agent_inbox_receipts(run_id);
CREATE TRIGGER purge_agent_inbox_evidence_on_attempt_delete AFTER DELETE ON work_attempts
BEGIN
 DELETE FROM agent_inbox_receipts WHERE run_id=OLD.run_id;
 DELETE FROM agent_inbox_run_sources WHERE run_id=OLD.run_id;
END;
