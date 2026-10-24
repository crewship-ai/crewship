-- Default-off node genesis correlation, never a source/writer capability.
-- Retained outside portable content; no Page/workspace cascade may erase it.
CREATE TABLE mooring_origin_node_anchors (
 page_id TEXT PRIMARY KEY CHECK(typeof(page_id)='text' AND length(page_id) BETWEEN 1 AND 200 AND instr(page_id,char(0))=0),
 workspace_id TEXT NOT NULL CHECK(typeof(workspace_id)='text' AND length(workspace_id) BETWEEN 1 AND 200 AND instr(workspace_id,char(0))=0),
 application_id TEXT NOT NULL CHECK(application_id='wordcraft'),
 lineage_id TEXT NOT NULL UNIQUE CHECK(typeof(lineage_id)='text' AND length(lineage_id) BETWEEN 1 AND 200 AND instr(lineage_id,char(0))=0),
 scope_hash TEXT NOT NULL UNIQUE CHECK(typeof(scope_hash)='text' AND length(scope_hash)=64 AND scope_hash NOT GLOB '*[^0-9a-f]*'),
 journal_id TEXT NOT NULL UNIQUE CHECK(typeof(journal_id)='text' AND length(journal_id)=64 AND journal_id NOT GLOB '*[^0-9a-f]*'),
 checkpoint_revision TEXT NOT NULL CHECK(checkpoint_revision='0'),
 checkpoint_digest TEXT NOT NULL CHECK(typeof(checkpoint_digest)='text' AND length(checkpoint_digest)=71 AND substr(checkpoint_digest,1,7)='sha256:' AND substr(checkpoint_digest,8) NOT GLOB '*[^0-9a-f]*'),
 creator_generation TEXT NOT NULL CHECK(typeof(creator_generation)='text' AND length(creator_generation) BETWEEN 1 AND 20 AND creator_generation NOT GLOB '*[^0-9]*' AND substr(creator_generation,1,1)<>'0' AND (length(creator_generation)<20 OR creator_generation<='18446744073709551615')),
 original_runtime_ref TEXT NOT NULL CHECK(typeof(original_runtime_ref)='text' AND length(original_runtime_ref) BETWEEN 1 AND 200 AND instr(original_runtime_ref,char(0))=0),
 original_owner_token_hash BLOB NOT NULL CHECK(typeof(original_owner_token_hash)='blob' AND length(original_owner_token_hash)=32),
 closed INTEGER NOT NULL DEFAULT 0 CHECK(typeof(closed)='integer' AND closed IN (0,1))
);
CREATE INDEX mooring_origin_node_anchors_workspace ON mooring_origin_node_anchors(workspace_id);
CREATE TRIGGER mooring_origin_node_anchor_insert BEFORE INSERT ON mooring_origin_node_anchors
 WHEN NEW.closed<>0 OR EXISTS(SELECT 1 FROM mooring_origin_node_anchors WHERE page_id=NEW.page_id OR lineage_id=NEW.lineage_id OR scope_hash=NEW.scope_hash OR journal_id=NEW.journal_id)
 OR NOT EXISTS(SELECT 1 FROM mooring_collab_documents d JOIN mooring_collab_checkpoints c ON c.page_id=d.page_id
 WHERE d.page_id=NEW.page_id AND d.workspace_id=NEW.workspace_id AND d.application_id=NEW.application_id AND d.origin_lineage_id=NEW.lineage_id
 AND d.revision='0' AND c.revision='0' AND c.workspace_id=NEW.workspace_id AND c.checkpoint_generation=NEW.creator_generation)
 BEGIN SELECT RAISE(ABORT,'node origin genesis does not match fresh content'); END;
CREATE TRIGGER mooring_origin_node_anchor_update BEFORE UPDATE ON mooring_origin_node_anchors
 WHEN NEW.page_id IS NOT OLD.page_id OR NEW.workspace_id IS NOT OLD.workspace_id OR NEW.application_id IS NOT OLD.application_id OR NEW.lineage_id IS NOT OLD.lineage_id OR NEW.scope_hash IS NOT OLD.scope_hash OR NEW.journal_id IS NOT OLD.journal_id OR NEW.checkpoint_revision IS NOT OLD.checkpoint_revision OR NEW.checkpoint_digest IS NOT OLD.checkpoint_digest OR NEW.creator_generation IS NOT OLD.creator_generation OR NEW.original_runtime_ref IS NOT OLD.original_runtime_ref OR NEW.original_owner_token_hash IS NOT OLD.original_owner_token_hash OR NEW.closed<OLD.closed
 BEGIN SELECT RAISE(ABORT,'node origin anchor is immutable'); END;
CREATE TRIGGER mooring_origin_node_anchor_delete BEFORE DELETE ON mooring_origin_node_anchors
 BEGIN SELECT RAISE(ABORT,'node origin anchor is retained'); END;
CREATE TRIGGER mooring_origin_node_content_retire AFTER DELETE ON mooring_collab_documents
 BEGIN UPDATE mooring_origin_node_anchors SET closed=1 WHERE page_id=OLD.page_id; END;
