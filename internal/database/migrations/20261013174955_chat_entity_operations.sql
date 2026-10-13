-- Durable mutation results are separate from their 0..n chat references.
-- Actor scope prevents a guessed key from retrieving another actor's result.
CREATE TABLE agent_operations (
 id TEXT PRIMARY KEY,
 workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 operation_key TEXT NOT NULL CHECK(length(operation_key) BETWEEN 1 AND 200),
 request_hash TEXT NOT NULL,
 actor_type TEXT NOT NULL CHECK(actor_type IN ('agent','human','system')),
 actor_id TEXT NOT NULL,
 run_id TEXT NOT NULL DEFAULT '',
 human_authority TEXT NOT NULL DEFAULT '{}' CHECK(json_valid(human_authority)),
 provenance TEXT NOT NULL CHECK(provenance IN ('verified','legacy','human')),
 kind TEXT NOT NULL CHECK(kind = 'issue'),
 verb TEXT NOT NULL CHECK(verb IN ('created','updated','commented','shown','deleted')),
 entity_id TEXT NOT NULL DEFAULT '',
 entity_revision INTEGER NOT NULL DEFAULT 0,
 entity_aggregate_revision INTEGER NOT NULL DEFAULT 0,
 access_snapshot TEXT NOT NULL DEFAULT '{}' CHECK(json_valid(access_snapshot)),
 status TEXT NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','completed')),
 result TEXT NOT NULL DEFAULT '{}' CHECK(json_valid(result)),
 source_proposal_id TEXT NOT NULL DEFAULT '',
 source_proposal_version INTEGER NOT NULL DEFAULT 0,
 created_at TEXT NOT NULL,
 UNIQUE(workspace_id, actor_type, actor_id, operation_key)
);
CREATE INDEX agent_operations_entity ON agent_operations(workspace_id, kind, entity_id);
CREATE TABLE chat_entity_refs (
 chat_seq INTEGER PRIMARY KEY AUTOINCREMENT,
 event_id TEXT NOT NULL UNIQUE,
 chat_id TEXT NOT NULL REFERENCES chats(id) ON DELETE CASCADE,
 operation_id TEXT NOT NULL REFERENCES agent_operations(id) ON DELETE CASCADE,
 run_id TEXT NOT NULL,
 trigger_message_id TEXT NOT NULL,
 created_at TEXT NOT NULL,
 UNIQUE(chat_id, operation_id)
);
CREATE INDEX chat_entity_refs_scan ON chat_entity_refs(chat_id, chat_seq);
-- Deletion receipts fan out from an operation/entity to its original chats.
CREATE INDEX chat_entity_refs_operation ON chat_entity_refs(operation_id);
