-- Agent proposals on Canvas Docs (schema 6, #5149). Schema 6 is schema 5 plus
-- the Canvas surface and keeps every capability schema 5 has, Propose
-- included (internal/documents/schemas.go). A propose mandate and a proposal
-- stay bound to the Document's own schema; only the closed schema CHECKs and
-- the propose-cursor trigger widen from 4,5 to 4,5,6 (3,4,5 to 3,4,5,6 for
-- mandates).
--
-- Both tables are rebuilt exactly as in 20261109090843: rows are copied before
-- any trigger exists, so the copy charges no storage twice; columns, FK
-- actions, indexes and triggers are preserved otherwise. Nothing references
-- either table.
CREATE TABLE document_iteration_intents_v2 (
 message_id TEXT PRIMARY KEY REFERENCES conversation_messages(id) ON DELETE CASCADE,
 workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 human_user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 chat_id TEXT NOT NULL REFERENCES chats(id) ON DELETE CASCADE,
 submission_id TEXT NOT NULL CHECK(length(submission_id) BETWEEN 1 AND 128),
 page_id TEXT NOT NULL,
 schema_version INTEGER NOT NULL CHECK(schema_version IN (3,4,5,6)),
 document_generation INTEGER NOT NULL CHECK(document_generation>=0),
 original_rev INTEGER NOT NULL CHECK(original_rev>=0),
 draft_rev INTEGER NOT NULL CHECK(draft_rev>=original_rev),
 pages_version INTEGER NOT NULL,
 policy_version INTEGER NOT NULL,
 member_id TEXT NOT NULL,
 member_revision INTEGER NOT NULL,
 chat_generation TEXT NOT NULL,
 chat_revision INTEGER NOT NULL,
 run_id TEXT UNIQUE REFERENCES artifact_run_principals(run_id) ON DELETE CASCADE,
 expires_at TEXT,
 revoked_at TEXT,
 -- NULL is the whole document; otherwise a JSON array of 1-16 block IDs.
 -- Only a propose mandate (schema 4, 5 or 6) may name blocks.
 scope_blocks TEXT CHECK(scope_blocks IS NULL OR (schema_version IN (4,5,6) AND length(scope_blocks) BETWEEN 2 AND 2048)),
 UNIQUE(workspace_id,human_user_id,chat_id,submission_id),
 CHECK((run_id IS NULL)=(expires_at IS NULL))
);
INSERT INTO document_iteration_intents_v2(message_id,workspace_id,human_user_id,chat_id,submission_id,page_id,schema_version,document_generation,original_rev,draft_rev,pages_version,policy_version,member_id,member_revision,chat_generation,chat_revision,run_id,expires_at,revoked_at,scope_blocks)
 SELECT message_id,workspace_id,human_user_id,chat_id,submission_id,page_id,schema_version,document_generation,original_rev,draft_rev,pages_version,policy_version,member_id,member_revision,chat_generation,chat_revision,run_id,expires_at,revoked_at,scope_blocks FROM document_iteration_intents;
DROP TABLE document_iteration_intents;
ALTER TABLE document_iteration_intents_v2 RENAME TO document_iteration_intents;
CREATE INDEX document_iteration_page ON document_iteration_intents(workspace_id,page_id);
CREATE INDEX document_iteration_chat ON document_iteration_intents(chat_id);
CREATE INDEX document_iteration_human ON document_iteration_intents(human_user_id);
CREATE TRIGGER document_iteration_identity_immutable BEFORE UPDATE OF message_id,workspace_id,human_user_id,chat_id,submission_id,page_id,schema_version,document_generation,original_rev,pages_version,policy_version,member_id,member_revision,chat_generation,chat_revision,scope_blocks ON document_iteration_intents
BEGIN SELECT RAISE(ABORT,'document iteration identity is immutable'); END;
CREATE TRIGGER document_iteration_binding_immutable BEFORE UPDATE OF run_id,expires_at ON document_iteration_intents
WHEN OLD.run_id IS NOT NULL AND (NEW.run_id IS NOT OLD.run_id OR NEW.expires_at IS NOT OLD.expires_at)
BEGIN SELECT RAISE(ABORT,'document iteration binding is immutable'); END;
CREATE TRIGGER document_iteration_cursor_monotonic BEFORE UPDATE OF draft_rev ON document_iteration_intents
WHEN NEW.draft_rev<OLD.draft_rev OR (OLD.run_id IS NULL AND NEW.draft_rev!=OLD.draft_rev) OR (NEW.schema_version IN (4,5,6) AND NEW.draft_rev!=OLD.draft_rev)
BEGIN SELECT RAISE(ABORT,'document iteration cursor cannot rewind'); END;

CREATE TABLE document_proposals_v2 (
 workspace_id TEXT NOT NULL, page_id TEXT NOT NULL,
 id TEXT NOT NULL CHECK(length(id) BETWEEN 1 AND 128),
 run_id TEXT NOT NULL CHECK(length(run_id) BETWEEN 1 AND 256),
 actor TEXT NOT NULL CHECK(length(actor) BETWEEN 1 AND 600),
 target TEXT NOT NULL CHECK(length(target) BETWEEN 1 AND 80),
 -- The target's enclosing block IDs (JSON array): their hashes change with it.
 ancestors TEXT NOT NULL CHECK(length(ancestors) BETWEEN 2 AND 4096),
 schema_version INTEGER NOT NULL CHECK(schema_version IN (4,5,6)),
 generation INTEGER NOT NULL CHECK(generation >= 0),
 base_rev INTEGER NOT NULL CHECK(base_rev >= 0),
 base_hash TEXT NOT NULL CHECK(length(base_hash) = 64),
 proposed_hash TEXT NOT NULL CHECK(length(proposed_hash) = 64),
 text TEXT NOT NULL CHECK(length(text) <= 100000),
 base_json TEXT NOT NULL CHECK(length(base_json) BETWEEN 2 AND 500000),
 proposed_json TEXT NOT NULL CHECK(length(proposed_json) BETWEEN 2 AND 500000),
 status TEXT NOT NULL DEFAULT 'open' CHECK(status IN ('open','accepted','rejected')),
 decided_by TEXT,
 decision_id TEXT,
 decided_at TEXT,
 accepted_rev INTEGER,
 created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
 PRIMARY KEY(workspace_id,page_id,id),
 CHECK((status = 'open') = (decided_by IS NULL)),
 CHECK((status = 'open') = (decision_id IS NULL)),
 CHECK((status = 'accepted') = (accepted_rev IS NOT NULL)),
 FOREIGN KEY(workspace_id,page_id) REFERENCES documents(workspace_id,page_id) ON DELETE CASCADE
);
INSERT INTO document_proposals_v2(workspace_id,page_id,id,run_id,actor,target,ancestors,schema_version,generation,base_rev,base_hash,proposed_hash,text,base_json,proposed_json,status,decided_by,decision_id,decided_at,accepted_rev,created_at)
 SELECT workspace_id,page_id,id,run_id,actor,target,ancestors,schema_version,generation,base_rev,base_hash,proposed_hash,text,base_json,proposed_json,status,decided_by,decision_id,decided_at,accepted_rev,created_at FROM document_proposals;
DROP TABLE document_proposals;
ALTER TABLE document_proposals_v2 RENAME TO document_proposals;
CREATE INDEX idx_document_proposals_status ON document_proposals(workspace_id,page_id,status);
-- A proposal's content and identity never change; only an open proposal is decided, once.
CREATE TRIGGER document_proposals_immutable BEFORE UPDATE OF workspace_id,page_id,id,run_id,actor,target,ancestors,schema_version,generation,base_rev,base_hash,proposed_hash,text,base_json,proposed_json,created_at ON document_proposals
BEGIN SELECT RAISE(ABORT,'document proposal is immutable'); END;
CREATE TRIGGER document_proposals_decided_once BEFORE UPDATE OF status,decided_by,decision_id,decided_at,accepted_rev ON document_proposals
WHEN OLD.status != 'open'
BEGIN SELECT RAISE(ABORT,'document proposal is already decided'); END;
-- Proposals count against the aggregate artifact storage budget; the
-- expression is storageLaterSourcesSQL's in internal/artifacts/storage_source_sql.go.
CREATE TRIGGER document_proposals_storage_insert AFTER INSERT ON document_proposals BEGIN INSERT INTO artifact_storage_charge VALUES('i',NULL,NEW.workspace_id,0,coalesce(length(CAST(NEW.id AS BLOB)),0)+coalesce(length(CAST(NEW.run_id AS BLOB)),0)+coalesce(length(CAST(NEW.actor AS BLOB)),0)+coalesce(length(CAST(NEW.text AS BLOB)),0)+coalesce(length(CAST(NEW.base_json AS BLOB)),0)+coalesce(length(CAST(NEW.proposed_json AS BLOB)),0)+coalesce(length(CAST(NEW.decided_by AS BLOB)),0)+coalesce(length(CAST(NEW.decision_id AS BLOB)),0)+512); END;
CREATE TRIGGER document_proposals_storage_delete AFTER DELETE ON document_proposals BEGIN INSERT INTO artifact_storage_charge VALUES('d',OLD.workspace_id,NULL,coalesce(length(CAST(OLD.id AS BLOB)),0)+coalesce(length(CAST(OLD.run_id AS BLOB)),0)+coalesce(length(CAST(OLD.actor AS BLOB)),0)+coalesce(length(CAST(OLD.text AS BLOB)),0)+coalesce(length(CAST(OLD.base_json AS BLOB)),0)+coalesce(length(CAST(OLD.proposed_json AS BLOB)),0)+coalesce(length(CAST(OLD.decided_by AS BLOB)),0)+coalesce(length(CAST(OLD.decision_id AS BLOB)),0)+512,0); END;
CREATE TRIGGER document_proposals_storage_update AFTER UPDATE ON document_proposals BEGIN INSERT INTO artifact_storage_charge VALUES('u',OLD.workspace_id,NEW.workspace_id,coalesce(length(CAST(OLD.id AS BLOB)),0)+coalesce(length(CAST(OLD.run_id AS BLOB)),0)+coalesce(length(CAST(OLD.actor AS BLOB)),0)+coalesce(length(CAST(OLD.text AS BLOB)),0)+coalesce(length(CAST(OLD.base_json AS BLOB)),0)+coalesce(length(CAST(OLD.proposed_json AS BLOB)),0)+coalesce(length(CAST(OLD.decided_by AS BLOB)),0)+coalesce(length(CAST(OLD.decision_id AS BLOB)),0)+512,coalesce(length(CAST(NEW.id AS BLOB)),0)+coalesce(length(CAST(NEW.run_id AS BLOB)),0)+coalesce(length(CAST(NEW.actor AS BLOB)),0)+coalesce(length(CAST(NEW.text AS BLOB)),0)+coalesce(length(CAST(NEW.base_json AS BLOB)),0)+coalesce(length(CAST(NEW.proposed_json AS BLOB)),0)+coalesce(length(CAST(NEW.decided_by AS BLOB)),0)+coalesce(length(CAST(NEW.decision_id AS BLOB)),0)+512); END;
