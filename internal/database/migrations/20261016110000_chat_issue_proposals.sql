-- Proposal state belongs to the chat source; operation receipts reuse agent_operations.
CREATE TABLE chat_issue_proposals (
 id TEXT PRIMARY KEY,
 workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 chat_id TEXT NOT NULL REFERENCES chats(id) ON DELETE CASCADE,
 agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
 run_id TEXT NOT NULL,
 trigger_message_id TEXT NOT NULL,
 trigger_type TEXT NOT NULL,
 version INTEGER NOT NULL DEFAULT 1 CHECK(version BETWEEN 1 AND 9007199254740991),
 status TEXT NOT NULL DEFAULT 'draft' CHECK(status IN ('draft','accepted','discarded','expired')),
 title TEXT NOT NULL,
 items TEXT NOT NULL CHECK(json_valid(items)),
 expires_at TEXT NOT NULL,
 outcome TEXT CHECK(outcome IS NULL OR json_valid(outcome)),
 operation_id TEXT NOT NULL,
 event_id TEXT NOT NULL,
 created_at TEXT NOT NULL
);
CREATE INDEX chat_issue_proposals_chat ON chat_issue_proposals(chat_id,created_at,id);
CREATE TRIGGER chat_issue_proposals_origin_immutable BEFORE UPDATE OF workspace_id,chat_id,agent_id,run_id,trigger_message_id,trigger_type,operation_id,event_id,created_at ON chat_issue_proposals
BEGIN SELECT RAISE(ABORT,'proposal origin is immutable'); END;
CREATE TRIGGER chat_issue_proposals_terminal_immutable BEFORE UPDATE ON chat_issue_proposals WHEN OLD.status <> 'draft'
BEGIN SELECT RAISE(ABORT,'terminal proposal is immutable'); END;

-- This source-specific receipt is not a queue, grant, or execution authority.
CREATE TABLE chat_proposal_operations (
 id TEXT PRIMARY KEY,
 workspace_id TEXT NOT NULL,
 actor_type TEXT NOT NULL CHECK(actor_type IN ('agent','human')),
 actor_id TEXT NOT NULL,
 operation_key TEXT NOT NULL,
 request_hash TEXT NOT NULL,
 verb TEXT NOT NULL CHECK(verb IN ('create','edit','confirm','discard')),
 run_id TEXT NOT NULL DEFAULT '',
 proposal_id TEXT NOT NULL REFERENCES chat_issue_proposals(id) ON DELETE CASCADE,
 result TEXT NOT NULL CHECK(json_valid(result)),
 UNIQUE(workspace_id,actor_type,actor_id,operation_key)
);
CREATE TRIGGER chat_proposal_operations_immutable BEFORE UPDATE ON chat_proposal_operations
BEGIN SELECT RAISE(ABORT,'proposal receipt is immutable'); END;
