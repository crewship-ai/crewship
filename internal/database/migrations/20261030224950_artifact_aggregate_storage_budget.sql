-- Instance-local capacity accounting. Payloads, not these derived counters or
-- live upload reservations, travel in backups. No history is removed here.
CREATE TABLE artifact_storage_global (
 id INTEGER PRIMARY KEY CHECK(id=1),
 used_bytes INTEGER NOT NULL DEFAULT 0 CHECK(used_bytes>=0),
 reserved_bytes INTEGER NOT NULL DEFAULT 0 CHECK(reserved_bytes>=0),
 max_bytes INTEGER NOT NULL DEFAULT 68719476736 CHECK(max_bytes>0),
 workspace_max_bytes INTEGER NOT NULL DEFAULT 34359738368 CHECK(workspace_max_bytes>0),
 min_free_bytes INTEGER NOT NULL DEFAULT 2147483648 CHECK(min_free_bytes>=0)
);
INSERT INTO artifact_storage_global(id) VALUES(1);
CREATE TABLE artifact_storage_workspaces (
 workspace_id TEXT PRIMARY KEY REFERENCES workspaces(id) ON DELETE CASCADE,
 used_bytes INTEGER NOT NULL DEFAULT 0 CHECK(used_bytes>=0),
 reserved_bytes INTEGER NOT NULL DEFAULT 0 CHECK(reserved_bytes>=0),
 max_bytes INTEGER NOT NULL CHECK(max_bytes>0)
);
CREATE TABLE artifact_storage_reservations (
 id TEXT PRIMARY KEY,
 workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 page_id TEXT REFERENCES pages(id) ON DELETE CASCADE,
 bytes INTEGER NOT NULL CHECK(bytes>0),
 created_at TEXT NOT NULL,
 expires_at TEXT NOT NULL
);
CREATE INDEX artifact_storage_reservations_workspace ON artifact_storage_reservations(workspace_id);
CREATE INDEX artifact_storage_reservations_expiry ON artifact_storage_reservations(expires_at);
CREATE INDEX artifact_storage_reservations_page ON artifact_storage_reservations(page_id);
CREATE TRIGGER artifact_storage_reservation_scope BEFORE INSERT ON artifact_storage_reservations
WHEN NEW.page_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM pages WHERE id=NEW.page_id AND workspace_id=NEW.workspace_id)
BEGIN SELECT RAISE(ABORT,'artifact storage reservation scope mismatch'); END;

-- Page-scoped children of older editors have no workspace column. Keep a
-- derived scope until AFTER Page cascades finish, so deleting a parent still
-- decrements the original workspace and global counters exactly once.
CREATE TABLE artifact_storage_pages (
 page_id TEXT PRIMARY KEY,
 workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE
);
CREATE INDEX artifact_storage_pages_workspace ON artifact_storage_pages(workspace_id);
INSERT INTO artifact_storage_pages(page_id,workspace_id) SELECT id,workspace_id FROM pages;
CREATE TRIGGER pages_storage_scope_insert AFTER INSERT ON pages
BEGIN INSERT INTO artifact_storage_pages(page_id,workspace_id) VALUES(NEW.id,NEW.workspace_id); END;
CREATE TRIGGER pages_storage_scope_update AFTER UPDATE OF workspace_id ON pages
BEGIN UPDATE artifact_storage_pages SET workspace_id=NEW.workspace_id WHERE page_id=NEW.id; END;
CREATE TRIGGER pages_storage_scope_delete AFTER DELETE ON pages
BEGIN DELETE FROM artifact_storage_pages WHERE page_id=OLD.id; END;

-- Seed from existing durable data before installing admission checks. Existing
-- over-budget data remains readable; only subsequent growth is refused.
CREATE TEMP TABLE artifact_storage_seed AS SELECT workspace_id,SUM(bytes) AS bytes FROM (SELECT r.workspace_id AS workspace_id,coalesce(length(CAST(r.content AS BLOB)),0)+128 AS bytes FROM artifact_version_blobs r UNION ALL SELECT r.workspace_id AS workspace_id,coalesce(length(CAST(r.manifest_json AS BLOB)),0)+coalesce(length(CAST(r.message AS BLOB)),0)+coalesce(length(CAST(r.actor AS BLOB)),0)+coalesce(length(CAST(r.created_at AS BLOB)),0)+128 AS bytes FROM artifact_versions r UNION ALL SELECT r.workspace_id AS workspace_id,coalesce(length(CAST(r.content AS BLOB)),0)+coalesce(length(CAST(r.summary AS BLOB)),0)+coalesce(length(CAST(r.author_actor_id AS BLOB)),0)+coalesce(length(CAST(r.request_id AS BLOB)),0)+coalesce(length(CAST(r.id AS BLOB)),0)+coalesce(length(CAST(r.engine_pin AS BLOB)),0)+coalesce(length(CAST(r.content_hash AS BLOB)),0)+coalesce(length(CAST(r.created_at AS BLOB)),0)+512 AS bytes FROM craft_native_proposals r UNION ALL SELECT r.workspace_id AS workspace_id,coalesce(length(CAST(r.state AS BLOB)),0)+coalesce(length(CAST(r.updated_by AS BLOB)),0)+coalesce(length(CAST(r.updated_at AS BLOB)),0)+128 AS bytes FROM artifact_design_documents r UNION ALL SELECT r.workspace_id AS workspace_id,coalesce(length(CAST(r.name AS BLOB)),0)+128 AS bytes FROM artifact_data_collections r UNION ALL SELECT r.workspace_id AS workspace_id,coalesce(length(CAST(r.value_json AS BLOB)),0)+coalesce(length(CAST(r.id AS BLOB)),0)+coalesce(length(CAST(r.collection AS BLOB)),0)+coalesce(length(CAST(r.updated_by AS BLOB)),0)+coalesce(length(CAST(r.updated_at AS BLOB)),0)+128 AS bytes FROM artifact_data_documents r UNION ALL SELECT r.workspace_id AS workspace_id,coalesce(length(CAST(r.snapshot AS BLOB)),0)+coalesce(length(CAST(r.projection AS BLOB)),0)+coalesce(length(CAST(r.writer AS BLOB)),0)+coalesce(length(CAST(r.owner AS BLOB)),0)+256 AS bytes FROM documents r UNION ALL SELECT r.workspace_id AS workspace_id,coalesce(length(CAST(r.payload AS BLOB)),0)+128 AS bytes FROM document_updates r UNION ALL SELECT r.workspace_id AS workspace_id,coalesce(length(CAST(r.digest AS BLOB)),0)+coalesce(length(CAST(r.fingerprint AS BLOB)),0)+coalesce(length(CAST(r.actor AS BLOB)),0)+coalesce(length(CAST(r.kind AS BLOB)),0)+coalesce(length(CAST(r.created_at AS BLOB)),0)+256 AS bytes FROM document_receipts r UNION ALL SELECT r.workspace_id AS workspace_id,coalesce(length(CAST(r.snapshot AS BLOB)),0)+coalesce(length(CAST(r.projection AS BLOB)),0)+coalesce(length(CAST(r.actor AS BLOB)),0)+coalesce(length(CAST(r.created_at AS BLOB)),0)+256 AS bytes FROM document_checkpoints r UNION ALL SELECT r.workspace_id AS workspace_id,coalesce(length(CAST(r.canonical_json AS BLOB)),0)+coalesce(length(CAST(r.content_hash AS BLOB)),0)+256 AS bytes FROM slides_heads r UNION ALL SELECT r.workspace_id AS workspace_id,coalesce(length(CAST(r.canonical_json AS BLOB)),0)+coalesce(length(CAST(r.content_hash AS BLOB)),0)+coalesce(length(CAST(r.actor AS BLOB)),0)+coalesce(length(CAST(r.kind AS BLOB)),0)+coalesce(length(CAST(r.mutation_id AS BLOB)),0)+coalesce(length(CAST(r.created_at AS BLOB)),0)+256 AS bytes FROM slides_revisions r UNION ALL SELECT r.workspace_id AS workspace_id,coalesce(length(CAST(r.request_hash AS BLOB)),0)+coalesce(length(CAST(r.actor AS BLOB)),0)+coalesce(length(CAST(r.kind AS BLOB)),0)+coalesce(length(CAST(r.content_hash AS BLOB)),0)+coalesce(length(CAST(r.mutation_id AS BLOB)),0)+256 AS bytes FROM slides_receipts r UNION ALL SELECT r.workspace_id AS workspace_id,coalesce(length(CAST(r.object_id AS BLOB)),0)+coalesce(length(CAST(r.object_kind AS BLOB)),0)+128 AS bytes FROM slides_object_ids r UNION ALL SELECT r.workspace_id AS workspace_id,coalesce(length(CAST(r.snapshot AS BLOB)),0)+256 AS bytes FROM gridcraft_heads r UNION ALL SELECT r.workspace_id AS workspace_id,coalesce(length(CAST(r.receipt AS BLOB)),0)+coalesce(length(CAST(r.authority_id AS BLOB)),0)+coalesce(length(CAST(r.mutation_id AS BLOB)),0)+coalesce(length(CAST(r.payload_hash AS BLOB)),0)+coalesce(length(CAST(r.state AS BLOB)),0)+256 AS bytes FROM gridcraft_operations r UNION ALL SELECT coalesce((SELECT workspace_id FROM artifact_storage_pages WHERE page_id=r.page_id),(SELECT workspace_id FROM pages WHERE id=r.page_id)) AS workspace_id,coalesce(length(CAST(r.snapshot AS BLOB)),0)+128 AS bytes FROM gridcraft_revisions r UNION ALL SELECT coalesce((SELECT workspace_id FROM artifact_storage_pages WHERE page_id=r.page_id),(SELECT workspace_id FROM pages WHERE id=r.page_id)) AS workspace_id,coalesce(length(CAST(r.receipt AS BLOB)),0)+coalesce(length(CAST(r.command AS BLOB)),0)+coalesce(length(CAST(r.writer AS BLOB)),0)+coalesce(length(CAST(r.authority_id AS BLOB)),0)+coalesce(length(CAST(r.mutation_id AS BLOB)),0)+coalesce(length(CAST(r.mandate_id AS BLOB)),0)+coalesce(length(CAST(r.result_hash AS BLOB)),0)+256 AS bytes FROM gridcraft_audit r UNION ALL SELECT coalesce((SELECT workspace_id FROM artifact_storage_pages WHERE page_id=r.page_id),(SELECT workspace_id FROM pages WHERE id=r.page_id)) AS workspace_id,coalesce(length(CAST(r.state AS BLOB)),0)+128 AS bytes FROM gridcraft_outbox r UNION ALL SELECT r.workspace_id AS workspace_id,coalesce(length(CAST(r.snapshot AS BLOB)),0)+coalesce(length(CAST(r.app_id AS BLOB)),0)+256 AS bytes FROM mooring_documents r UNION ALL SELECT coalesce((SELECT workspace_id FROM artifact_storage_pages WHERE page_id=r.page_id),(SELECT workspace_id FROM pages WHERE id=r.page_id)) AS workspace_id,coalesce(length(CAST(r.receipt AS BLOB)),0)+coalesce(length(CAST(r.snapshot AS BLOB)),0)+coalesce(length(CAST(r.fingerprint AS BLOB)),0)+coalesce(length(CAST(r.authority_id AS BLOB)),0)+coalesce(length(CAST(r.mutation_id AS BLOB)),0)+256 AS bytes FROM mooring_receipts r UNION ALL SELECT r.workspace_id AS workspace_id,coalesce(length(CAST(r.checkpoint_base64 AS BLOB)),0)+coalesce(length(CAST(r.revision AS BLOB)),0)+coalesce(length(CAST(r.application_id AS BLOB)),0)+256 AS bytes FROM mooring_collab_documents r UNION ALL SELECT r.workspace_id AS workspace_id,coalesce(length(CAST(r.result_json AS BLOB)),0)+coalesce(length(CAST(r.membership_hash AS BLOB)),0)+coalesce(length(CAST(r.task_id AS BLOB)),0)+256 AS bytes FROM mooring_collab_changes r UNION ALL SELECT r.workspace_id AS workspace_id,coalesce(length(CAST(r.result_json AS BLOB)),0)+coalesce(length(CAST(r.fingerprint AS BLOB)),0)+coalesce(length(CAST(r.op_id AS BLOB)),0)+coalesce(length(CAST(r.change_id AS BLOB)),0)+coalesce(length(CAST(r.participant_ref AS BLOB)),0)+coalesce(length(CAST(r.dependencies_json AS BLOB)),0)+256 AS bytes FROM mooring_collab_operations r UNION ALL SELECT r.workspace_id AS workspace_id,coalesce(length(CAST(r.checkpoint_base64 AS BLOB)),0)+coalesce(length(CAST(r.revision AS BLOB)),0)+256 AS bytes FROM mooring_collab_checkpoints r UNION ALL SELECT r.workspace_id AS workspace_id,coalesce(length(CAST(r.detail_json AS BLOB)),0)+coalesce(length(CAST(r.kind AS BLOB)),0)+coalesce(length(CAST(r.id AS BLOB)),0)+256 AS bytes FROM mooring_collab_audit r UNION ALL SELECT r.workspace_id AS workspace_id,coalesce(length(CAST(r.participant_ref AS BLOB)),0)+coalesce(length(CAST(r.status AS BLOB)),0)+coalesce(length(CAST(r.task_id AS BLOB)),0)+256 AS bytes FROM mooring_collab_tasks r UNION ALL SELECT r.workspace_id AS workspace_id,coalesce(length(CAST(r.target_id AS BLOB)),0)+coalesce(length(CAST(r.revision AS BLOB)),0)+coalesce(length(CAST(r.structure_revision AS BLOB)),0)+256 AS bytes FROM mooring_collab_target_versions r UNION ALL SELECT r.workspace_id AS workspace_id,coalesce(length(CAST(r.record_json AS BLOB)),0)+256 AS bytes FROM mooring_collab_target_effects r UNION ALL SELECT r.workspace_id AS workspace_id,coalesce(length(CAST(r.application_id AS BLOB)),0)+coalesce(length(CAST(r.change_id AS BLOB)),0)+coalesce(length(CAST(r.membership_hash AS BLOB)),0)+coalesce(length(CAST(r.host_session_id AS BLOB)),0)+coalesce(length(CAST(r.human_user_id AS BLOB)),0)+coalesce(length(CAST(r.agent_id AS BLOB)),0)+coalesce(length(CAST(r.crew_id AS BLOB)),0)+coalesce(length(CAST(r.chat_id AS BLOB)),0)+coalesce(length(CAST(r.message_id AS BLOB)),0)+coalesce(length(CAST(r.work_id AS BLOB)),0)+coalesce(length(CAST(r.run_id AS BLOB)),0)+coalesce(length(CAST(r.created_at AS BLOB)),0)+256 AS bytes FROM mooring_collab_artifact_edits r) GROUP BY workspace_id;
-- Orphaned rows (workspace already gone) still count toward the instance
-- total so later deletes decrement symmetrically; only live workspaces get rows.
INSERT INTO artifact_storage_workspaces(workspace_id,used_bytes,max_bytes) SELECT s.workspace_id,s.bytes,(SELECT workspace_max_bytes FROM artifact_storage_global WHERE id=1) FROM artifact_storage_seed s WHERE s.workspace_id IN (SELECT id FROM workspaces);
UPDATE artifact_storage_global SET used_bytes=(SELECT coalesce(SUM(bytes),0) FROM artifact_storage_seed) WHERE id=1;
DROP TABLE temp.artifact_storage_seed;

-- Each charge/reservation is an atomic capacity acquisition inside the same
-- SQLite writer transaction as the payload. A refusal rolls back that whole
-- transaction, including any new blobs; it cannot delete confirmed history.
CREATE TRIGGER artifact_storage_workspace_capacity BEFORE UPDATE OF used_bytes,reserved_bytes ON artifact_storage_workspaces
WHEN (NEW.used_bytes>OLD.used_bytes OR NEW.reserved_bytes>OLD.reserved_bytes) AND NEW.used_bytes+NEW.reserved_bytes>NEW.max_bytes
BEGIN SELECT RAISE(ABORT,'artifact_storage_quota'); END;
CREATE TRIGGER artifact_storage_instance_capacity BEFORE UPDATE OF used_bytes,reserved_bytes ON artifact_storage_global
WHEN (NEW.used_bytes>OLD.used_bytes OR NEW.reserved_bytes>OLD.reserved_bytes) AND NEW.used_bytes+NEW.reserved_bytes>NEW.max_bytes
BEGIN SELECT RAISE(ABORT,'artifact_storage_quota'); END;

CREATE TRIGGER artifact_version_blobs_storage_insert AFTER INSERT ON artifact_version_blobs
BEGIN
 INSERT INTO artifact_storage_workspaces(workspace_id,max_bytes) SELECT NEW.workspace_id,workspace_max_bytes FROM artifact_storage_global WHERE id=1 AND NEW.workspace_id IS NOT NULL ON CONFLICT(workspace_id) DO NOTHING;
 UPDATE artifact_storage_workspaces SET used_bytes=used_bytes+(coalesce(length(CAST(NEW.content AS BLOB)),0)+128) WHERE workspace_id=NEW.workspace_id;
 UPDATE artifact_storage_global SET used_bytes=used_bytes+(coalesce(length(CAST(NEW.content AS BLOB)),0)+128) WHERE id=1;
END;
CREATE TRIGGER artifact_version_blobs_storage_delete AFTER DELETE ON artifact_version_blobs
BEGIN
 UPDATE artifact_storage_workspaces SET used_bytes=max(0,used_bytes-(coalesce(length(CAST(OLD.content AS BLOB)),0)+128)) WHERE workspace_id=OLD.workspace_id;
 UPDATE artifact_storage_global SET used_bytes=max(0,used_bytes-(coalesce(length(CAST(OLD.content AS BLOB)),0)+128)) WHERE id=1;
END;
CREATE TRIGGER artifact_version_blobs_storage_update AFTER UPDATE ON artifact_version_blobs
BEGIN
 INSERT INTO artifact_storage_workspaces(workspace_id,max_bytes) SELECT NEW.workspace_id,workspace_max_bytes FROM artifact_storage_global WHERE id=1 AND NEW.workspace_id IS NOT NULL ON CONFLICT(workspace_id) DO NOTHING;
 UPDATE artifact_storage_workspaces SET used_bytes=max(0,used_bytes+(coalesce(length(CAST(NEW.content AS BLOB)),0)+128)-(coalesce(length(CAST(OLD.content AS BLOB)),0)+128)) WHERE workspace_id=NEW.workspace_id AND NEW.workspace_id=OLD.workspace_id;
 UPDATE artifact_storage_workspaces SET used_bytes=max(0,used_bytes-(coalesce(length(CAST(OLD.content AS BLOB)),0)+128)) WHERE workspace_id=OLD.workspace_id AND NEW.workspace_id<>OLD.workspace_id;
 UPDATE artifact_storage_workspaces SET used_bytes=used_bytes+(coalesce(length(CAST(NEW.content AS BLOB)),0)+128) WHERE workspace_id=NEW.workspace_id AND NEW.workspace_id<>OLD.workspace_id;
 UPDATE artifact_storage_global SET used_bytes=max(0,used_bytes+(coalesce(length(CAST(NEW.content AS BLOB)),0)+128)-(coalesce(length(CAST(OLD.content AS BLOB)),0)+128)) WHERE id=1;
END;

CREATE TRIGGER artifact_versions_storage_insert AFTER INSERT ON artifact_versions
BEGIN
 INSERT INTO artifact_storage_workspaces(workspace_id,max_bytes) SELECT NEW.workspace_id,workspace_max_bytes FROM artifact_storage_global WHERE id=1 AND NEW.workspace_id IS NOT NULL ON CONFLICT(workspace_id) DO NOTHING;
 UPDATE artifact_storage_workspaces SET used_bytes=used_bytes+(coalesce(length(CAST(NEW.manifest_json AS BLOB)),0)+coalesce(length(CAST(NEW.message AS BLOB)),0)+coalesce(length(CAST(NEW.actor AS BLOB)),0)+coalesce(length(CAST(NEW.created_at AS BLOB)),0)+128) WHERE workspace_id=NEW.workspace_id;
 UPDATE artifact_storage_global SET used_bytes=used_bytes+(coalesce(length(CAST(NEW.manifest_json AS BLOB)),0)+coalesce(length(CAST(NEW.message AS BLOB)),0)+coalesce(length(CAST(NEW.actor AS BLOB)),0)+coalesce(length(CAST(NEW.created_at AS BLOB)),0)+128) WHERE id=1;
END;
CREATE TRIGGER artifact_versions_storage_delete AFTER DELETE ON artifact_versions
BEGIN
 UPDATE artifact_storage_workspaces SET used_bytes=max(0,used_bytes-(coalesce(length(CAST(OLD.manifest_json AS BLOB)),0)+coalesce(length(CAST(OLD.message AS BLOB)),0)+coalesce(length(CAST(OLD.actor AS BLOB)),0)+coalesce(length(CAST(OLD.created_at AS BLOB)),0)+128)) WHERE workspace_id=OLD.workspace_id;
 UPDATE artifact_storage_global SET used_bytes=max(0,used_bytes-(coalesce(length(CAST(OLD.manifest_json AS BLOB)),0)+coalesce(length(CAST(OLD.message AS BLOB)),0)+coalesce(length(CAST(OLD.actor AS BLOB)),0)+coalesce(length(CAST(OLD.created_at AS BLOB)),0)+128)) WHERE id=1;
END;
CREATE TRIGGER artifact_versions_storage_update AFTER UPDATE ON artifact_versions
BEGIN
 INSERT INTO artifact_storage_workspaces(workspace_id,max_bytes) SELECT NEW.workspace_id,workspace_max_bytes FROM artifact_storage_global WHERE id=1 AND NEW.workspace_id IS NOT NULL ON CONFLICT(workspace_id) DO NOTHING;
 UPDATE artifact_storage_workspaces SET used_bytes=max(0,used_bytes+(coalesce(length(CAST(NEW.manifest_json AS BLOB)),0)+coalesce(length(CAST(NEW.message AS BLOB)),0)+coalesce(length(CAST(NEW.actor AS BLOB)),0)+coalesce(length(CAST(NEW.created_at AS BLOB)),0)+128)-(coalesce(length(CAST(OLD.manifest_json AS BLOB)),0)+coalesce(length(CAST(OLD.message AS BLOB)),0)+coalesce(length(CAST(OLD.actor AS BLOB)),0)+coalesce(length(CAST(OLD.created_at AS BLOB)),0)+128)) WHERE workspace_id=NEW.workspace_id AND NEW.workspace_id=OLD.workspace_id;
 UPDATE artifact_storage_workspaces SET used_bytes=max(0,used_bytes-(coalesce(length(CAST(OLD.manifest_json AS BLOB)),0)+coalesce(length(CAST(OLD.message AS BLOB)),0)+coalesce(length(CAST(OLD.actor AS BLOB)),0)+coalesce(length(CAST(OLD.created_at AS BLOB)),0)+128)) WHERE workspace_id=OLD.workspace_id AND NEW.workspace_id<>OLD.workspace_id;
 UPDATE artifact_storage_workspaces SET used_bytes=used_bytes+(coalesce(length(CAST(NEW.manifest_json AS BLOB)),0)+coalesce(length(CAST(NEW.message AS BLOB)),0)+coalesce(length(CAST(NEW.actor AS BLOB)),0)+coalesce(length(CAST(NEW.created_at AS BLOB)),0)+128) WHERE workspace_id=NEW.workspace_id AND NEW.workspace_id<>OLD.workspace_id;
 UPDATE artifact_storage_global SET used_bytes=max(0,used_bytes+(coalesce(length(CAST(NEW.manifest_json AS BLOB)),0)+coalesce(length(CAST(NEW.message AS BLOB)),0)+coalesce(length(CAST(NEW.actor AS BLOB)),0)+coalesce(length(CAST(NEW.created_at AS BLOB)),0)+128)-(coalesce(length(CAST(OLD.manifest_json AS BLOB)),0)+coalesce(length(CAST(OLD.message AS BLOB)),0)+coalesce(length(CAST(OLD.actor AS BLOB)),0)+coalesce(length(CAST(OLD.created_at AS BLOB)),0)+128)) WHERE id=1;
END;

CREATE TRIGGER craft_native_proposals_storage_insert AFTER INSERT ON craft_native_proposals
BEGIN
 INSERT INTO artifact_storage_workspaces(workspace_id,max_bytes) SELECT NEW.workspace_id,workspace_max_bytes FROM artifact_storage_global WHERE id=1 AND NEW.workspace_id IS NOT NULL ON CONFLICT(workspace_id) DO NOTHING;
 UPDATE artifact_storage_workspaces SET used_bytes=used_bytes+(coalesce(length(CAST(NEW.content AS BLOB)),0)+coalesce(length(CAST(NEW.summary AS BLOB)),0)+coalesce(length(CAST(NEW.author_actor_id AS BLOB)),0)+coalesce(length(CAST(NEW.request_id AS BLOB)),0)+coalesce(length(CAST(NEW.id AS BLOB)),0)+coalesce(length(CAST(NEW.engine_pin AS BLOB)),0)+coalesce(length(CAST(NEW.content_hash AS BLOB)),0)+coalesce(length(CAST(NEW.created_at AS BLOB)),0)+512) WHERE workspace_id=NEW.workspace_id;
 UPDATE artifact_storage_global SET used_bytes=used_bytes+(coalesce(length(CAST(NEW.content AS BLOB)),0)+coalesce(length(CAST(NEW.summary AS BLOB)),0)+coalesce(length(CAST(NEW.author_actor_id AS BLOB)),0)+coalesce(length(CAST(NEW.request_id AS BLOB)),0)+coalesce(length(CAST(NEW.id AS BLOB)),0)+coalesce(length(CAST(NEW.engine_pin AS BLOB)),0)+coalesce(length(CAST(NEW.content_hash AS BLOB)),0)+coalesce(length(CAST(NEW.created_at AS BLOB)),0)+512) WHERE id=1;
END;
CREATE TRIGGER craft_native_proposals_storage_delete AFTER DELETE ON craft_native_proposals
BEGIN
 UPDATE artifact_storage_workspaces SET used_bytes=max(0,used_bytes-(coalesce(length(CAST(OLD.content AS BLOB)),0)+coalesce(length(CAST(OLD.summary AS BLOB)),0)+coalesce(length(CAST(OLD.author_actor_id AS BLOB)),0)+coalesce(length(CAST(OLD.request_id AS BLOB)),0)+coalesce(length(CAST(OLD.id AS BLOB)),0)+coalesce(length(CAST(OLD.engine_pin AS BLOB)),0)+coalesce(length(CAST(OLD.content_hash AS BLOB)),0)+coalesce(length(CAST(OLD.created_at AS BLOB)),0)+512)) WHERE workspace_id=OLD.workspace_id;
 UPDATE artifact_storage_global SET used_bytes=max(0,used_bytes-(coalesce(length(CAST(OLD.content AS BLOB)),0)+coalesce(length(CAST(OLD.summary AS BLOB)),0)+coalesce(length(CAST(OLD.author_actor_id AS BLOB)),0)+coalesce(length(CAST(OLD.request_id AS BLOB)),0)+coalesce(length(CAST(OLD.id AS BLOB)),0)+coalesce(length(CAST(OLD.engine_pin AS BLOB)),0)+coalesce(length(CAST(OLD.content_hash AS BLOB)),0)+coalesce(length(CAST(OLD.created_at AS BLOB)),0)+512)) WHERE id=1;
END;
CREATE TRIGGER craft_native_proposals_storage_update AFTER UPDATE ON craft_native_proposals
BEGIN
 INSERT INTO artifact_storage_workspaces(workspace_id,max_bytes) SELECT NEW.workspace_id,workspace_max_bytes FROM artifact_storage_global WHERE id=1 AND NEW.workspace_id IS NOT NULL ON CONFLICT(workspace_id) DO NOTHING;
 UPDATE artifact_storage_workspaces SET used_bytes=max(0,used_bytes+(coalesce(length(CAST(NEW.content AS BLOB)),0)+coalesce(length(CAST(NEW.summary AS BLOB)),0)+coalesce(length(CAST(NEW.author_actor_id AS BLOB)),0)+coalesce(length(CAST(NEW.request_id AS BLOB)),0)+coalesce(length(CAST(NEW.id AS BLOB)),0)+coalesce(length(CAST(NEW.engine_pin AS BLOB)),0)+coalesce(length(CAST(NEW.content_hash AS BLOB)),0)+coalesce(length(CAST(NEW.created_at AS BLOB)),0)+512)-(coalesce(length(CAST(OLD.content AS BLOB)),0)+coalesce(length(CAST(OLD.summary AS BLOB)),0)+coalesce(length(CAST(OLD.author_actor_id AS BLOB)),0)+coalesce(length(CAST(OLD.request_id AS BLOB)),0)+coalesce(length(CAST(OLD.id AS BLOB)),0)+coalesce(length(CAST(OLD.engine_pin AS BLOB)),0)+coalesce(length(CAST(OLD.content_hash AS BLOB)),0)+coalesce(length(CAST(OLD.created_at AS BLOB)),0)+512)) WHERE workspace_id=NEW.workspace_id AND NEW.workspace_id=OLD.workspace_id;
 UPDATE artifact_storage_workspaces SET used_bytes=max(0,used_bytes-(coalesce(length(CAST(OLD.content AS BLOB)),0)+coalesce(length(CAST(OLD.summary AS BLOB)),0)+coalesce(length(CAST(OLD.author_actor_id AS BLOB)),0)+coalesce(length(CAST(OLD.request_id AS BLOB)),0)+coalesce(length(CAST(OLD.id AS BLOB)),0)+coalesce(length(CAST(OLD.engine_pin AS BLOB)),0)+coalesce(length(CAST(OLD.content_hash AS BLOB)),0)+coalesce(length(CAST(OLD.created_at AS BLOB)),0)+512)) WHERE workspace_id=OLD.workspace_id AND NEW.workspace_id<>OLD.workspace_id;
 UPDATE artifact_storage_workspaces SET used_bytes=used_bytes+(coalesce(length(CAST(NEW.content AS BLOB)),0)+coalesce(length(CAST(NEW.summary AS BLOB)),0)+coalesce(length(CAST(NEW.author_actor_id AS BLOB)),0)+coalesce(length(CAST(NEW.request_id AS BLOB)),0)+coalesce(length(CAST(NEW.id AS BLOB)),0)+coalesce(length(CAST(NEW.engine_pin AS BLOB)),0)+coalesce(length(CAST(NEW.content_hash AS BLOB)),0)+coalesce(length(CAST(NEW.created_at AS BLOB)),0)+512) WHERE workspace_id=NEW.workspace_id AND NEW.workspace_id<>OLD.workspace_id;
 UPDATE artifact_storage_global SET used_bytes=max(0,used_bytes+(coalesce(length(CAST(NEW.content AS BLOB)),0)+coalesce(length(CAST(NEW.summary AS BLOB)),0)+coalesce(length(CAST(NEW.author_actor_id AS BLOB)),0)+coalesce(length(CAST(NEW.request_id AS BLOB)),0)+coalesce(length(CAST(NEW.id AS BLOB)),0)+coalesce(length(CAST(NEW.engine_pin AS BLOB)),0)+coalesce(length(CAST(NEW.content_hash AS BLOB)),0)+coalesce(length(CAST(NEW.created_at AS BLOB)),0)+512)-(coalesce(length(CAST(OLD.content AS BLOB)),0)+coalesce(length(CAST(OLD.summary AS BLOB)),0)+coalesce(length(CAST(OLD.author_actor_id AS BLOB)),0)+coalesce(length(CAST(OLD.request_id AS BLOB)),0)+coalesce(length(CAST(OLD.id AS BLOB)),0)+coalesce(length(CAST(OLD.engine_pin AS BLOB)),0)+coalesce(length(CAST(OLD.content_hash AS BLOB)),0)+coalesce(length(CAST(OLD.created_at AS BLOB)),0)+512)) WHERE id=1;
END;

CREATE TRIGGER artifact_design_documents_storage_insert AFTER INSERT ON artifact_design_documents
BEGIN
 INSERT INTO artifact_storage_workspaces(workspace_id,max_bytes) SELECT NEW.workspace_id,workspace_max_bytes FROM artifact_storage_global WHERE id=1 AND NEW.workspace_id IS NOT NULL ON CONFLICT(workspace_id) DO NOTHING;
 UPDATE artifact_storage_workspaces SET used_bytes=used_bytes+(coalesce(length(CAST(NEW.state AS BLOB)),0)+coalesce(length(CAST(NEW.updated_by AS BLOB)),0)+coalesce(length(CAST(NEW.updated_at AS BLOB)),0)+128) WHERE workspace_id=NEW.workspace_id;
 UPDATE artifact_storage_global SET used_bytes=used_bytes+(coalesce(length(CAST(NEW.state AS BLOB)),0)+coalesce(length(CAST(NEW.updated_by AS BLOB)),0)+coalesce(length(CAST(NEW.updated_at AS BLOB)),0)+128) WHERE id=1;
END;
CREATE TRIGGER artifact_design_documents_storage_delete AFTER DELETE ON artifact_design_documents
BEGIN
 UPDATE artifact_storage_workspaces SET used_bytes=max(0,used_bytes-(coalesce(length(CAST(OLD.state AS BLOB)),0)+coalesce(length(CAST(OLD.updated_by AS BLOB)),0)+coalesce(length(CAST(OLD.updated_at AS BLOB)),0)+128)) WHERE workspace_id=OLD.workspace_id;
 UPDATE artifact_storage_global SET used_bytes=max(0,used_bytes-(coalesce(length(CAST(OLD.state AS BLOB)),0)+coalesce(length(CAST(OLD.updated_by AS BLOB)),0)+coalesce(length(CAST(OLD.updated_at AS BLOB)),0)+128)) WHERE id=1;
END;
CREATE TRIGGER artifact_design_documents_storage_update AFTER UPDATE ON artifact_design_documents
BEGIN
 INSERT INTO artifact_storage_workspaces(workspace_id,max_bytes) SELECT NEW.workspace_id,workspace_max_bytes FROM artifact_storage_global WHERE id=1 AND NEW.workspace_id IS NOT NULL ON CONFLICT(workspace_id) DO NOTHING;
 UPDATE artifact_storage_workspaces SET used_bytes=max(0,used_bytes+(coalesce(length(CAST(NEW.state AS BLOB)),0)+coalesce(length(CAST(NEW.updated_by AS BLOB)),0)+coalesce(length(CAST(NEW.updated_at AS BLOB)),0)+128)-(coalesce(length(CAST(OLD.state AS BLOB)),0)+coalesce(length(CAST(OLD.updated_by AS BLOB)),0)+coalesce(length(CAST(OLD.updated_at AS BLOB)),0)+128)) WHERE workspace_id=NEW.workspace_id AND NEW.workspace_id=OLD.workspace_id;
 UPDATE artifact_storage_workspaces SET used_bytes=max(0,used_bytes-(coalesce(length(CAST(OLD.state AS BLOB)),0)+coalesce(length(CAST(OLD.updated_by AS BLOB)),0)+coalesce(length(CAST(OLD.updated_at AS BLOB)),0)+128)) WHERE workspace_id=OLD.workspace_id AND NEW.workspace_id<>OLD.workspace_id;
 UPDATE artifact_storage_workspaces SET used_bytes=used_bytes+(coalesce(length(CAST(NEW.state AS BLOB)),0)+coalesce(length(CAST(NEW.updated_by AS BLOB)),0)+coalesce(length(CAST(NEW.updated_at AS BLOB)),0)+128) WHERE workspace_id=NEW.workspace_id AND NEW.workspace_id<>OLD.workspace_id;
 UPDATE artifact_storage_global SET used_bytes=max(0,used_bytes+(coalesce(length(CAST(NEW.state AS BLOB)),0)+coalesce(length(CAST(NEW.updated_by AS BLOB)),0)+coalesce(length(CAST(NEW.updated_at AS BLOB)),0)+128)-(coalesce(length(CAST(OLD.state AS BLOB)),0)+coalesce(length(CAST(OLD.updated_by AS BLOB)),0)+coalesce(length(CAST(OLD.updated_at AS BLOB)),0)+128)) WHERE id=1;
END;

CREATE TRIGGER artifact_data_collections_storage_insert AFTER INSERT ON artifact_data_collections
BEGIN
 INSERT INTO artifact_storage_workspaces(workspace_id,max_bytes) SELECT NEW.workspace_id,workspace_max_bytes FROM artifact_storage_global WHERE id=1 AND NEW.workspace_id IS NOT NULL ON CONFLICT(workspace_id) DO NOTHING;
 UPDATE artifact_storage_workspaces SET used_bytes=used_bytes+(coalesce(length(CAST(NEW.name AS BLOB)),0)+128) WHERE workspace_id=NEW.workspace_id;
 UPDATE artifact_storage_global SET used_bytes=used_bytes+(coalesce(length(CAST(NEW.name AS BLOB)),0)+128) WHERE id=1;
END;
CREATE TRIGGER artifact_data_collections_storage_delete AFTER DELETE ON artifact_data_collections
BEGIN
 UPDATE artifact_storage_workspaces SET used_bytes=max(0,used_bytes-(coalesce(length(CAST(OLD.name AS BLOB)),0)+128)) WHERE workspace_id=OLD.workspace_id;
 UPDATE artifact_storage_global SET used_bytes=max(0,used_bytes-(coalesce(length(CAST(OLD.name AS BLOB)),0)+128)) WHERE id=1;
END;
CREATE TRIGGER artifact_data_collections_storage_update AFTER UPDATE ON artifact_data_collections
BEGIN
 INSERT INTO artifact_storage_workspaces(workspace_id,max_bytes) SELECT NEW.workspace_id,workspace_max_bytes FROM artifact_storage_global WHERE id=1 AND NEW.workspace_id IS NOT NULL ON CONFLICT(workspace_id) DO NOTHING;
 UPDATE artifact_storage_workspaces SET used_bytes=max(0,used_bytes+(coalesce(length(CAST(NEW.name AS BLOB)),0)+128)-(coalesce(length(CAST(OLD.name AS BLOB)),0)+128)) WHERE workspace_id=NEW.workspace_id AND NEW.workspace_id=OLD.workspace_id;
 UPDATE artifact_storage_workspaces SET used_bytes=max(0,used_bytes-(coalesce(length(CAST(OLD.name AS BLOB)),0)+128)) WHERE workspace_id=OLD.workspace_id AND NEW.workspace_id<>OLD.workspace_id;
 UPDATE artifact_storage_workspaces SET used_bytes=used_bytes+(coalesce(length(CAST(NEW.name AS BLOB)),0)+128) WHERE workspace_id=NEW.workspace_id AND NEW.workspace_id<>OLD.workspace_id;
 UPDATE artifact_storage_global SET used_bytes=max(0,used_bytes+(coalesce(length(CAST(NEW.name AS BLOB)),0)+128)-(coalesce(length(CAST(OLD.name AS BLOB)),0)+128)) WHERE id=1;
END;

CREATE TRIGGER artifact_data_documents_storage_insert AFTER INSERT ON artifact_data_documents
BEGIN
 INSERT INTO artifact_storage_workspaces(workspace_id,max_bytes) SELECT NEW.workspace_id,workspace_max_bytes FROM artifact_storage_global WHERE id=1 AND NEW.workspace_id IS NOT NULL ON CONFLICT(workspace_id) DO NOTHING;
 UPDATE artifact_storage_workspaces SET used_bytes=used_bytes+(coalesce(length(CAST(NEW.value_json AS BLOB)),0)+coalesce(length(CAST(NEW.id AS BLOB)),0)+coalesce(length(CAST(NEW.collection AS BLOB)),0)+coalesce(length(CAST(NEW.updated_by AS BLOB)),0)+coalesce(length(CAST(NEW.updated_at AS BLOB)),0)+128) WHERE workspace_id=NEW.workspace_id;
 UPDATE artifact_storage_global SET used_bytes=used_bytes+(coalesce(length(CAST(NEW.value_json AS BLOB)),0)+coalesce(length(CAST(NEW.id AS BLOB)),0)+coalesce(length(CAST(NEW.collection AS BLOB)),0)+coalesce(length(CAST(NEW.updated_by AS BLOB)),0)+coalesce(length(CAST(NEW.updated_at AS BLOB)),0)+128) WHERE id=1;
END;
CREATE TRIGGER artifact_data_documents_storage_delete AFTER DELETE ON artifact_data_documents
BEGIN
 UPDATE artifact_storage_workspaces SET used_bytes=max(0,used_bytes-(coalesce(length(CAST(OLD.value_json AS BLOB)),0)+coalesce(length(CAST(OLD.id AS BLOB)),0)+coalesce(length(CAST(OLD.collection AS BLOB)),0)+coalesce(length(CAST(OLD.updated_by AS BLOB)),0)+coalesce(length(CAST(OLD.updated_at AS BLOB)),0)+128)) WHERE workspace_id=OLD.workspace_id;
 UPDATE artifact_storage_global SET used_bytes=max(0,used_bytes-(coalesce(length(CAST(OLD.value_json AS BLOB)),0)+coalesce(length(CAST(OLD.id AS BLOB)),0)+coalesce(length(CAST(OLD.collection AS BLOB)),0)+coalesce(length(CAST(OLD.updated_by AS BLOB)),0)+coalesce(length(CAST(OLD.updated_at AS BLOB)),0)+128)) WHERE id=1;
END;
CREATE TRIGGER artifact_data_documents_storage_update AFTER UPDATE ON artifact_data_documents
BEGIN
 INSERT INTO artifact_storage_workspaces(workspace_id,max_bytes) SELECT NEW.workspace_id,workspace_max_bytes FROM artifact_storage_global WHERE id=1 AND NEW.workspace_id IS NOT NULL ON CONFLICT(workspace_id) DO NOTHING;
 UPDATE artifact_storage_workspaces SET used_bytes=max(0,used_bytes+(coalesce(length(CAST(NEW.value_json AS BLOB)),0)+coalesce(length(CAST(NEW.id AS BLOB)),0)+coalesce(length(CAST(NEW.collection AS BLOB)),0)+coalesce(length(CAST(NEW.updated_by AS BLOB)),0)+coalesce(length(CAST(NEW.updated_at AS BLOB)),0)+128)-(coalesce(length(CAST(OLD.value_json AS BLOB)),0)+coalesce(length(CAST(OLD.id AS BLOB)),0)+coalesce(length(CAST(OLD.collection AS BLOB)),0)+coalesce(length(CAST(OLD.updated_by AS BLOB)),0)+coalesce(length(CAST(OLD.updated_at AS BLOB)),0)+128)) WHERE workspace_id=NEW.workspace_id AND NEW.workspace_id=OLD.workspace_id;
 UPDATE artifact_storage_workspaces SET used_bytes=max(0,used_bytes-(coalesce(length(CAST(OLD.value_json AS BLOB)),0)+coalesce(length(CAST(OLD.id AS BLOB)),0)+coalesce(length(CAST(OLD.collection AS BLOB)),0)+coalesce(length(CAST(OLD.updated_by AS BLOB)),0)+coalesce(length(CAST(OLD.updated_at AS BLOB)),0)+128)) WHERE workspace_id=OLD.workspace_id AND NEW.workspace_id<>OLD.workspace_id;
 UPDATE artifact_storage_workspaces SET used_bytes=used_bytes+(coalesce(length(CAST(NEW.value_json AS BLOB)),0)+coalesce(length(CAST(NEW.id AS BLOB)),0)+coalesce(length(CAST(NEW.collection AS BLOB)),0)+coalesce(length(CAST(NEW.updated_by AS BLOB)),0)+coalesce(length(CAST(NEW.updated_at AS BLOB)),0)+128) WHERE workspace_id=NEW.workspace_id AND NEW.workspace_id<>OLD.workspace_id;
 UPDATE artifact_storage_global SET used_bytes=max(0,used_bytes+(coalesce(length(CAST(NEW.value_json AS BLOB)),0)+coalesce(length(CAST(NEW.id AS BLOB)),0)+coalesce(length(CAST(NEW.collection AS BLOB)),0)+coalesce(length(CAST(NEW.updated_by AS BLOB)),0)+coalesce(length(CAST(NEW.updated_at AS BLOB)),0)+128)-(coalesce(length(CAST(OLD.value_json AS BLOB)),0)+coalesce(length(CAST(OLD.id AS BLOB)),0)+coalesce(length(CAST(OLD.collection AS BLOB)),0)+coalesce(length(CAST(OLD.updated_by AS BLOB)),0)+coalesce(length(CAST(OLD.updated_at AS BLOB)),0)+128)) WHERE id=1;
END;

CREATE TRIGGER documents_storage_insert AFTER INSERT ON documents
BEGIN
 INSERT INTO artifact_storage_workspaces(workspace_id,max_bytes) SELECT NEW.workspace_id,workspace_max_bytes FROM artifact_storage_global WHERE id=1 AND NEW.workspace_id IS NOT NULL ON CONFLICT(workspace_id) DO NOTHING;
 UPDATE artifact_storage_workspaces SET used_bytes=used_bytes+(coalesce(length(CAST(NEW.snapshot AS BLOB)),0)+coalesce(length(CAST(NEW.projection AS BLOB)),0)+coalesce(length(CAST(NEW.writer AS BLOB)),0)+coalesce(length(CAST(NEW.owner AS BLOB)),0)+256) WHERE workspace_id=NEW.workspace_id;
 UPDATE artifact_storage_global SET used_bytes=used_bytes+(coalesce(length(CAST(NEW.snapshot AS BLOB)),0)+coalesce(length(CAST(NEW.projection AS BLOB)),0)+coalesce(length(CAST(NEW.writer AS BLOB)),0)+coalesce(length(CAST(NEW.owner AS BLOB)),0)+256) WHERE id=1;
END;
CREATE TRIGGER documents_storage_delete AFTER DELETE ON documents
BEGIN
 UPDATE artifact_storage_workspaces SET used_bytes=max(0,used_bytes-(coalesce(length(CAST(OLD.snapshot AS BLOB)),0)+coalesce(length(CAST(OLD.projection AS BLOB)),0)+coalesce(length(CAST(OLD.writer AS BLOB)),0)+coalesce(length(CAST(OLD.owner AS BLOB)),0)+256)) WHERE workspace_id=OLD.workspace_id;
 UPDATE artifact_storage_global SET used_bytes=max(0,used_bytes-(coalesce(length(CAST(OLD.snapshot AS BLOB)),0)+coalesce(length(CAST(OLD.projection AS BLOB)),0)+coalesce(length(CAST(OLD.writer AS BLOB)),0)+coalesce(length(CAST(OLD.owner AS BLOB)),0)+256)) WHERE id=1;
END;
CREATE TRIGGER documents_storage_update AFTER UPDATE ON documents
BEGIN
 INSERT INTO artifact_storage_workspaces(workspace_id,max_bytes) SELECT NEW.workspace_id,workspace_max_bytes FROM artifact_storage_global WHERE id=1 AND NEW.workspace_id IS NOT NULL ON CONFLICT(workspace_id) DO NOTHING;
 UPDATE artifact_storage_workspaces SET used_bytes=max(0,used_bytes+(coalesce(length(CAST(NEW.snapshot AS BLOB)),0)+coalesce(length(CAST(NEW.projection AS BLOB)),0)+coalesce(length(CAST(NEW.writer AS BLOB)),0)+coalesce(length(CAST(NEW.owner AS BLOB)),0)+256)-(coalesce(length(CAST(OLD.snapshot AS BLOB)),0)+coalesce(length(CAST(OLD.projection AS BLOB)),0)+coalesce(length(CAST(OLD.writer AS BLOB)),0)+coalesce(length(CAST(OLD.owner AS BLOB)),0)+256)) WHERE workspace_id=NEW.workspace_id AND NEW.workspace_id=OLD.workspace_id;
 UPDATE artifact_storage_workspaces SET used_bytes=max(0,used_bytes-(coalesce(length(CAST(OLD.snapshot AS BLOB)),0)+coalesce(length(CAST(OLD.projection AS BLOB)),0)+coalesce(length(CAST(OLD.writer AS BLOB)),0)+coalesce(length(CAST(OLD.owner AS BLOB)),0)+256)) WHERE workspace_id=OLD.workspace_id AND NEW.workspace_id<>OLD.workspace_id;
 UPDATE artifact_storage_workspaces SET used_bytes=used_bytes+(coalesce(length(CAST(NEW.snapshot AS BLOB)),0)+coalesce(length(CAST(NEW.projection AS BLOB)),0)+coalesce(length(CAST(NEW.writer AS BLOB)),0)+coalesce(length(CAST(NEW.owner AS BLOB)),0)+256) WHERE workspace_id=NEW.workspace_id AND NEW.workspace_id<>OLD.workspace_id;
 UPDATE artifact_storage_global SET used_bytes=max(0,used_bytes+(coalesce(length(CAST(NEW.snapshot AS BLOB)),0)+coalesce(length(CAST(NEW.projection AS BLOB)),0)+coalesce(length(CAST(NEW.writer AS BLOB)),0)+coalesce(length(CAST(NEW.owner AS BLOB)),0)+256)-(coalesce(length(CAST(OLD.snapshot AS BLOB)),0)+coalesce(length(CAST(OLD.projection AS BLOB)),0)+coalesce(length(CAST(OLD.writer AS BLOB)),0)+coalesce(length(CAST(OLD.owner AS BLOB)),0)+256)) WHERE id=1;
END;

CREATE TRIGGER document_updates_storage_insert AFTER INSERT ON document_updates
BEGIN
 INSERT INTO artifact_storage_workspaces(workspace_id,max_bytes) SELECT NEW.workspace_id,workspace_max_bytes FROM artifact_storage_global WHERE id=1 AND NEW.workspace_id IS NOT NULL ON CONFLICT(workspace_id) DO NOTHING;
 UPDATE artifact_storage_workspaces SET used_bytes=used_bytes+(coalesce(length(CAST(NEW.payload AS BLOB)),0)+128) WHERE workspace_id=NEW.workspace_id;
 UPDATE artifact_storage_global SET used_bytes=used_bytes+(coalesce(length(CAST(NEW.payload AS BLOB)),0)+128) WHERE id=1;
END;
CREATE TRIGGER document_updates_storage_delete AFTER DELETE ON document_updates
BEGIN
 UPDATE artifact_storage_workspaces SET used_bytes=max(0,used_bytes-(coalesce(length(CAST(OLD.payload AS BLOB)),0)+128)) WHERE workspace_id=OLD.workspace_id;
 UPDATE artifact_storage_global SET used_bytes=max(0,used_bytes-(coalesce(length(CAST(OLD.payload AS BLOB)),0)+128)) WHERE id=1;
END;
CREATE TRIGGER document_updates_storage_update AFTER UPDATE ON document_updates
BEGIN
 INSERT INTO artifact_storage_workspaces(workspace_id,max_bytes) SELECT NEW.workspace_id,workspace_max_bytes FROM artifact_storage_global WHERE id=1 AND NEW.workspace_id IS NOT NULL ON CONFLICT(workspace_id) DO NOTHING;
 UPDATE artifact_storage_workspaces SET used_bytes=max(0,used_bytes+(coalesce(length(CAST(NEW.payload AS BLOB)),0)+128)-(coalesce(length(CAST(OLD.payload AS BLOB)),0)+128)) WHERE workspace_id=NEW.workspace_id AND NEW.workspace_id=OLD.workspace_id;
 UPDATE artifact_storage_workspaces SET used_bytes=max(0,used_bytes-(coalesce(length(CAST(OLD.payload AS BLOB)),0)+128)) WHERE workspace_id=OLD.workspace_id AND NEW.workspace_id<>OLD.workspace_id;
 UPDATE artifact_storage_workspaces SET used_bytes=used_bytes+(coalesce(length(CAST(NEW.payload AS BLOB)),0)+128) WHERE workspace_id=NEW.workspace_id AND NEW.workspace_id<>OLD.workspace_id;
 UPDATE artifact_storage_global SET used_bytes=max(0,used_bytes+(coalesce(length(CAST(NEW.payload AS BLOB)),0)+128)-(coalesce(length(CAST(OLD.payload AS BLOB)),0)+128)) WHERE id=1;
END;

CREATE TRIGGER document_receipts_storage_insert AFTER INSERT ON document_receipts
BEGIN
 INSERT INTO artifact_storage_workspaces(workspace_id,max_bytes) SELECT NEW.workspace_id,workspace_max_bytes FROM artifact_storage_global WHERE id=1 AND NEW.workspace_id IS NOT NULL ON CONFLICT(workspace_id) DO NOTHING;
 UPDATE artifact_storage_workspaces SET used_bytes=used_bytes+(coalesce(length(CAST(NEW.digest AS BLOB)),0)+coalesce(length(CAST(NEW.fingerprint AS BLOB)),0)+coalesce(length(CAST(NEW.actor AS BLOB)),0)+coalesce(length(CAST(NEW.kind AS BLOB)),0)+coalesce(length(CAST(NEW.created_at AS BLOB)),0)+256) WHERE workspace_id=NEW.workspace_id;
 UPDATE artifact_storage_global SET used_bytes=used_bytes+(coalesce(length(CAST(NEW.digest AS BLOB)),0)+coalesce(length(CAST(NEW.fingerprint AS BLOB)),0)+coalesce(length(CAST(NEW.actor AS BLOB)),0)+coalesce(length(CAST(NEW.kind AS BLOB)),0)+coalesce(length(CAST(NEW.created_at AS BLOB)),0)+256) WHERE id=1;
END;
CREATE TRIGGER document_receipts_storage_delete AFTER DELETE ON document_receipts
BEGIN
 UPDATE artifact_storage_workspaces SET used_bytes=max(0,used_bytes-(coalesce(length(CAST(OLD.digest AS BLOB)),0)+coalesce(length(CAST(OLD.fingerprint AS BLOB)),0)+coalesce(length(CAST(OLD.actor AS BLOB)),0)+coalesce(length(CAST(OLD.kind AS BLOB)),0)+coalesce(length(CAST(OLD.created_at AS BLOB)),0)+256)) WHERE workspace_id=OLD.workspace_id;
 UPDATE artifact_storage_global SET used_bytes=max(0,used_bytes-(coalesce(length(CAST(OLD.digest AS BLOB)),0)+coalesce(length(CAST(OLD.fingerprint AS BLOB)),0)+coalesce(length(CAST(OLD.actor AS BLOB)),0)+coalesce(length(CAST(OLD.kind AS BLOB)),0)+coalesce(length(CAST(OLD.created_at AS BLOB)),0)+256)) WHERE id=1;
END;
CREATE TRIGGER document_receipts_storage_update AFTER UPDATE ON document_receipts
BEGIN
 INSERT INTO artifact_storage_workspaces(workspace_id,max_bytes) SELECT NEW.workspace_id,workspace_max_bytes FROM artifact_storage_global WHERE id=1 AND NEW.workspace_id IS NOT NULL ON CONFLICT(workspace_id) DO NOTHING;
 UPDATE artifact_storage_workspaces SET used_bytes=max(0,used_bytes+(coalesce(length(CAST(NEW.digest AS BLOB)),0)+coalesce(length(CAST(NEW.fingerprint AS BLOB)),0)+coalesce(length(CAST(NEW.actor AS BLOB)),0)+coalesce(length(CAST(NEW.kind AS BLOB)),0)+coalesce(length(CAST(NEW.created_at AS BLOB)),0)+256)-(coalesce(length(CAST(OLD.digest AS BLOB)),0)+coalesce(length(CAST(OLD.fingerprint AS BLOB)),0)+coalesce(length(CAST(OLD.actor AS BLOB)),0)+coalesce(length(CAST(OLD.kind AS BLOB)),0)+coalesce(length(CAST(OLD.created_at AS BLOB)),0)+256)) WHERE workspace_id=NEW.workspace_id AND NEW.workspace_id=OLD.workspace_id;
 UPDATE artifact_storage_workspaces SET used_bytes=max(0,used_bytes-(coalesce(length(CAST(OLD.digest AS BLOB)),0)+coalesce(length(CAST(OLD.fingerprint AS BLOB)),0)+coalesce(length(CAST(OLD.actor AS BLOB)),0)+coalesce(length(CAST(OLD.kind AS BLOB)),0)+coalesce(length(CAST(OLD.created_at AS BLOB)),0)+256)) WHERE workspace_id=OLD.workspace_id AND NEW.workspace_id<>OLD.workspace_id;
 UPDATE artifact_storage_workspaces SET used_bytes=used_bytes+(coalesce(length(CAST(NEW.digest AS BLOB)),0)+coalesce(length(CAST(NEW.fingerprint AS BLOB)),0)+coalesce(length(CAST(NEW.actor AS BLOB)),0)+coalesce(length(CAST(NEW.kind AS BLOB)),0)+coalesce(length(CAST(NEW.created_at AS BLOB)),0)+256) WHERE workspace_id=NEW.workspace_id AND NEW.workspace_id<>OLD.workspace_id;
 UPDATE artifact_storage_global SET used_bytes=max(0,used_bytes+(coalesce(length(CAST(NEW.digest AS BLOB)),0)+coalesce(length(CAST(NEW.fingerprint AS BLOB)),0)+coalesce(length(CAST(NEW.actor AS BLOB)),0)+coalesce(length(CAST(NEW.kind AS BLOB)),0)+coalesce(length(CAST(NEW.created_at AS BLOB)),0)+256)-(coalesce(length(CAST(OLD.digest AS BLOB)),0)+coalesce(length(CAST(OLD.fingerprint AS BLOB)),0)+coalesce(length(CAST(OLD.actor AS BLOB)),0)+coalesce(length(CAST(OLD.kind AS BLOB)),0)+coalesce(length(CAST(OLD.created_at AS BLOB)),0)+256)) WHERE id=1;
END;

CREATE TRIGGER document_checkpoints_storage_insert AFTER INSERT ON document_checkpoints
BEGIN
 INSERT INTO artifact_storage_workspaces(workspace_id,max_bytes) SELECT NEW.workspace_id,workspace_max_bytes FROM artifact_storage_global WHERE id=1 AND NEW.workspace_id IS NOT NULL ON CONFLICT(workspace_id) DO NOTHING;
 UPDATE artifact_storage_workspaces SET used_bytes=used_bytes+(coalesce(length(CAST(NEW.snapshot AS BLOB)),0)+coalesce(length(CAST(NEW.projection AS BLOB)),0)+coalesce(length(CAST(NEW.actor AS BLOB)),0)+coalesce(length(CAST(NEW.created_at AS BLOB)),0)+256) WHERE workspace_id=NEW.workspace_id;
 UPDATE artifact_storage_global SET used_bytes=used_bytes+(coalesce(length(CAST(NEW.snapshot AS BLOB)),0)+coalesce(length(CAST(NEW.projection AS BLOB)),0)+coalesce(length(CAST(NEW.actor AS BLOB)),0)+coalesce(length(CAST(NEW.created_at AS BLOB)),0)+256) WHERE id=1;
END;
CREATE TRIGGER document_checkpoints_storage_delete AFTER DELETE ON document_checkpoints
BEGIN
 UPDATE artifact_storage_workspaces SET used_bytes=max(0,used_bytes-(coalesce(length(CAST(OLD.snapshot AS BLOB)),0)+coalesce(length(CAST(OLD.projection AS BLOB)),0)+coalesce(length(CAST(OLD.actor AS BLOB)),0)+coalesce(length(CAST(OLD.created_at AS BLOB)),0)+256)) WHERE workspace_id=OLD.workspace_id;
 UPDATE artifact_storage_global SET used_bytes=max(0,used_bytes-(coalesce(length(CAST(OLD.snapshot AS BLOB)),0)+coalesce(length(CAST(OLD.projection AS BLOB)),0)+coalesce(length(CAST(OLD.actor AS BLOB)),0)+coalesce(length(CAST(OLD.created_at AS BLOB)),0)+256)) WHERE id=1;
END;
CREATE TRIGGER document_checkpoints_storage_update AFTER UPDATE ON document_checkpoints
BEGIN
 INSERT INTO artifact_storage_workspaces(workspace_id,max_bytes) SELECT NEW.workspace_id,workspace_max_bytes FROM artifact_storage_global WHERE id=1 AND NEW.workspace_id IS NOT NULL ON CONFLICT(workspace_id) DO NOTHING;
 UPDATE artifact_storage_workspaces SET used_bytes=max(0,used_bytes+(coalesce(length(CAST(NEW.snapshot AS BLOB)),0)+coalesce(length(CAST(NEW.projection AS BLOB)),0)+coalesce(length(CAST(NEW.actor AS BLOB)),0)+coalesce(length(CAST(NEW.created_at AS BLOB)),0)+256)-(coalesce(length(CAST(OLD.snapshot AS BLOB)),0)+coalesce(length(CAST(OLD.projection AS BLOB)),0)+coalesce(length(CAST(OLD.actor AS BLOB)),0)+coalesce(length(CAST(OLD.created_at AS BLOB)),0)+256)) WHERE workspace_id=NEW.workspace_id AND NEW.workspace_id=OLD.workspace_id;
 UPDATE artifact_storage_workspaces SET used_bytes=max(0,used_bytes-(coalesce(length(CAST(OLD.snapshot AS BLOB)),0)+coalesce(length(CAST(OLD.projection AS BLOB)),0)+coalesce(length(CAST(OLD.actor AS BLOB)),0)+coalesce(length(CAST(OLD.created_at AS BLOB)),0)+256)) WHERE workspace_id=OLD.workspace_id AND NEW.workspace_id<>OLD.workspace_id;
 UPDATE artifact_storage_workspaces SET used_bytes=used_bytes+(coalesce(length(CAST(NEW.snapshot AS BLOB)),0)+coalesce(length(CAST(NEW.projection AS BLOB)),0)+coalesce(length(CAST(NEW.actor AS BLOB)),0)+coalesce(length(CAST(NEW.created_at AS BLOB)),0)+256) WHERE workspace_id=NEW.workspace_id AND NEW.workspace_id<>OLD.workspace_id;
 UPDATE artifact_storage_global SET used_bytes=max(0,used_bytes+(coalesce(length(CAST(NEW.snapshot AS BLOB)),0)+coalesce(length(CAST(NEW.projection AS BLOB)),0)+coalesce(length(CAST(NEW.actor AS BLOB)),0)+coalesce(length(CAST(NEW.created_at AS BLOB)),0)+256)-(coalesce(length(CAST(OLD.snapshot AS BLOB)),0)+coalesce(length(CAST(OLD.projection AS BLOB)),0)+coalesce(length(CAST(OLD.actor AS BLOB)),0)+coalesce(length(CAST(OLD.created_at AS BLOB)),0)+256)) WHERE id=1;
END;

CREATE TRIGGER slides_heads_storage_insert AFTER INSERT ON slides_heads
BEGIN
 INSERT INTO artifact_storage_workspaces(workspace_id,max_bytes) SELECT NEW.workspace_id,workspace_max_bytes FROM artifact_storage_global WHERE id=1 AND NEW.workspace_id IS NOT NULL ON CONFLICT(workspace_id) DO NOTHING;
 UPDATE artifact_storage_workspaces SET used_bytes=used_bytes+(coalesce(length(CAST(NEW.canonical_json AS BLOB)),0)+coalesce(length(CAST(NEW.content_hash AS BLOB)),0)+256) WHERE workspace_id=NEW.workspace_id;
 UPDATE artifact_storage_global SET used_bytes=used_bytes+(coalesce(length(CAST(NEW.canonical_json AS BLOB)),0)+coalesce(length(CAST(NEW.content_hash AS BLOB)),0)+256) WHERE id=1;
END;
CREATE TRIGGER slides_heads_storage_delete AFTER DELETE ON slides_heads
BEGIN
 UPDATE artifact_storage_workspaces SET used_bytes=max(0,used_bytes-(coalesce(length(CAST(OLD.canonical_json AS BLOB)),0)+coalesce(length(CAST(OLD.content_hash AS BLOB)),0)+256)) WHERE workspace_id=OLD.workspace_id;
 UPDATE artifact_storage_global SET used_bytes=max(0,used_bytes-(coalesce(length(CAST(OLD.canonical_json AS BLOB)),0)+coalesce(length(CAST(OLD.content_hash AS BLOB)),0)+256)) WHERE id=1;
END;
CREATE TRIGGER slides_heads_storage_update AFTER UPDATE ON slides_heads
BEGIN
 INSERT INTO artifact_storage_workspaces(workspace_id,max_bytes) SELECT NEW.workspace_id,workspace_max_bytes FROM artifact_storage_global WHERE id=1 AND NEW.workspace_id IS NOT NULL ON CONFLICT(workspace_id) DO NOTHING;
 UPDATE artifact_storage_workspaces SET used_bytes=max(0,used_bytes+(coalesce(length(CAST(NEW.canonical_json AS BLOB)),0)+coalesce(length(CAST(NEW.content_hash AS BLOB)),0)+256)-(coalesce(length(CAST(OLD.canonical_json AS BLOB)),0)+coalesce(length(CAST(OLD.content_hash AS BLOB)),0)+256)) WHERE workspace_id=NEW.workspace_id AND NEW.workspace_id=OLD.workspace_id;
 UPDATE artifact_storage_workspaces SET used_bytes=max(0,used_bytes-(coalesce(length(CAST(OLD.canonical_json AS BLOB)),0)+coalesce(length(CAST(OLD.content_hash AS BLOB)),0)+256)) WHERE workspace_id=OLD.workspace_id AND NEW.workspace_id<>OLD.workspace_id;
 UPDATE artifact_storage_workspaces SET used_bytes=used_bytes+(coalesce(length(CAST(NEW.canonical_json AS BLOB)),0)+coalesce(length(CAST(NEW.content_hash AS BLOB)),0)+256) WHERE workspace_id=NEW.workspace_id AND NEW.workspace_id<>OLD.workspace_id;
 UPDATE artifact_storage_global SET used_bytes=max(0,used_bytes+(coalesce(length(CAST(NEW.canonical_json AS BLOB)),0)+coalesce(length(CAST(NEW.content_hash AS BLOB)),0)+256)-(coalesce(length(CAST(OLD.canonical_json AS BLOB)),0)+coalesce(length(CAST(OLD.content_hash AS BLOB)),0)+256)) WHERE id=1;
END;

CREATE TRIGGER slides_revisions_storage_insert AFTER INSERT ON slides_revisions
BEGIN
 INSERT INTO artifact_storage_workspaces(workspace_id,max_bytes) SELECT NEW.workspace_id,workspace_max_bytes FROM artifact_storage_global WHERE id=1 AND NEW.workspace_id IS NOT NULL ON CONFLICT(workspace_id) DO NOTHING;
 UPDATE artifact_storage_workspaces SET used_bytes=used_bytes+(coalesce(length(CAST(NEW.canonical_json AS BLOB)),0)+coalesce(length(CAST(NEW.content_hash AS BLOB)),0)+coalesce(length(CAST(NEW.actor AS BLOB)),0)+coalesce(length(CAST(NEW.kind AS BLOB)),0)+coalesce(length(CAST(NEW.mutation_id AS BLOB)),0)+coalesce(length(CAST(NEW.created_at AS BLOB)),0)+256) WHERE workspace_id=NEW.workspace_id;
 UPDATE artifact_storage_global SET used_bytes=used_bytes+(coalesce(length(CAST(NEW.canonical_json AS BLOB)),0)+coalesce(length(CAST(NEW.content_hash AS BLOB)),0)+coalesce(length(CAST(NEW.actor AS BLOB)),0)+coalesce(length(CAST(NEW.kind AS BLOB)),0)+coalesce(length(CAST(NEW.mutation_id AS BLOB)),0)+coalesce(length(CAST(NEW.created_at AS BLOB)),0)+256) WHERE id=1;
END;
CREATE TRIGGER slides_revisions_storage_delete AFTER DELETE ON slides_revisions
BEGIN
 UPDATE artifact_storage_workspaces SET used_bytes=max(0,used_bytes-(coalesce(length(CAST(OLD.canonical_json AS BLOB)),0)+coalesce(length(CAST(OLD.content_hash AS BLOB)),0)+coalesce(length(CAST(OLD.actor AS BLOB)),0)+coalesce(length(CAST(OLD.kind AS BLOB)),0)+coalesce(length(CAST(OLD.mutation_id AS BLOB)),0)+coalesce(length(CAST(OLD.created_at AS BLOB)),0)+256)) WHERE workspace_id=OLD.workspace_id;
 UPDATE artifact_storage_global SET used_bytes=max(0,used_bytes-(coalesce(length(CAST(OLD.canonical_json AS BLOB)),0)+coalesce(length(CAST(OLD.content_hash AS BLOB)),0)+coalesce(length(CAST(OLD.actor AS BLOB)),0)+coalesce(length(CAST(OLD.kind AS BLOB)),0)+coalesce(length(CAST(OLD.mutation_id AS BLOB)),0)+coalesce(length(CAST(OLD.created_at AS BLOB)),0)+256)) WHERE id=1;
END;
CREATE TRIGGER slides_revisions_storage_update AFTER UPDATE ON slides_revisions
BEGIN
 INSERT INTO artifact_storage_workspaces(workspace_id,max_bytes) SELECT NEW.workspace_id,workspace_max_bytes FROM artifact_storage_global WHERE id=1 AND NEW.workspace_id IS NOT NULL ON CONFLICT(workspace_id) DO NOTHING;
 UPDATE artifact_storage_workspaces SET used_bytes=max(0,used_bytes+(coalesce(length(CAST(NEW.canonical_json AS BLOB)),0)+coalesce(length(CAST(NEW.content_hash AS BLOB)),0)+coalesce(length(CAST(NEW.actor AS BLOB)),0)+coalesce(length(CAST(NEW.kind AS BLOB)),0)+coalesce(length(CAST(NEW.mutation_id AS BLOB)),0)+coalesce(length(CAST(NEW.created_at AS BLOB)),0)+256)-(coalesce(length(CAST(OLD.canonical_json AS BLOB)),0)+coalesce(length(CAST(OLD.content_hash AS BLOB)),0)+coalesce(length(CAST(OLD.actor AS BLOB)),0)+coalesce(length(CAST(OLD.kind AS BLOB)),0)+coalesce(length(CAST(OLD.mutation_id AS BLOB)),0)+coalesce(length(CAST(OLD.created_at AS BLOB)),0)+256)) WHERE workspace_id=NEW.workspace_id AND NEW.workspace_id=OLD.workspace_id;
 UPDATE artifact_storage_workspaces SET used_bytes=max(0,used_bytes-(coalesce(length(CAST(OLD.canonical_json AS BLOB)),0)+coalesce(length(CAST(OLD.content_hash AS BLOB)),0)+coalesce(length(CAST(OLD.actor AS BLOB)),0)+coalesce(length(CAST(OLD.kind AS BLOB)),0)+coalesce(length(CAST(OLD.mutation_id AS BLOB)),0)+coalesce(length(CAST(OLD.created_at AS BLOB)),0)+256)) WHERE workspace_id=OLD.workspace_id AND NEW.workspace_id<>OLD.workspace_id;
 UPDATE artifact_storage_workspaces SET used_bytes=used_bytes+(coalesce(length(CAST(NEW.canonical_json AS BLOB)),0)+coalesce(length(CAST(NEW.content_hash AS BLOB)),0)+coalesce(length(CAST(NEW.actor AS BLOB)),0)+coalesce(length(CAST(NEW.kind AS BLOB)),0)+coalesce(length(CAST(NEW.mutation_id AS BLOB)),0)+coalesce(length(CAST(NEW.created_at AS BLOB)),0)+256) WHERE workspace_id=NEW.workspace_id AND NEW.workspace_id<>OLD.workspace_id;
 UPDATE artifact_storage_global SET used_bytes=max(0,used_bytes+(coalesce(length(CAST(NEW.canonical_json AS BLOB)),0)+coalesce(length(CAST(NEW.content_hash AS BLOB)),0)+coalesce(length(CAST(NEW.actor AS BLOB)),0)+coalesce(length(CAST(NEW.kind AS BLOB)),0)+coalesce(length(CAST(NEW.mutation_id AS BLOB)),0)+coalesce(length(CAST(NEW.created_at AS BLOB)),0)+256)-(coalesce(length(CAST(OLD.canonical_json AS BLOB)),0)+coalesce(length(CAST(OLD.content_hash AS BLOB)),0)+coalesce(length(CAST(OLD.actor AS BLOB)),0)+coalesce(length(CAST(OLD.kind AS BLOB)),0)+coalesce(length(CAST(OLD.mutation_id AS BLOB)),0)+coalesce(length(CAST(OLD.created_at AS BLOB)),0)+256)) WHERE id=1;
END;

CREATE TRIGGER slides_receipts_storage_insert AFTER INSERT ON slides_receipts
BEGIN
 INSERT INTO artifact_storage_workspaces(workspace_id,max_bytes) SELECT NEW.workspace_id,workspace_max_bytes FROM artifact_storage_global WHERE id=1 AND NEW.workspace_id IS NOT NULL ON CONFLICT(workspace_id) DO NOTHING;
 UPDATE artifact_storage_workspaces SET used_bytes=used_bytes+(coalesce(length(CAST(NEW.request_hash AS BLOB)),0)+coalesce(length(CAST(NEW.actor AS BLOB)),0)+coalesce(length(CAST(NEW.kind AS BLOB)),0)+coalesce(length(CAST(NEW.content_hash AS BLOB)),0)+coalesce(length(CAST(NEW.mutation_id AS BLOB)),0)+256) WHERE workspace_id=NEW.workspace_id;
 UPDATE artifact_storage_global SET used_bytes=used_bytes+(coalesce(length(CAST(NEW.request_hash AS BLOB)),0)+coalesce(length(CAST(NEW.actor AS BLOB)),0)+coalesce(length(CAST(NEW.kind AS BLOB)),0)+coalesce(length(CAST(NEW.content_hash AS BLOB)),0)+coalesce(length(CAST(NEW.mutation_id AS BLOB)),0)+256) WHERE id=1;
END;
CREATE TRIGGER slides_receipts_storage_delete AFTER DELETE ON slides_receipts
BEGIN
 UPDATE artifact_storage_workspaces SET used_bytes=max(0,used_bytes-(coalesce(length(CAST(OLD.request_hash AS BLOB)),0)+coalesce(length(CAST(OLD.actor AS BLOB)),0)+coalesce(length(CAST(OLD.kind AS BLOB)),0)+coalesce(length(CAST(OLD.content_hash AS BLOB)),0)+coalesce(length(CAST(OLD.mutation_id AS BLOB)),0)+256)) WHERE workspace_id=OLD.workspace_id;
 UPDATE artifact_storage_global SET used_bytes=max(0,used_bytes-(coalesce(length(CAST(OLD.request_hash AS BLOB)),0)+coalesce(length(CAST(OLD.actor AS BLOB)),0)+coalesce(length(CAST(OLD.kind AS BLOB)),0)+coalesce(length(CAST(OLD.content_hash AS BLOB)),0)+coalesce(length(CAST(OLD.mutation_id AS BLOB)),0)+256)) WHERE id=1;
END;
CREATE TRIGGER slides_receipts_storage_update AFTER UPDATE ON slides_receipts
BEGIN
 INSERT INTO artifact_storage_workspaces(workspace_id,max_bytes) SELECT NEW.workspace_id,workspace_max_bytes FROM artifact_storage_global WHERE id=1 AND NEW.workspace_id IS NOT NULL ON CONFLICT(workspace_id) DO NOTHING;
 UPDATE artifact_storage_workspaces SET used_bytes=max(0,used_bytes+(coalesce(length(CAST(NEW.request_hash AS BLOB)),0)+coalesce(length(CAST(NEW.actor AS BLOB)),0)+coalesce(length(CAST(NEW.kind AS BLOB)),0)+coalesce(length(CAST(NEW.content_hash AS BLOB)),0)+coalesce(length(CAST(NEW.mutation_id AS BLOB)),0)+256)-(coalesce(length(CAST(OLD.request_hash AS BLOB)),0)+coalesce(length(CAST(OLD.actor AS BLOB)),0)+coalesce(length(CAST(OLD.kind AS BLOB)),0)+coalesce(length(CAST(OLD.content_hash AS BLOB)),0)+coalesce(length(CAST(OLD.mutation_id AS BLOB)),0)+256)) WHERE workspace_id=NEW.workspace_id AND NEW.workspace_id=OLD.workspace_id;
 UPDATE artifact_storage_workspaces SET used_bytes=max(0,used_bytes-(coalesce(length(CAST(OLD.request_hash AS BLOB)),0)+coalesce(length(CAST(OLD.actor AS BLOB)),0)+coalesce(length(CAST(OLD.kind AS BLOB)),0)+coalesce(length(CAST(OLD.content_hash AS BLOB)),0)+coalesce(length(CAST(OLD.mutation_id AS BLOB)),0)+256)) WHERE workspace_id=OLD.workspace_id AND NEW.workspace_id<>OLD.workspace_id;
 UPDATE artifact_storage_workspaces SET used_bytes=used_bytes+(coalesce(length(CAST(NEW.request_hash AS BLOB)),0)+coalesce(length(CAST(NEW.actor AS BLOB)),0)+coalesce(length(CAST(NEW.kind AS BLOB)),0)+coalesce(length(CAST(NEW.content_hash AS BLOB)),0)+coalesce(length(CAST(NEW.mutation_id AS BLOB)),0)+256) WHERE workspace_id=NEW.workspace_id AND NEW.workspace_id<>OLD.workspace_id;
 UPDATE artifact_storage_global SET used_bytes=max(0,used_bytes+(coalesce(length(CAST(NEW.request_hash AS BLOB)),0)+coalesce(length(CAST(NEW.actor AS BLOB)),0)+coalesce(length(CAST(NEW.kind AS BLOB)),0)+coalesce(length(CAST(NEW.content_hash AS BLOB)),0)+coalesce(length(CAST(NEW.mutation_id AS BLOB)),0)+256)-(coalesce(length(CAST(OLD.request_hash AS BLOB)),0)+coalesce(length(CAST(OLD.actor AS BLOB)),0)+coalesce(length(CAST(OLD.kind AS BLOB)),0)+coalesce(length(CAST(OLD.content_hash AS BLOB)),0)+coalesce(length(CAST(OLD.mutation_id AS BLOB)),0)+256)) WHERE id=1;
END;

CREATE TRIGGER slides_object_ids_storage_insert AFTER INSERT ON slides_object_ids
BEGIN
 INSERT INTO artifact_storage_workspaces(workspace_id,max_bytes) SELECT NEW.workspace_id,workspace_max_bytes FROM artifact_storage_global WHERE id=1 AND NEW.workspace_id IS NOT NULL ON CONFLICT(workspace_id) DO NOTHING;
 UPDATE artifact_storage_workspaces SET used_bytes=used_bytes+(coalesce(length(CAST(NEW.object_id AS BLOB)),0)+coalesce(length(CAST(NEW.object_kind AS BLOB)),0)+128) WHERE workspace_id=NEW.workspace_id;
 UPDATE artifact_storage_global SET used_bytes=used_bytes+(coalesce(length(CAST(NEW.object_id AS BLOB)),0)+coalesce(length(CAST(NEW.object_kind AS BLOB)),0)+128) WHERE id=1;
END;
CREATE TRIGGER slides_object_ids_storage_delete AFTER DELETE ON slides_object_ids
BEGIN
 UPDATE artifact_storage_workspaces SET used_bytes=max(0,used_bytes-(coalesce(length(CAST(OLD.object_id AS BLOB)),0)+coalesce(length(CAST(OLD.object_kind AS BLOB)),0)+128)) WHERE workspace_id=OLD.workspace_id;
 UPDATE artifact_storage_global SET used_bytes=max(0,used_bytes-(coalesce(length(CAST(OLD.object_id AS BLOB)),0)+coalesce(length(CAST(OLD.object_kind AS BLOB)),0)+128)) WHERE id=1;
END;
CREATE TRIGGER slides_object_ids_storage_update AFTER UPDATE ON slides_object_ids
BEGIN
 INSERT INTO artifact_storage_workspaces(workspace_id,max_bytes) SELECT NEW.workspace_id,workspace_max_bytes FROM artifact_storage_global WHERE id=1 AND NEW.workspace_id IS NOT NULL ON CONFLICT(workspace_id) DO NOTHING;
 UPDATE artifact_storage_workspaces SET used_bytes=max(0,used_bytes+(coalesce(length(CAST(NEW.object_id AS BLOB)),0)+coalesce(length(CAST(NEW.object_kind AS BLOB)),0)+128)-(coalesce(length(CAST(OLD.object_id AS BLOB)),0)+coalesce(length(CAST(OLD.object_kind AS BLOB)),0)+128)) WHERE workspace_id=NEW.workspace_id AND NEW.workspace_id=OLD.workspace_id;
 UPDATE artifact_storage_workspaces SET used_bytes=max(0,used_bytes-(coalesce(length(CAST(OLD.object_id AS BLOB)),0)+coalesce(length(CAST(OLD.object_kind AS BLOB)),0)+128)) WHERE workspace_id=OLD.workspace_id AND NEW.workspace_id<>OLD.workspace_id;
 UPDATE artifact_storage_workspaces SET used_bytes=used_bytes+(coalesce(length(CAST(NEW.object_id AS BLOB)),0)+coalesce(length(CAST(NEW.object_kind AS BLOB)),0)+128) WHERE workspace_id=NEW.workspace_id AND NEW.workspace_id<>OLD.workspace_id;
 UPDATE artifact_storage_global SET used_bytes=max(0,used_bytes+(coalesce(length(CAST(NEW.object_id AS BLOB)),0)+coalesce(length(CAST(NEW.object_kind AS BLOB)),0)+128)-(coalesce(length(CAST(OLD.object_id AS BLOB)),0)+coalesce(length(CAST(OLD.object_kind AS BLOB)),0)+128)) WHERE id=1;
END;

CREATE TRIGGER gridcraft_heads_storage_insert AFTER INSERT ON gridcraft_heads
BEGIN
 INSERT INTO artifact_storage_workspaces(workspace_id,max_bytes) SELECT NEW.workspace_id,workspace_max_bytes FROM artifact_storage_global WHERE id=1 AND NEW.workspace_id IS NOT NULL ON CONFLICT(workspace_id) DO NOTHING;
 UPDATE artifact_storage_workspaces SET used_bytes=used_bytes+(coalesce(length(CAST(NEW.snapshot AS BLOB)),0)+256) WHERE workspace_id=NEW.workspace_id;
 UPDATE artifact_storage_global SET used_bytes=used_bytes+(coalesce(length(CAST(NEW.snapshot AS BLOB)),0)+256) WHERE id=1;
END;
CREATE TRIGGER gridcraft_heads_storage_delete AFTER DELETE ON gridcraft_heads
BEGIN
 UPDATE artifact_storage_workspaces SET used_bytes=max(0,used_bytes-(coalesce(length(CAST(OLD.snapshot AS BLOB)),0)+256)) WHERE workspace_id=OLD.workspace_id;
 UPDATE artifact_storage_global SET used_bytes=max(0,used_bytes-(coalesce(length(CAST(OLD.snapshot AS BLOB)),0)+256)) WHERE id=1;
END;
CREATE TRIGGER gridcraft_heads_storage_update AFTER UPDATE ON gridcraft_heads
BEGIN
 INSERT INTO artifact_storage_workspaces(workspace_id,max_bytes) SELECT NEW.workspace_id,workspace_max_bytes FROM artifact_storage_global WHERE id=1 AND NEW.workspace_id IS NOT NULL ON CONFLICT(workspace_id) DO NOTHING;
 UPDATE artifact_storage_workspaces SET used_bytes=max(0,used_bytes+(coalesce(length(CAST(NEW.snapshot AS BLOB)),0)+256)-(coalesce(length(CAST(OLD.snapshot AS BLOB)),0)+256)) WHERE workspace_id=NEW.workspace_id AND NEW.workspace_id=OLD.workspace_id;
 UPDATE artifact_storage_workspaces SET used_bytes=max(0,used_bytes-(coalesce(length(CAST(OLD.snapshot AS BLOB)),0)+256)) WHERE workspace_id=OLD.workspace_id AND NEW.workspace_id<>OLD.workspace_id;
 UPDATE artifact_storage_workspaces SET used_bytes=used_bytes+(coalesce(length(CAST(NEW.snapshot AS BLOB)),0)+256) WHERE workspace_id=NEW.workspace_id AND NEW.workspace_id<>OLD.workspace_id;
 UPDATE artifact_storage_global SET used_bytes=max(0,used_bytes+(coalesce(length(CAST(NEW.snapshot AS BLOB)),0)+256)-(coalesce(length(CAST(OLD.snapshot AS BLOB)),0)+256)) WHERE id=1;
END;

CREATE TRIGGER gridcraft_operations_storage_insert AFTER INSERT ON gridcraft_operations
BEGIN
 INSERT INTO artifact_storage_workspaces(workspace_id,max_bytes) SELECT NEW.workspace_id,workspace_max_bytes FROM artifact_storage_global WHERE id=1 AND NEW.workspace_id IS NOT NULL ON CONFLICT(workspace_id) DO NOTHING;
 UPDATE artifact_storage_workspaces SET used_bytes=used_bytes+(coalesce(length(CAST(NEW.receipt AS BLOB)),0)+coalesce(length(CAST(NEW.authority_id AS BLOB)),0)+coalesce(length(CAST(NEW.mutation_id AS BLOB)),0)+coalesce(length(CAST(NEW.payload_hash AS BLOB)),0)+coalesce(length(CAST(NEW.state AS BLOB)),0)+256) WHERE workspace_id=NEW.workspace_id;
 UPDATE artifact_storage_global SET used_bytes=used_bytes+(coalesce(length(CAST(NEW.receipt AS BLOB)),0)+coalesce(length(CAST(NEW.authority_id AS BLOB)),0)+coalesce(length(CAST(NEW.mutation_id AS BLOB)),0)+coalesce(length(CAST(NEW.payload_hash AS BLOB)),0)+coalesce(length(CAST(NEW.state AS BLOB)),0)+256) WHERE id=1;
END;
CREATE TRIGGER gridcraft_operations_storage_delete AFTER DELETE ON gridcraft_operations
BEGIN
 UPDATE artifact_storage_workspaces SET used_bytes=max(0,used_bytes-(coalesce(length(CAST(OLD.receipt AS BLOB)),0)+coalesce(length(CAST(OLD.authority_id AS BLOB)),0)+coalesce(length(CAST(OLD.mutation_id AS BLOB)),0)+coalesce(length(CAST(OLD.payload_hash AS BLOB)),0)+coalesce(length(CAST(OLD.state AS BLOB)),0)+256)) WHERE workspace_id=OLD.workspace_id;
 UPDATE artifact_storage_global SET used_bytes=max(0,used_bytes-(coalesce(length(CAST(OLD.receipt AS BLOB)),0)+coalesce(length(CAST(OLD.authority_id AS BLOB)),0)+coalesce(length(CAST(OLD.mutation_id AS BLOB)),0)+coalesce(length(CAST(OLD.payload_hash AS BLOB)),0)+coalesce(length(CAST(OLD.state AS BLOB)),0)+256)) WHERE id=1;
END;
CREATE TRIGGER gridcraft_operations_storage_update AFTER UPDATE ON gridcraft_operations
BEGIN
 INSERT INTO artifact_storage_workspaces(workspace_id,max_bytes) SELECT NEW.workspace_id,workspace_max_bytes FROM artifact_storage_global WHERE id=1 AND NEW.workspace_id IS NOT NULL ON CONFLICT(workspace_id) DO NOTHING;
 UPDATE artifact_storage_workspaces SET used_bytes=max(0,used_bytes+(coalesce(length(CAST(NEW.receipt AS BLOB)),0)+coalesce(length(CAST(NEW.authority_id AS BLOB)),0)+coalesce(length(CAST(NEW.mutation_id AS BLOB)),0)+coalesce(length(CAST(NEW.payload_hash AS BLOB)),0)+coalesce(length(CAST(NEW.state AS BLOB)),0)+256)-(coalesce(length(CAST(OLD.receipt AS BLOB)),0)+coalesce(length(CAST(OLD.authority_id AS BLOB)),0)+coalesce(length(CAST(OLD.mutation_id AS BLOB)),0)+coalesce(length(CAST(OLD.payload_hash AS BLOB)),0)+coalesce(length(CAST(OLD.state AS BLOB)),0)+256)) WHERE workspace_id=NEW.workspace_id AND NEW.workspace_id=OLD.workspace_id;
 UPDATE artifact_storage_workspaces SET used_bytes=max(0,used_bytes-(coalesce(length(CAST(OLD.receipt AS BLOB)),0)+coalesce(length(CAST(OLD.authority_id AS BLOB)),0)+coalesce(length(CAST(OLD.mutation_id AS BLOB)),0)+coalesce(length(CAST(OLD.payload_hash AS BLOB)),0)+coalesce(length(CAST(OLD.state AS BLOB)),0)+256)) WHERE workspace_id=OLD.workspace_id AND NEW.workspace_id<>OLD.workspace_id;
 UPDATE artifact_storage_workspaces SET used_bytes=used_bytes+(coalesce(length(CAST(NEW.receipt AS BLOB)),0)+coalesce(length(CAST(NEW.authority_id AS BLOB)),0)+coalesce(length(CAST(NEW.mutation_id AS BLOB)),0)+coalesce(length(CAST(NEW.payload_hash AS BLOB)),0)+coalesce(length(CAST(NEW.state AS BLOB)),0)+256) WHERE workspace_id=NEW.workspace_id AND NEW.workspace_id<>OLD.workspace_id;
 UPDATE artifact_storage_global SET used_bytes=max(0,used_bytes+(coalesce(length(CAST(NEW.receipt AS BLOB)),0)+coalesce(length(CAST(NEW.authority_id AS BLOB)),0)+coalesce(length(CAST(NEW.mutation_id AS BLOB)),0)+coalesce(length(CAST(NEW.payload_hash AS BLOB)),0)+coalesce(length(CAST(NEW.state AS BLOB)),0)+256)-(coalesce(length(CAST(OLD.receipt AS BLOB)),0)+coalesce(length(CAST(OLD.authority_id AS BLOB)),0)+coalesce(length(CAST(OLD.mutation_id AS BLOB)),0)+coalesce(length(CAST(OLD.payload_hash AS BLOB)),0)+coalesce(length(CAST(OLD.state AS BLOB)),0)+256)) WHERE id=1;
END;

CREATE TRIGGER gridcraft_revisions_storage_insert AFTER INSERT ON gridcraft_revisions
BEGIN
 INSERT INTO artifact_storage_workspaces(workspace_id,max_bytes) SELECT coalesce((SELECT workspace_id FROM artifact_storage_pages WHERE page_id=NEW.page_id),(SELECT workspace_id FROM pages WHERE id=NEW.page_id)),workspace_max_bytes FROM artifact_storage_global WHERE id=1 AND coalesce((SELECT workspace_id FROM artifact_storage_pages WHERE page_id=NEW.page_id),(SELECT workspace_id FROM pages WHERE id=NEW.page_id)) IS NOT NULL ON CONFLICT(workspace_id) DO NOTHING;
 UPDATE artifact_storage_workspaces SET used_bytes=used_bytes+(coalesce(length(CAST(NEW.snapshot AS BLOB)),0)+128) WHERE workspace_id=coalesce((SELECT workspace_id FROM artifact_storage_pages WHERE page_id=NEW.page_id),(SELECT workspace_id FROM pages WHERE id=NEW.page_id));
 UPDATE artifact_storage_global SET used_bytes=used_bytes+(coalesce(length(CAST(NEW.snapshot AS BLOB)),0)+128) WHERE id=1;
END;
CREATE TRIGGER gridcraft_revisions_storage_delete AFTER DELETE ON gridcraft_revisions
BEGIN
 UPDATE artifact_storage_workspaces SET used_bytes=max(0,used_bytes-(coalesce(length(CAST(OLD.snapshot AS BLOB)),0)+128)) WHERE workspace_id=coalesce((SELECT workspace_id FROM artifact_storage_pages WHERE page_id=OLD.page_id),(SELECT workspace_id FROM pages WHERE id=OLD.page_id));
 UPDATE artifact_storage_global SET used_bytes=max(0,used_bytes-(coalesce(length(CAST(OLD.snapshot AS BLOB)),0)+128)) WHERE id=1;
END;
CREATE TRIGGER gridcraft_revisions_storage_update AFTER UPDATE ON gridcraft_revisions
BEGIN
 INSERT INTO artifact_storage_workspaces(workspace_id,max_bytes) SELECT coalesce((SELECT workspace_id FROM artifact_storage_pages WHERE page_id=NEW.page_id),(SELECT workspace_id FROM pages WHERE id=NEW.page_id)),workspace_max_bytes FROM artifact_storage_global WHERE id=1 AND coalesce((SELECT workspace_id FROM artifact_storage_pages WHERE page_id=NEW.page_id),(SELECT workspace_id FROM pages WHERE id=NEW.page_id)) IS NOT NULL ON CONFLICT(workspace_id) DO NOTHING;
 UPDATE artifact_storage_workspaces SET used_bytes=max(0,used_bytes+(coalesce(length(CAST(NEW.snapshot AS BLOB)),0)+128)-(coalesce(length(CAST(OLD.snapshot AS BLOB)),0)+128)) WHERE workspace_id=coalesce((SELECT workspace_id FROM artifact_storage_pages WHERE page_id=NEW.page_id),(SELECT workspace_id FROM pages WHERE id=NEW.page_id)) AND coalesce((SELECT workspace_id FROM artifact_storage_pages WHERE page_id=NEW.page_id),(SELECT workspace_id FROM pages WHERE id=NEW.page_id))=coalesce((SELECT workspace_id FROM artifact_storage_pages WHERE page_id=OLD.page_id),(SELECT workspace_id FROM pages WHERE id=OLD.page_id));
 UPDATE artifact_storage_workspaces SET used_bytes=max(0,used_bytes-(coalesce(length(CAST(OLD.snapshot AS BLOB)),0)+128)) WHERE workspace_id=coalesce((SELECT workspace_id FROM artifact_storage_pages WHERE page_id=OLD.page_id),(SELECT workspace_id FROM pages WHERE id=OLD.page_id)) AND coalesce((SELECT workspace_id FROM artifact_storage_pages WHERE page_id=NEW.page_id),(SELECT workspace_id FROM pages WHERE id=NEW.page_id))<>coalesce((SELECT workspace_id FROM artifact_storage_pages WHERE page_id=OLD.page_id),(SELECT workspace_id FROM pages WHERE id=OLD.page_id));
 UPDATE artifact_storage_workspaces SET used_bytes=used_bytes+(coalesce(length(CAST(NEW.snapshot AS BLOB)),0)+128) WHERE workspace_id=coalesce((SELECT workspace_id FROM artifact_storage_pages WHERE page_id=NEW.page_id),(SELECT workspace_id FROM pages WHERE id=NEW.page_id)) AND coalesce((SELECT workspace_id FROM artifact_storage_pages WHERE page_id=NEW.page_id),(SELECT workspace_id FROM pages WHERE id=NEW.page_id))<>coalesce((SELECT workspace_id FROM artifact_storage_pages WHERE page_id=OLD.page_id),(SELECT workspace_id FROM pages WHERE id=OLD.page_id));
 UPDATE artifact_storage_global SET used_bytes=max(0,used_bytes+(coalesce(length(CAST(NEW.snapshot AS BLOB)),0)+128)-(coalesce(length(CAST(OLD.snapshot AS BLOB)),0)+128)) WHERE id=1;
END;

CREATE TRIGGER gridcraft_audit_storage_insert AFTER INSERT ON gridcraft_audit
BEGIN
 INSERT INTO artifact_storage_workspaces(workspace_id,max_bytes) SELECT coalesce((SELECT workspace_id FROM artifact_storage_pages WHERE page_id=NEW.page_id),(SELECT workspace_id FROM pages WHERE id=NEW.page_id)),workspace_max_bytes FROM artifact_storage_global WHERE id=1 AND coalesce((SELECT workspace_id FROM artifact_storage_pages WHERE page_id=NEW.page_id),(SELECT workspace_id FROM pages WHERE id=NEW.page_id)) IS NOT NULL ON CONFLICT(workspace_id) DO NOTHING;
 UPDATE artifact_storage_workspaces SET used_bytes=used_bytes+(coalesce(length(CAST(NEW.receipt AS BLOB)),0)+coalesce(length(CAST(NEW.command AS BLOB)),0)+coalesce(length(CAST(NEW.writer AS BLOB)),0)+coalesce(length(CAST(NEW.authority_id AS BLOB)),0)+coalesce(length(CAST(NEW.mutation_id AS BLOB)),0)+coalesce(length(CAST(NEW.mandate_id AS BLOB)),0)+coalesce(length(CAST(NEW.result_hash AS BLOB)),0)+256) WHERE workspace_id=coalesce((SELECT workspace_id FROM artifact_storage_pages WHERE page_id=NEW.page_id),(SELECT workspace_id FROM pages WHERE id=NEW.page_id));
 UPDATE artifact_storage_global SET used_bytes=used_bytes+(coalesce(length(CAST(NEW.receipt AS BLOB)),0)+coalesce(length(CAST(NEW.command AS BLOB)),0)+coalesce(length(CAST(NEW.writer AS BLOB)),0)+coalesce(length(CAST(NEW.authority_id AS BLOB)),0)+coalesce(length(CAST(NEW.mutation_id AS BLOB)),0)+coalesce(length(CAST(NEW.mandate_id AS BLOB)),0)+coalesce(length(CAST(NEW.result_hash AS BLOB)),0)+256) WHERE id=1;
END;
CREATE TRIGGER gridcraft_audit_storage_delete AFTER DELETE ON gridcraft_audit
BEGIN
 UPDATE artifact_storage_workspaces SET used_bytes=max(0,used_bytes-(coalesce(length(CAST(OLD.receipt AS BLOB)),0)+coalesce(length(CAST(OLD.command AS BLOB)),0)+coalesce(length(CAST(OLD.writer AS BLOB)),0)+coalesce(length(CAST(OLD.authority_id AS BLOB)),0)+coalesce(length(CAST(OLD.mutation_id AS BLOB)),0)+coalesce(length(CAST(OLD.mandate_id AS BLOB)),0)+coalesce(length(CAST(OLD.result_hash AS BLOB)),0)+256)) WHERE workspace_id=coalesce((SELECT workspace_id FROM artifact_storage_pages WHERE page_id=OLD.page_id),(SELECT workspace_id FROM pages WHERE id=OLD.page_id));
 UPDATE artifact_storage_global SET used_bytes=max(0,used_bytes-(coalesce(length(CAST(OLD.receipt AS BLOB)),0)+coalesce(length(CAST(OLD.command AS BLOB)),0)+coalesce(length(CAST(OLD.writer AS BLOB)),0)+coalesce(length(CAST(OLD.authority_id AS BLOB)),0)+coalesce(length(CAST(OLD.mutation_id AS BLOB)),0)+coalesce(length(CAST(OLD.mandate_id AS BLOB)),0)+coalesce(length(CAST(OLD.result_hash AS BLOB)),0)+256)) WHERE id=1;
END;
CREATE TRIGGER gridcraft_audit_storage_update AFTER UPDATE ON gridcraft_audit
BEGIN
 INSERT INTO artifact_storage_workspaces(workspace_id,max_bytes) SELECT coalesce((SELECT workspace_id FROM artifact_storage_pages WHERE page_id=NEW.page_id),(SELECT workspace_id FROM pages WHERE id=NEW.page_id)),workspace_max_bytes FROM artifact_storage_global WHERE id=1 AND coalesce((SELECT workspace_id FROM artifact_storage_pages WHERE page_id=NEW.page_id),(SELECT workspace_id FROM pages WHERE id=NEW.page_id)) IS NOT NULL ON CONFLICT(workspace_id) DO NOTHING;
 UPDATE artifact_storage_workspaces SET used_bytes=max(0,used_bytes+(coalesce(length(CAST(NEW.receipt AS BLOB)),0)+coalesce(length(CAST(NEW.command AS BLOB)),0)+coalesce(length(CAST(NEW.writer AS BLOB)),0)+coalesce(length(CAST(NEW.authority_id AS BLOB)),0)+coalesce(length(CAST(NEW.mutation_id AS BLOB)),0)+coalesce(length(CAST(NEW.mandate_id AS BLOB)),0)+coalesce(length(CAST(NEW.result_hash AS BLOB)),0)+256)-(coalesce(length(CAST(OLD.receipt AS BLOB)),0)+coalesce(length(CAST(OLD.command AS BLOB)),0)+coalesce(length(CAST(OLD.writer AS BLOB)),0)+coalesce(length(CAST(OLD.authority_id AS BLOB)),0)+coalesce(length(CAST(OLD.mutation_id AS BLOB)),0)+coalesce(length(CAST(OLD.mandate_id AS BLOB)),0)+coalesce(length(CAST(OLD.result_hash AS BLOB)),0)+256)) WHERE workspace_id=coalesce((SELECT workspace_id FROM artifact_storage_pages WHERE page_id=NEW.page_id),(SELECT workspace_id FROM pages WHERE id=NEW.page_id)) AND coalesce((SELECT workspace_id FROM artifact_storage_pages WHERE page_id=NEW.page_id),(SELECT workspace_id FROM pages WHERE id=NEW.page_id))=coalesce((SELECT workspace_id FROM artifact_storage_pages WHERE page_id=OLD.page_id),(SELECT workspace_id FROM pages WHERE id=OLD.page_id));
 UPDATE artifact_storage_workspaces SET used_bytes=max(0,used_bytes-(coalesce(length(CAST(OLD.receipt AS BLOB)),0)+coalesce(length(CAST(OLD.command AS BLOB)),0)+coalesce(length(CAST(OLD.writer AS BLOB)),0)+coalesce(length(CAST(OLD.authority_id AS BLOB)),0)+coalesce(length(CAST(OLD.mutation_id AS BLOB)),0)+coalesce(length(CAST(OLD.mandate_id AS BLOB)),0)+coalesce(length(CAST(OLD.result_hash AS BLOB)),0)+256)) WHERE workspace_id=coalesce((SELECT workspace_id FROM artifact_storage_pages WHERE page_id=OLD.page_id),(SELECT workspace_id FROM pages WHERE id=OLD.page_id)) AND coalesce((SELECT workspace_id FROM artifact_storage_pages WHERE page_id=NEW.page_id),(SELECT workspace_id FROM pages WHERE id=NEW.page_id))<>coalesce((SELECT workspace_id FROM artifact_storage_pages WHERE page_id=OLD.page_id),(SELECT workspace_id FROM pages WHERE id=OLD.page_id));
 UPDATE artifact_storage_workspaces SET used_bytes=used_bytes+(coalesce(length(CAST(NEW.receipt AS BLOB)),0)+coalesce(length(CAST(NEW.command AS BLOB)),0)+coalesce(length(CAST(NEW.writer AS BLOB)),0)+coalesce(length(CAST(NEW.authority_id AS BLOB)),0)+coalesce(length(CAST(NEW.mutation_id AS BLOB)),0)+coalesce(length(CAST(NEW.mandate_id AS BLOB)),0)+coalesce(length(CAST(NEW.result_hash AS BLOB)),0)+256) WHERE workspace_id=coalesce((SELECT workspace_id FROM artifact_storage_pages WHERE page_id=NEW.page_id),(SELECT workspace_id FROM pages WHERE id=NEW.page_id)) AND coalesce((SELECT workspace_id FROM artifact_storage_pages WHERE page_id=NEW.page_id),(SELECT workspace_id FROM pages WHERE id=NEW.page_id))<>coalesce((SELECT workspace_id FROM artifact_storage_pages WHERE page_id=OLD.page_id),(SELECT workspace_id FROM pages WHERE id=OLD.page_id));
 UPDATE artifact_storage_global SET used_bytes=max(0,used_bytes+(coalesce(length(CAST(NEW.receipt AS BLOB)),0)+coalesce(length(CAST(NEW.command AS BLOB)),0)+coalesce(length(CAST(NEW.writer AS BLOB)),0)+coalesce(length(CAST(NEW.authority_id AS BLOB)),0)+coalesce(length(CAST(NEW.mutation_id AS BLOB)),0)+coalesce(length(CAST(NEW.mandate_id AS BLOB)),0)+coalesce(length(CAST(NEW.result_hash AS BLOB)),0)+256)-(coalesce(length(CAST(OLD.receipt AS BLOB)),0)+coalesce(length(CAST(OLD.command AS BLOB)),0)+coalesce(length(CAST(OLD.writer AS BLOB)),0)+coalesce(length(CAST(OLD.authority_id AS BLOB)),0)+coalesce(length(CAST(OLD.mutation_id AS BLOB)),0)+coalesce(length(CAST(OLD.mandate_id AS BLOB)),0)+coalesce(length(CAST(OLD.result_hash AS BLOB)),0)+256)) WHERE id=1;
END;

CREATE TRIGGER gridcraft_outbox_storage_insert AFTER INSERT ON gridcraft_outbox
BEGIN
 INSERT INTO artifact_storage_workspaces(workspace_id,max_bytes) SELECT coalesce((SELECT workspace_id FROM artifact_storage_pages WHERE page_id=NEW.page_id),(SELECT workspace_id FROM pages WHERE id=NEW.page_id)),workspace_max_bytes FROM artifact_storage_global WHERE id=1 AND coalesce((SELECT workspace_id FROM artifact_storage_pages WHERE page_id=NEW.page_id),(SELECT workspace_id FROM pages WHERE id=NEW.page_id)) IS NOT NULL ON CONFLICT(workspace_id) DO NOTHING;
 UPDATE artifact_storage_workspaces SET used_bytes=used_bytes+(coalesce(length(CAST(NEW.state AS BLOB)),0)+128) WHERE workspace_id=coalesce((SELECT workspace_id FROM artifact_storage_pages WHERE page_id=NEW.page_id),(SELECT workspace_id FROM pages WHERE id=NEW.page_id));
 UPDATE artifact_storage_global SET used_bytes=used_bytes+(coalesce(length(CAST(NEW.state AS BLOB)),0)+128) WHERE id=1;
END;
CREATE TRIGGER gridcraft_outbox_storage_delete AFTER DELETE ON gridcraft_outbox
BEGIN
 UPDATE artifact_storage_workspaces SET used_bytes=max(0,used_bytes-(coalesce(length(CAST(OLD.state AS BLOB)),0)+128)) WHERE workspace_id=coalesce((SELECT workspace_id FROM artifact_storage_pages WHERE page_id=OLD.page_id),(SELECT workspace_id FROM pages WHERE id=OLD.page_id));
 UPDATE artifact_storage_global SET used_bytes=max(0,used_bytes-(coalesce(length(CAST(OLD.state AS BLOB)),0)+128)) WHERE id=1;
END;
CREATE TRIGGER gridcraft_outbox_storage_update AFTER UPDATE ON gridcraft_outbox
BEGIN
 INSERT INTO artifact_storage_workspaces(workspace_id,max_bytes) SELECT coalesce((SELECT workspace_id FROM artifact_storage_pages WHERE page_id=NEW.page_id),(SELECT workspace_id FROM pages WHERE id=NEW.page_id)),workspace_max_bytes FROM artifact_storage_global WHERE id=1 AND coalesce((SELECT workspace_id FROM artifact_storage_pages WHERE page_id=NEW.page_id),(SELECT workspace_id FROM pages WHERE id=NEW.page_id)) IS NOT NULL ON CONFLICT(workspace_id) DO NOTHING;
 UPDATE artifact_storage_workspaces SET used_bytes=max(0,used_bytes+(coalesce(length(CAST(NEW.state AS BLOB)),0)+128)-(coalesce(length(CAST(OLD.state AS BLOB)),0)+128)) WHERE workspace_id=coalesce((SELECT workspace_id FROM artifact_storage_pages WHERE page_id=NEW.page_id),(SELECT workspace_id FROM pages WHERE id=NEW.page_id)) AND coalesce((SELECT workspace_id FROM artifact_storage_pages WHERE page_id=NEW.page_id),(SELECT workspace_id FROM pages WHERE id=NEW.page_id))=coalesce((SELECT workspace_id FROM artifact_storage_pages WHERE page_id=OLD.page_id),(SELECT workspace_id FROM pages WHERE id=OLD.page_id));
 UPDATE artifact_storage_workspaces SET used_bytes=max(0,used_bytes-(coalesce(length(CAST(OLD.state AS BLOB)),0)+128)) WHERE workspace_id=coalesce((SELECT workspace_id FROM artifact_storage_pages WHERE page_id=OLD.page_id),(SELECT workspace_id FROM pages WHERE id=OLD.page_id)) AND coalesce((SELECT workspace_id FROM artifact_storage_pages WHERE page_id=NEW.page_id),(SELECT workspace_id FROM pages WHERE id=NEW.page_id))<>coalesce((SELECT workspace_id FROM artifact_storage_pages WHERE page_id=OLD.page_id),(SELECT workspace_id FROM pages WHERE id=OLD.page_id));
 UPDATE artifact_storage_workspaces SET used_bytes=used_bytes+(coalesce(length(CAST(NEW.state AS BLOB)),0)+128) WHERE workspace_id=coalesce((SELECT workspace_id FROM artifact_storage_pages WHERE page_id=NEW.page_id),(SELECT workspace_id FROM pages WHERE id=NEW.page_id)) AND coalesce((SELECT workspace_id FROM artifact_storage_pages WHERE page_id=NEW.page_id),(SELECT workspace_id FROM pages WHERE id=NEW.page_id))<>coalesce((SELECT workspace_id FROM artifact_storage_pages WHERE page_id=OLD.page_id),(SELECT workspace_id FROM pages WHERE id=OLD.page_id));
 UPDATE artifact_storage_global SET used_bytes=max(0,used_bytes+(coalesce(length(CAST(NEW.state AS BLOB)),0)+128)-(coalesce(length(CAST(OLD.state AS BLOB)),0)+128)) WHERE id=1;
END;

CREATE TRIGGER mooring_documents_storage_insert AFTER INSERT ON mooring_documents
BEGIN
 INSERT INTO artifact_storage_workspaces(workspace_id,max_bytes) SELECT NEW.workspace_id,workspace_max_bytes FROM artifact_storage_global WHERE id=1 AND NEW.workspace_id IS NOT NULL ON CONFLICT(workspace_id) DO NOTHING;
 UPDATE artifact_storage_workspaces SET used_bytes=used_bytes+(coalesce(length(CAST(NEW.snapshot AS BLOB)),0)+coalesce(length(CAST(NEW.app_id AS BLOB)),0)+256) WHERE workspace_id=NEW.workspace_id;
 UPDATE artifact_storage_global SET used_bytes=used_bytes+(coalesce(length(CAST(NEW.snapshot AS BLOB)),0)+coalesce(length(CAST(NEW.app_id AS BLOB)),0)+256) WHERE id=1;
END;
CREATE TRIGGER mooring_documents_storage_delete AFTER DELETE ON mooring_documents
BEGIN
 UPDATE artifact_storage_workspaces SET used_bytes=max(0,used_bytes-(coalesce(length(CAST(OLD.snapshot AS BLOB)),0)+coalesce(length(CAST(OLD.app_id AS BLOB)),0)+256)) WHERE workspace_id=OLD.workspace_id;
 UPDATE artifact_storage_global SET used_bytes=max(0,used_bytes-(coalesce(length(CAST(OLD.snapshot AS BLOB)),0)+coalesce(length(CAST(OLD.app_id AS BLOB)),0)+256)) WHERE id=1;
END;
CREATE TRIGGER mooring_documents_storage_update AFTER UPDATE ON mooring_documents
BEGIN
 INSERT INTO artifact_storage_workspaces(workspace_id,max_bytes) SELECT NEW.workspace_id,workspace_max_bytes FROM artifact_storage_global WHERE id=1 AND NEW.workspace_id IS NOT NULL ON CONFLICT(workspace_id) DO NOTHING;
 UPDATE artifact_storage_workspaces SET used_bytes=max(0,used_bytes+(coalesce(length(CAST(NEW.snapshot AS BLOB)),0)+coalesce(length(CAST(NEW.app_id AS BLOB)),0)+256)-(coalesce(length(CAST(OLD.snapshot AS BLOB)),0)+coalesce(length(CAST(OLD.app_id AS BLOB)),0)+256)) WHERE workspace_id=NEW.workspace_id AND NEW.workspace_id=OLD.workspace_id;
 UPDATE artifact_storage_workspaces SET used_bytes=max(0,used_bytes-(coalesce(length(CAST(OLD.snapshot AS BLOB)),0)+coalesce(length(CAST(OLD.app_id AS BLOB)),0)+256)) WHERE workspace_id=OLD.workspace_id AND NEW.workspace_id<>OLD.workspace_id;
 UPDATE artifact_storage_workspaces SET used_bytes=used_bytes+(coalesce(length(CAST(NEW.snapshot AS BLOB)),0)+coalesce(length(CAST(NEW.app_id AS BLOB)),0)+256) WHERE workspace_id=NEW.workspace_id AND NEW.workspace_id<>OLD.workspace_id;
 UPDATE artifact_storage_global SET used_bytes=max(0,used_bytes+(coalesce(length(CAST(NEW.snapshot AS BLOB)),0)+coalesce(length(CAST(NEW.app_id AS BLOB)),0)+256)-(coalesce(length(CAST(OLD.snapshot AS BLOB)),0)+coalesce(length(CAST(OLD.app_id AS BLOB)),0)+256)) WHERE id=1;
END;

CREATE TRIGGER mooring_receipts_storage_insert AFTER INSERT ON mooring_receipts
BEGIN
 INSERT INTO artifact_storage_workspaces(workspace_id,max_bytes) SELECT coalesce((SELECT workspace_id FROM artifact_storage_pages WHERE page_id=NEW.page_id),(SELECT workspace_id FROM pages WHERE id=NEW.page_id)),workspace_max_bytes FROM artifact_storage_global WHERE id=1 AND coalesce((SELECT workspace_id FROM artifact_storage_pages WHERE page_id=NEW.page_id),(SELECT workspace_id FROM pages WHERE id=NEW.page_id)) IS NOT NULL ON CONFLICT(workspace_id) DO NOTHING;
 UPDATE artifact_storage_workspaces SET used_bytes=used_bytes+(coalesce(length(CAST(NEW.receipt AS BLOB)),0)+coalesce(length(CAST(NEW.snapshot AS BLOB)),0)+coalesce(length(CAST(NEW.fingerprint AS BLOB)),0)+coalesce(length(CAST(NEW.authority_id AS BLOB)),0)+coalesce(length(CAST(NEW.mutation_id AS BLOB)),0)+256) WHERE workspace_id=coalesce((SELECT workspace_id FROM artifact_storage_pages WHERE page_id=NEW.page_id),(SELECT workspace_id FROM pages WHERE id=NEW.page_id));
 UPDATE artifact_storage_global SET used_bytes=used_bytes+(coalesce(length(CAST(NEW.receipt AS BLOB)),0)+coalesce(length(CAST(NEW.snapshot AS BLOB)),0)+coalesce(length(CAST(NEW.fingerprint AS BLOB)),0)+coalesce(length(CAST(NEW.authority_id AS BLOB)),0)+coalesce(length(CAST(NEW.mutation_id AS BLOB)),0)+256) WHERE id=1;
END;
CREATE TRIGGER mooring_receipts_storage_delete AFTER DELETE ON mooring_receipts
BEGIN
 UPDATE artifact_storage_workspaces SET used_bytes=max(0,used_bytes-(coalesce(length(CAST(OLD.receipt AS BLOB)),0)+coalesce(length(CAST(OLD.snapshot AS BLOB)),0)+coalesce(length(CAST(OLD.fingerprint AS BLOB)),0)+coalesce(length(CAST(OLD.authority_id AS BLOB)),0)+coalesce(length(CAST(OLD.mutation_id AS BLOB)),0)+256)) WHERE workspace_id=coalesce((SELECT workspace_id FROM artifact_storage_pages WHERE page_id=OLD.page_id),(SELECT workspace_id FROM pages WHERE id=OLD.page_id));
 UPDATE artifact_storage_global SET used_bytes=max(0,used_bytes-(coalesce(length(CAST(OLD.receipt AS BLOB)),0)+coalesce(length(CAST(OLD.snapshot AS BLOB)),0)+coalesce(length(CAST(OLD.fingerprint AS BLOB)),0)+coalesce(length(CAST(OLD.authority_id AS BLOB)),0)+coalesce(length(CAST(OLD.mutation_id AS BLOB)),0)+256)) WHERE id=1;
END;
CREATE TRIGGER mooring_receipts_storage_update AFTER UPDATE ON mooring_receipts
BEGIN
 INSERT INTO artifact_storage_workspaces(workspace_id,max_bytes) SELECT coalesce((SELECT workspace_id FROM artifact_storage_pages WHERE page_id=NEW.page_id),(SELECT workspace_id FROM pages WHERE id=NEW.page_id)),workspace_max_bytes FROM artifact_storage_global WHERE id=1 AND coalesce((SELECT workspace_id FROM artifact_storage_pages WHERE page_id=NEW.page_id),(SELECT workspace_id FROM pages WHERE id=NEW.page_id)) IS NOT NULL ON CONFLICT(workspace_id) DO NOTHING;
 UPDATE artifact_storage_workspaces SET used_bytes=max(0,used_bytes+(coalesce(length(CAST(NEW.receipt AS BLOB)),0)+coalesce(length(CAST(NEW.snapshot AS BLOB)),0)+coalesce(length(CAST(NEW.fingerprint AS BLOB)),0)+coalesce(length(CAST(NEW.authority_id AS BLOB)),0)+coalesce(length(CAST(NEW.mutation_id AS BLOB)),0)+256)-(coalesce(length(CAST(OLD.receipt AS BLOB)),0)+coalesce(length(CAST(OLD.snapshot AS BLOB)),0)+coalesce(length(CAST(OLD.fingerprint AS BLOB)),0)+coalesce(length(CAST(OLD.authority_id AS BLOB)),0)+coalesce(length(CAST(OLD.mutation_id AS BLOB)),0)+256)) WHERE workspace_id=coalesce((SELECT workspace_id FROM artifact_storage_pages WHERE page_id=NEW.page_id),(SELECT workspace_id FROM pages WHERE id=NEW.page_id)) AND coalesce((SELECT workspace_id FROM artifact_storage_pages WHERE page_id=NEW.page_id),(SELECT workspace_id FROM pages WHERE id=NEW.page_id))=coalesce((SELECT workspace_id FROM artifact_storage_pages WHERE page_id=OLD.page_id),(SELECT workspace_id FROM pages WHERE id=OLD.page_id));
 UPDATE artifact_storage_workspaces SET used_bytes=max(0,used_bytes-(coalesce(length(CAST(OLD.receipt AS BLOB)),0)+coalesce(length(CAST(OLD.snapshot AS BLOB)),0)+coalesce(length(CAST(OLD.fingerprint AS BLOB)),0)+coalesce(length(CAST(OLD.authority_id AS BLOB)),0)+coalesce(length(CAST(OLD.mutation_id AS BLOB)),0)+256)) WHERE workspace_id=coalesce((SELECT workspace_id FROM artifact_storage_pages WHERE page_id=OLD.page_id),(SELECT workspace_id FROM pages WHERE id=OLD.page_id)) AND coalesce((SELECT workspace_id FROM artifact_storage_pages WHERE page_id=NEW.page_id),(SELECT workspace_id FROM pages WHERE id=NEW.page_id))<>coalesce((SELECT workspace_id FROM artifact_storage_pages WHERE page_id=OLD.page_id),(SELECT workspace_id FROM pages WHERE id=OLD.page_id));
 UPDATE artifact_storage_workspaces SET used_bytes=used_bytes+(coalesce(length(CAST(NEW.receipt AS BLOB)),0)+coalesce(length(CAST(NEW.snapshot AS BLOB)),0)+coalesce(length(CAST(NEW.fingerprint AS BLOB)),0)+coalesce(length(CAST(NEW.authority_id AS BLOB)),0)+coalesce(length(CAST(NEW.mutation_id AS BLOB)),0)+256) WHERE workspace_id=coalesce((SELECT workspace_id FROM artifact_storage_pages WHERE page_id=NEW.page_id),(SELECT workspace_id FROM pages WHERE id=NEW.page_id)) AND coalesce((SELECT workspace_id FROM artifact_storage_pages WHERE page_id=NEW.page_id),(SELECT workspace_id FROM pages WHERE id=NEW.page_id))<>coalesce((SELECT workspace_id FROM artifact_storage_pages WHERE page_id=OLD.page_id),(SELECT workspace_id FROM pages WHERE id=OLD.page_id));
 UPDATE artifact_storage_global SET used_bytes=max(0,used_bytes+(coalesce(length(CAST(NEW.receipt AS BLOB)),0)+coalesce(length(CAST(NEW.snapshot AS BLOB)),0)+coalesce(length(CAST(NEW.fingerprint AS BLOB)),0)+coalesce(length(CAST(NEW.authority_id AS BLOB)),0)+coalesce(length(CAST(NEW.mutation_id AS BLOB)),0)+256)-(coalesce(length(CAST(OLD.receipt AS BLOB)),0)+coalesce(length(CAST(OLD.snapshot AS BLOB)),0)+coalesce(length(CAST(OLD.fingerprint AS BLOB)),0)+coalesce(length(CAST(OLD.authority_id AS BLOB)),0)+coalesce(length(CAST(OLD.mutation_id AS BLOB)),0)+256)) WHERE id=1;
END;

CREATE TRIGGER mooring_collab_documents_storage_insert AFTER INSERT ON mooring_collab_documents
BEGIN
 INSERT INTO artifact_storage_workspaces(workspace_id,max_bytes) SELECT NEW.workspace_id,workspace_max_bytes FROM artifact_storage_global WHERE id=1 AND NEW.workspace_id IS NOT NULL ON CONFLICT(workspace_id) DO NOTHING;
 UPDATE artifact_storage_workspaces SET used_bytes=used_bytes+(coalesce(length(CAST(NEW.checkpoint_base64 AS BLOB)),0)+coalesce(length(CAST(NEW.revision AS BLOB)),0)+coalesce(length(CAST(NEW.application_id AS BLOB)),0)+256) WHERE workspace_id=NEW.workspace_id;
 UPDATE artifact_storage_global SET used_bytes=used_bytes+(coalesce(length(CAST(NEW.checkpoint_base64 AS BLOB)),0)+coalesce(length(CAST(NEW.revision AS BLOB)),0)+coalesce(length(CAST(NEW.application_id AS BLOB)),0)+256) WHERE id=1;
END;
CREATE TRIGGER mooring_collab_documents_storage_delete AFTER DELETE ON mooring_collab_documents
BEGIN
 UPDATE artifact_storage_workspaces SET used_bytes=max(0,used_bytes-(coalesce(length(CAST(OLD.checkpoint_base64 AS BLOB)),0)+coalesce(length(CAST(OLD.revision AS BLOB)),0)+coalesce(length(CAST(OLD.application_id AS BLOB)),0)+256)) WHERE workspace_id=OLD.workspace_id;
 UPDATE artifact_storage_global SET used_bytes=max(0,used_bytes-(coalesce(length(CAST(OLD.checkpoint_base64 AS BLOB)),0)+coalesce(length(CAST(OLD.revision AS BLOB)),0)+coalesce(length(CAST(OLD.application_id AS BLOB)),0)+256)) WHERE id=1;
END;
CREATE TRIGGER mooring_collab_documents_storage_update AFTER UPDATE ON mooring_collab_documents
BEGIN
 INSERT INTO artifact_storage_workspaces(workspace_id,max_bytes) SELECT NEW.workspace_id,workspace_max_bytes FROM artifact_storage_global WHERE id=1 AND NEW.workspace_id IS NOT NULL ON CONFLICT(workspace_id) DO NOTHING;
 UPDATE artifact_storage_workspaces SET used_bytes=max(0,used_bytes+(coalesce(length(CAST(NEW.checkpoint_base64 AS BLOB)),0)+coalesce(length(CAST(NEW.revision AS BLOB)),0)+coalesce(length(CAST(NEW.application_id AS BLOB)),0)+256)-(coalesce(length(CAST(OLD.checkpoint_base64 AS BLOB)),0)+coalesce(length(CAST(OLD.revision AS BLOB)),0)+coalesce(length(CAST(OLD.application_id AS BLOB)),0)+256)) WHERE workspace_id=NEW.workspace_id AND NEW.workspace_id=OLD.workspace_id;
 UPDATE artifact_storage_workspaces SET used_bytes=max(0,used_bytes-(coalesce(length(CAST(OLD.checkpoint_base64 AS BLOB)),0)+coalesce(length(CAST(OLD.revision AS BLOB)),0)+coalesce(length(CAST(OLD.application_id AS BLOB)),0)+256)) WHERE workspace_id=OLD.workspace_id AND NEW.workspace_id<>OLD.workspace_id;
 UPDATE artifact_storage_workspaces SET used_bytes=used_bytes+(coalesce(length(CAST(NEW.checkpoint_base64 AS BLOB)),0)+coalesce(length(CAST(NEW.revision AS BLOB)),0)+coalesce(length(CAST(NEW.application_id AS BLOB)),0)+256) WHERE workspace_id=NEW.workspace_id AND NEW.workspace_id<>OLD.workspace_id;
 UPDATE artifact_storage_global SET used_bytes=max(0,used_bytes+(coalesce(length(CAST(NEW.checkpoint_base64 AS BLOB)),0)+coalesce(length(CAST(NEW.revision AS BLOB)),0)+coalesce(length(CAST(NEW.application_id AS BLOB)),0)+256)-(coalesce(length(CAST(OLD.checkpoint_base64 AS BLOB)),0)+coalesce(length(CAST(OLD.revision AS BLOB)),0)+coalesce(length(CAST(OLD.application_id AS BLOB)),0)+256)) WHERE id=1;
END;

CREATE TRIGGER mooring_collab_changes_storage_insert AFTER INSERT ON mooring_collab_changes
BEGIN
 INSERT INTO artifact_storage_workspaces(workspace_id,max_bytes) SELECT NEW.workspace_id,workspace_max_bytes FROM artifact_storage_global WHERE id=1 AND NEW.workspace_id IS NOT NULL ON CONFLICT(workspace_id) DO NOTHING;
 UPDATE artifact_storage_workspaces SET used_bytes=used_bytes+(coalesce(length(CAST(NEW.result_json AS BLOB)),0)+coalesce(length(CAST(NEW.membership_hash AS BLOB)),0)+coalesce(length(CAST(NEW.task_id AS BLOB)),0)+256) WHERE workspace_id=NEW.workspace_id;
 UPDATE artifact_storage_global SET used_bytes=used_bytes+(coalesce(length(CAST(NEW.result_json AS BLOB)),0)+coalesce(length(CAST(NEW.membership_hash AS BLOB)),0)+coalesce(length(CAST(NEW.task_id AS BLOB)),0)+256) WHERE id=1;
END;
CREATE TRIGGER mooring_collab_changes_storage_delete AFTER DELETE ON mooring_collab_changes
BEGIN
 UPDATE artifact_storage_workspaces SET used_bytes=max(0,used_bytes-(coalesce(length(CAST(OLD.result_json AS BLOB)),0)+coalesce(length(CAST(OLD.membership_hash AS BLOB)),0)+coalesce(length(CAST(OLD.task_id AS BLOB)),0)+256)) WHERE workspace_id=OLD.workspace_id;
 UPDATE artifact_storage_global SET used_bytes=max(0,used_bytes-(coalesce(length(CAST(OLD.result_json AS BLOB)),0)+coalesce(length(CAST(OLD.membership_hash AS BLOB)),0)+coalesce(length(CAST(OLD.task_id AS BLOB)),0)+256)) WHERE id=1;
END;
CREATE TRIGGER mooring_collab_changes_storage_update AFTER UPDATE ON mooring_collab_changes
BEGIN
 INSERT INTO artifact_storage_workspaces(workspace_id,max_bytes) SELECT NEW.workspace_id,workspace_max_bytes FROM artifact_storage_global WHERE id=1 AND NEW.workspace_id IS NOT NULL ON CONFLICT(workspace_id) DO NOTHING;
 UPDATE artifact_storage_workspaces SET used_bytes=max(0,used_bytes+(coalesce(length(CAST(NEW.result_json AS BLOB)),0)+coalesce(length(CAST(NEW.membership_hash AS BLOB)),0)+coalesce(length(CAST(NEW.task_id AS BLOB)),0)+256)-(coalesce(length(CAST(OLD.result_json AS BLOB)),0)+coalesce(length(CAST(OLD.membership_hash AS BLOB)),0)+coalesce(length(CAST(OLD.task_id AS BLOB)),0)+256)) WHERE workspace_id=NEW.workspace_id AND NEW.workspace_id=OLD.workspace_id;
 UPDATE artifact_storage_workspaces SET used_bytes=max(0,used_bytes-(coalesce(length(CAST(OLD.result_json AS BLOB)),0)+coalesce(length(CAST(OLD.membership_hash AS BLOB)),0)+coalesce(length(CAST(OLD.task_id AS BLOB)),0)+256)) WHERE workspace_id=OLD.workspace_id AND NEW.workspace_id<>OLD.workspace_id;
 UPDATE artifact_storage_workspaces SET used_bytes=used_bytes+(coalesce(length(CAST(NEW.result_json AS BLOB)),0)+coalesce(length(CAST(NEW.membership_hash AS BLOB)),0)+coalesce(length(CAST(NEW.task_id AS BLOB)),0)+256) WHERE workspace_id=NEW.workspace_id AND NEW.workspace_id<>OLD.workspace_id;
 UPDATE artifact_storage_global SET used_bytes=max(0,used_bytes+(coalesce(length(CAST(NEW.result_json AS BLOB)),0)+coalesce(length(CAST(NEW.membership_hash AS BLOB)),0)+coalesce(length(CAST(NEW.task_id AS BLOB)),0)+256)-(coalesce(length(CAST(OLD.result_json AS BLOB)),0)+coalesce(length(CAST(OLD.membership_hash AS BLOB)),0)+coalesce(length(CAST(OLD.task_id AS BLOB)),0)+256)) WHERE id=1;
END;

CREATE TRIGGER mooring_collab_operations_storage_insert AFTER INSERT ON mooring_collab_operations
BEGIN
 INSERT INTO artifact_storage_workspaces(workspace_id,max_bytes) SELECT NEW.workspace_id,workspace_max_bytes FROM artifact_storage_global WHERE id=1 AND NEW.workspace_id IS NOT NULL ON CONFLICT(workspace_id) DO NOTHING;
 UPDATE artifact_storage_workspaces SET used_bytes=used_bytes+(coalesce(length(CAST(NEW.result_json AS BLOB)),0)+coalesce(length(CAST(NEW.fingerprint AS BLOB)),0)+coalesce(length(CAST(NEW.op_id AS BLOB)),0)+coalesce(length(CAST(NEW.change_id AS BLOB)),0)+coalesce(length(CAST(NEW.participant_ref AS BLOB)),0)+coalesce(length(CAST(NEW.dependencies_json AS BLOB)),0)+256) WHERE workspace_id=NEW.workspace_id;
 UPDATE artifact_storage_global SET used_bytes=used_bytes+(coalesce(length(CAST(NEW.result_json AS BLOB)),0)+coalesce(length(CAST(NEW.fingerprint AS BLOB)),0)+coalesce(length(CAST(NEW.op_id AS BLOB)),0)+coalesce(length(CAST(NEW.change_id AS BLOB)),0)+coalesce(length(CAST(NEW.participant_ref AS BLOB)),0)+coalesce(length(CAST(NEW.dependencies_json AS BLOB)),0)+256) WHERE id=1;
END;
CREATE TRIGGER mooring_collab_operations_storage_delete AFTER DELETE ON mooring_collab_operations
BEGIN
 UPDATE artifact_storage_workspaces SET used_bytes=max(0,used_bytes-(coalesce(length(CAST(OLD.result_json AS BLOB)),0)+coalesce(length(CAST(OLD.fingerprint AS BLOB)),0)+coalesce(length(CAST(OLD.op_id AS BLOB)),0)+coalesce(length(CAST(OLD.change_id AS BLOB)),0)+coalesce(length(CAST(OLD.participant_ref AS BLOB)),0)+coalesce(length(CAST(OLD.dependencies_json AS BLOB)),0)+256)) WHERE workspace_id=OLD.workspace_id;
 UPDATE artifact_storage_global SET used_bytes=max(0,used_bytes-(coalesce(length(CAST(OLD.result_json AS BLOB)),0)+coalesce(length(CAST(OLD.fingerprint AS BLOB)),0)+coalesce(length(CAST(OLD.op_id AS BLOB)),0)+coalesce(length(CAST(OLD.change_id AS BLOB)),0)+coalesce(length(CAST(OLD.participant_ref AS BLOB)),0)+coalesce(length(CAST(OLD.dependencies_json AS BLOB)),0)+256)) WHERE id=1;
END;
CREATE TRIGGER mooring_collab_operations_storage_update AFTER UPDATE ON mooring_collab_operations
BEGIN
 INSERT INTO artifact_storage_workspaces(workspace_id,max_bytes) SELECT NEW.workspace_id,workspace_max_bytes FROM artifact_storage_global WHERE id=1 AND NEW.workspace_id IS NOT NULL ON CONFLICT(workspace_id) DO NOTHING;
 UPDATE artifact_storage_workspaces SET used_bytes=max(0,used_bytes+(coalesce(length(CAST(NEW.result_json AS BLOB)),0)+coalesce(length(CAST(NEW.fingerprint AS BLOB)),0)+coalesce(length(CAST(NEW.op_id AS BLOB)),0)+coalesce(length(CAST(NEW.change_id AS BLOB)),0)+coalesce(length(CAST(NEW.participant_ref AS BLOB)),0)+coalesce(length(CAST(NEW.dependencies_json AS BLOB)),0)+256)-(coalesce(length(CAST(OLD.result_json AS BLOB)),0)+coalesce(length(CAST(OLD.fingerprint AS BLOB)),0)+coalesce(length(CAST(OLD.op_id AS BLOB)),0)+coalesce(length(CAST(OLD.change_id AS BLOB)),0)+coalesce(length(CAST(OLD.participant_ref AS BLOB)),0)+coalesce(length(CAST(OLD.dependencies_json AS BLOB)),0)+256)) WHERE workspace_id=NEW.workspace_id AND NEW.workspace_id=OLD.workspace_id;
 UPDATE artifact_storage_workspaces SET used_bytes=max(0,used_bytes-(coalesce(length(CAST(OLD.result_json AS BLOB)),0)+coalesce(length(CAST(OLD.fingerprint AS BLOB)),0)+coalesce(length(CAST(OLD.op_id AS BLOB)),0)+coalesce(length(CAST(OLD.change_id AS BLOB)),0)+coalesce(length(CAST(OLD.participant_ref AS BLOB)),0)+coalesce(length(CAST(OLD.dependencies_json AS BLOB)),0)+256)) WHERE workspace_id=OLD.workspace_id AND NEW.workspace_id<>OLD.workspace_id;
 UPDATE artifact_storage_workspaces SET used_bytes=used_bytes+(coalesce(length(CAST(NEW.result_json AS BLOB)),0)+coalesce(length(CAST(NEW.fingerprint AS BLOB)),0)+coalesce(length(CAST(NEW.op_id AS BLOB)),0)+coalesce(length(CAST(NEW.change_id AS BLOB)),0)+coalesce(length(CAST(NEW.participant_ref AS BLOB)),0)+coalesce(length(CAST(NEW.dependencies_json AS BLOB)),0)+256) WHERE workspace_id=NEW.workspace_id AND NEW.workspace_id<>OLD.workspace_id;
 UPDATE artifact_storage_global SET used_bytes=max(0,used_bytes+(coalesce(length(CAST(NEW.result_json AS BLOB)),0)+coalesce(length(CAST(NEW.fingerprint AS BLOB)),0)+coalesce(length(CAST(NEW.op_id AS BLOB)),0)+coalesce(length(CAST(NEW.change_id AS BLOB)),0)+coalesce(length(CAST(NEW.participant_ref AS BLOB)),0)+coalesce(length(CAST(NEW.dependencies_json AS BLOB)),0)+256)-(coalesce(length(CAST(OLD.result_json AS BLOB)),0)+coalesce(length(CAST(OLD.fingerprint AS BLOB)),0)+coalesce(length(CAST(OLD.op_id AS BLOB)),0)+coalesce(length(CAST(OLD.change_id AS BLOB)),0)+coalesce(length(CAST(OLD.participant_ref AS BLOB)),0)+coalesce(length(CAST(OLD.dependencies_json AS BLOB)),0)+256)) WHERE id=1;
END;

CREATE TRIGGER mooring_collab_checkpoints_storage_insert AFTER INSERT ON mooring_collab_checkpoints
BEGIN
 INSERT INTO artifact_storage_workspaces(workspace_id,max_bytes) SELECT NEW.workspace_id,workspace_max_bytes FROM artifact_storage_global WHERE id=1 AND NEW.workspace_id IS NOT NULL ON CONFLICT(workspace_id) DO NOTHING;
 UPDATE artifact_storage_workspaces SET used_bytes=used_bytes+(coalesce(length(CAST(NEW.checkpoint_base64 AS BLOB)),0)+coalesce(length(CAST(NEW.revision AS BLOB)),0)+256) WHERE workspace_id=NEW.workspace_id;
 UPDATE artifact_storage_global SET used_bytes=used_bytes+(coalesce(length(CAST(NEW.checkpoint_base64 AS BLOB)),0)+coalesce(length(CAST(NEW.revision AS BLOB)),0)+256) WHERE id=1;
END;
CREATE TRIGGER mooring_collab_checkpoints_storage_delete AFTER DELETE ON mooring_collab_checkpoints
BEGIN
 UPDATE artifact_storage_workspaces SET used_bytes=max(0,used_bytes-(coalesce(length(CAST(OLD.checkpoint_base64 AS BLOB)),0)+coalesce(length(CAST(OLD.revision AS BLOB)),0)+256)) WHERE workspace_id=OLD.workspace_id;
 UPDATE artifact_storage_global SET used_bytes=max(0,used_bytes-(coalesce(length(CAST(OLD.checkpoint_base64 AS BLOB)),0)+coalesce(length(CAST(OLD.revision AS BLOB)),0)+256)) WHERE id=1;
END;
CREATE TRIGGER mooring_collab_checkpoints_storage_update AFTER UPDATE ON mooring_collab_checkpoints
BEGIN
 INSERT INTO artifact_storage_workspaces(workspace_id,max_bytes) SELECT NEW.workspace_id,workspace_max_bytes FROM artifact_storage_global WHERE id=1 AND NEW.workspace_id IS NOT NULL ON CONFLICT(workspace_id) DO NOTHING;
 UPDATE artifact_storage_workspaces SET used_bytes=max(0,used_bytes+(coalesce(length(CAST(NEW.checkpoint_base64 AS BLOB)),0)+coalesce(length(CAST(NEW.revision AS BLOB)),0)+256)-(coalesce(length(CAST(OLD.checkpoint_base64 AS BLOB)),0)+coalesce(length(CAST(OLD.revision AS BLOB)),0)+256)) WHERE workspace_id=NEW.workspace_id AND NEW.workspace_id=OLD.workspace_id;
 UPDATE artifact_storage_workspaces SET used_bytes=max(0,used_bytes-(coalesce(length(CAST(OLD.checkpoint_base64 AS BLOB)),0)+coalesce(length(CAST(OLD.revision AS BLOB)),0)+256)) WHERE workspace_id=OLD.workspace_id AND NEW.workspace_id<>OLD.workspace_id;
 UPDATE artifact_storage_workspaces SET used_bytes=used_bytes+(coalesce(length(CAST(NEW.checkpoint_base64 AS BLOB)),0)+coalesce(length(CAST(NEW.revision AS BLOB)),0)+256) WHERE workspace_id=NEW.workspace_id AND NEW.workspace_id<>OLD.workspace_id;
 UPDATE artifact_storage_global SET used_bytes=max(0,used_bytes+(coalesce(length(CAST(NEW.checkpoint_base64 AS BLOB)),0)+coalesce(length(CAST(NEW.revision AS BLOB)),0)+256)-(coalesce(length(CAST(OLD.checkpoint_base64 AS BLOB)),0)+coalesce(length(CAST(OLD.revision AS BLOB)),0)+256)) WHERE id=1;
END;

CREATE TRIGGER mooring_collab_audit_storage_insert AFTER INSERT ON mooring_collab_audit
BEGIN
 INSERT INTO artifact_storage_workspaces(workspace_id,max_bytes) SELECT NEW.workspace_id,workspace_max_bytes FROM artifact_storage_global WHERE id=1 AND NEW.workspace_id IS NOT NULL ON CONFLICT(workspace_id) DO NOTHING;
 UPDATE artifact_storage_workspaces SET used_bytes=used_bytes+(coalesce(length(CAST(NEW.detail_json AS BLOB)),0)+coalesce(length(CAST(NEW.kind AS BLOB)),0)+coalesce(length(CAST(NEW.id AS BLOB)),0)+256) WHERE workspace_id=NEW.workspace_id;
 UPDATE artifact_storage_global SET used_bytes=used_bytes+(coalesce(length(CAST(NEW.detail_json AS BLOB)),0)+coalesce(length(CAST(NEW.kind AS BLOB)),0)+coalesce(length(CAST(NEW.id AS BLOB)),0)+256) WHERE id=1;
END;
CREATE TRIGGER mooring_collab_audit_storage_delete AFTER DELETE ON mooring_collab_audit
BEGIN
 UPDATE artifact_storage_workspaces SET used_bytes=max(0,used_bytes-(coalesce(length(CAST(OLD.detail_json AS BLOB)),0)+coalesce(length(CAST(OLD.kind AS BLOB)),0)+coalesce(length(CAST(OLD.id AS BLOB)),0)+256)) WHERE workspace_id=OLD.workspace_id;
 UPDATE artifact_storage_global SET used_bytes=max(0,used_bytes-(coalesce(length(CAST(OLD.detail_json AS BLOB)),0)+coalesce(length(CAST(OLD.kind AS BLOB)),0)+coalesce(length(CAST(OLD.id AS BLOB)),0)+256)) WHERE id=1;
END;
CREATE TRIGGER mooring_collab_audit_storage_update AFTER UPDATE ON mooring_collab_audit
BEGIN
 INSERT INTO artifact_storage_workspaces(workspace_id,max_bytes) SELECT NEW.workspace_id,workspace_max_bytes FROM artifact_storage_global WHERE id=1 AND NEW.workspace_id IS NOT NULL ON CONFLICT(workspace_id) DO NOTHING;
 UPDATE artifact_storage_workspaces SET used_bytes=max(0,used_bytes+(coalesce(length(CAST(NEW.detail_json AS BLOB)),0)+coalesce(length(CAST(NEW.kind AS BLOB)),0)+coalesce(length(CAST(NEW.id AS BLOB)),0)+256)-(coalesce(length(CAST(OLD.detail_json AS BLOB)),0)+coalesce(length(CAST(OLD.kind AS BLOB)),0)+coalesce(length(CAST(OLD.id AS BLOB)),0)+256)) WHERE workspace_id=NEW.workspace_id AND NEW.workspace_id=OLD.workspace_id;
 UPDATE artifact_storage_workspaces SET used_bytes=max(0,used_bytes-(coalesce(length(CAST(OLD.detail_json AS BLOB)),0)+coalesce(length(CAST(OLD.kind AS BLOB)),0)+coalesce(length(CAST(OLD.id AS BLOB)),0)+256)) WHERE workspace_id=OLD.workspace_id AND NEW.workspace_id<>OLD.workspace_id;
 UPDATE artifact_storage_workspaces SET used_bytes=used_bytes+(coalesce(length(CAST(NEW.detail_json AS BLOB)),0)+coalesce(length(CAST(NEW.kind AS BLOB)),0)+coalesce(length(CAST(NEW.id AS BLOB)),0)+256) WHERE workspace_id=NEW.workspace_id AND NEW.workspace_id<>OLD.workspace_id;
 UPDATE artifact_storage_global SET used_bytes=max(0,used_bytes+(coalesce(length(CAST(NEW.detail_json AS BLOB)),0)+coalesce(length(CAST(NEW.kind AS BLOB)),0)+coalesce(length(CAST(NEW.id AS BLOB)),0)+256)-(coalesce(length(CAST(OLD.detail_json AS BLOB)),0)+coalesce(length(CAST(OLD.kind AS BLOB)),0)+coalesce(length(CAST(OLD.id AS BLOB)),0)+256)) WHERE id=1;
END;

CREATE TRIGGER mooring_collab_tasks_storage_insert AFTER INSERT ON mooring_collab_tasks
BEGIN
 INSERT INTO artifact_storage_workspaces(workspace_id,max_bytes) SELECT NEW.workspace_id,workspace_max_bytes FROM artifact_storage_global WHERE id=1 AND NEW.workspace_id IS NOT NULL ON CONFLICT(workspace_id) DO NOTHING;
 UPDATE artifact_storage_workspaces SET used_bytes=used_bytes+(coalesce(length(CAST(NEW.participant_ref AS BLOB)),0)+coalesce(length(CAST(NEW.status AS BLOB)),0)+coalesce(length(CAST(NEW.task_id AS BLOB)),0)+256) WHERE workspace_id=NEW.workspace_id;
 UPDATE artifact_storage_global SET used_bytes=used_bytes+(coalesce(length(CAST(NEW.participant_ref AS BLOB)),0)+coalesce(length(CAST(NEW.status AS BLOB)),0)+coalesce(length(CAST(NEW.task_id AS BLOB)),0)+256) WHERE id=1;
END;
CREATE TRIGGER mooring_collab_tasks_storage_delete AFTER DELETE ON mooring_collab_tasks
BEGIN
 UPDATE artifact_storage_workspaces SET used_bytes=max(0,used_bytes-(coalesce(length(CAST(OLD.participant_ref AS BLOB)),0)+coalesce(length(CAST(OLD.status AS BLOB)),0)+coalesce(length(CAST(OLD.task_id AS BLOB)),0)+256)) WHERE workspace_id=OLD.workspace_id;
 UPDATE artifact_storage_global SET used_bytes=max(0,used_bytes-(coalesce(length(CAST(OLD.participant_ref AS BLOB)),0)+coalesce(length(CAST(OLD.status AS BLOB)),0)+coalesce(length(CAST(OLD.task_id AS BLOB)),0)+256)) WHERE id=1;
END;
CREATE TRIGGER mooring_collab_tasks_storage_update AFTER UPDATE ON mooring_collab_tasks
BEGIN
 INSERT INTO artifact_storage_workspaces(workspace_id,max_bytes) SELECT NEW.workspace_id,workspace_max_bytes FROM artifact_storage_global WHERE id=1 AND NEW.workspace_id IS NOT NULL ON CONFLICT(workspace_id) DO NOTHING;
 UPDATE artifact_storage_workspaces SET used_bytes=max(0,used_bytes+(coalesce(length(CAST(NEW.participant_ref AS BLOB)),0)+coalesce(length(CAST(NEW.status AS BLOB)),0)+coalesce(length(CAST(NEW.task_id AS BLOB)),0)+256)-(coalesce(length(CAST(OLD.participant_ref AS BLOB)),0)+coalesce(length(CAST(OLD.status AS BLOB)),0)+coalesce(length(CAST(OLD.task_id AS BLOB)),0)+256)) WHERE workspace_id=NEW.workspace_id AND NEW.workspace_id=OLD.workspace_id;
 UPDATE artifact_storage_workspaces SET used_bytes=max(0,used_bytes-(coalesce(length(CAST(OLD.participant_ref AS BLOB)),0)+coalesce(length(CAST(OLD.status AS BLOB)),0)+coalesce(length(CAST(OLD.task_id AS BLOB)),0)+256)) WHERE workspace_id=OLD.workspace_id AND NEW.workspace_id<>OLD.workspace_id;
 UPDATE artifact_storage_workspaces SET used_bytes=used_bytes+(coalesce(length(CAST(NEW.participant_ref AS BLOB)),0)+coalesce(length(CAST(NEW.status AS BLOB)),0)+coalesce(length(CAST(NEW.task_id AS BLOB)),0)+256) WHERE workspace_id=NEW.workspace_id AND NEW.workspace_id<>OLD.workspace_id;
 UPDATE artifact_storage_global SET used_bytes=max(0,used_bytes+(coalesce(length(CAST(NEW.participant_ref AS BLOB)),0)+coalesce(length(CAST(NEW.status AS BLOB)),0)+coalesce(length(CAST(NEW.task_id AS BLOB)),0)+256)-(coalesce(length(CAST(OLD.participant_ref AS BLOB)),0)+coalesce(length(CAST(OLD.status AS BLOB)),0)+coalesce(length(CAST(OLD.task_id AS BLOB)),0)+256)) WHERE id=1;
END;

CREATE TRIGGER mooring_collab_target_versions_storage_insert AFTER INSERT ON mooring_collab_target_versions
BEGIN
 INSERT INTO artifact_storage_workspaces(workspace_id,max_bytes) SELECT NEW.workspace_id,workspace_max_bytes FROM artifact_storage_global WHERE id=1 AND NEW.workspace_id IS NOT NULL ON CONFLICT(workspace_id) DO NOTHING;
 UPDATE artifact_storage_workspaces SET used_bytes=used_bytes+(coalesce(length(CAST(NEW.target_id AS BLOB)),0)+coalesce(length(CAST(NEW.revision AS BLOB)),0)+coalesce(length(CAST(NEW.structure_revision AS BLOB)),0)+256) WHERE workspace_id=NEW.workspace_id;
 UPDATE artifact_storage_global SET used_bytes=used_bytes+(coalesce(length(CAST(NEW.target_id AS BLOB)),0)+coalesce(length(CAST(NEW.revision AS BLOB)),0)+coalesce(length(CAST(NEW.structure_revision AS BLOB)),0)+256) WHERE id=1;
END;
CREATE TRIGGER mooring_collab_target_versions_storage_delete AFTER DELETE ON mooring_collab_target_versions
BEGIN
 UPDATE artifact_storage_workspaces SET used_bytes=max(0,used_bytes-(coalesce(length(CAST(OLD.target_id AS BLOB)),0)+coalesce(length(CAST(OLD.revision AS BLOB)),0)+coalesce(length(CAST(OLD.structure_revision AS BLOB)),0)+256)) WHERE workspace_id=OLD.workspace_id;
 UPDATE artifact_storage_global SET used_bytes=max(0,used_bytes-(coalesce(length(CAST(OLD.target_id AS BLOB)),0)+coalesce(length(CAST(OLD.revision AS BLOB)),0)+coalesce(length(CAST(OLD.structure_revision AS BLOB)),0)+256)) WHERE id=1;
END;
CREATE TRIGGER mooring_collab_target_versions_storage_update AFTER UPDATE ON mooring_collab_target_versions
BEGIN
 INSERT INTO artifact_storage_workspaces(workspace_id,max_bytes) SELECT NEW.workspace_id,workspace_max_bytes FROM artifact_storage_global WHERE id=1 AND NEW.workspace_id IS NOT NULL ON CONFLICT(workspace_id) DO NOTHING;
 UPDATE artifact_storage_workspaces SET used_bytes=max(0,used_bytes+(coalesce(length(CAST(NEW.target_id AS BLOB)),0)+coalesce(length(CAST(NEW.revision AS BLOB)),0)+coalesce(length(CAST(NEW.structure_revision AS BLOB)),0)+256)-(coalesce(length(CAST(OLD.target_id AS BLOB)),0)+coalesce(length(CAST(OLD.revision AS BLOB)),0)+coalesce(length(CAST(OLD.structure_revision AS BLOB)),0)+256)) WHERE workspace_id=NEW.workspace_id AND NEW.workspace_id=OLD.workspace_id;
 UPDATE artifact_storage_workspaces SET used_bytes=max(0,used_bytes-(coalesce(length(CAST(OLD.target_id AS BLOB)),0)+coalesce(length(CAST(OLD.revision AS BLOB)),0)+coalesce(length(CAST(OLD.structure_revision AS BLOB)),0)+256)) WHERE workspace_id=OLD.workspace_id AND NEW.workspace_id<>OLD.workspace_id;
 UPDATE artifact_storage_workspaces SET used_bytes=used_bytes+(coalesce(length(CAST(NEW.target_id AS BLOB)),0)+coalesce(length(CAST(NEW.revision AS BLOB)),0)+coalesce(length(CAST(NEW.structure_revision AS BLOB)),0)+256) WHERE workspace_id=NEW.workspace_id AND NEW.workspace_id<>OLD.workspace_id;
 UPDATE artifact_storage_global SET used_bytes=max(0,used_bytes+(coalesce(length(CAST(NEW.target_id AS BLOB)),0)+coalesce(length(CAST(NEW.revision AS BLOB)),0)+coalesce(length(CAST(NEW.structure_revision AS BLOB)),0)+256)-(coalesce(length(CAST(OLD.target_id AS BLOB)),0)+coalesce(length(CAST(OLD.revision AS BLOB)),0)+coalesce(length(CAST(OLD.structure_revision AS BLOB)),0)+256)) WHERE id=1;
END;

CREATE TRIGGER mooring_collab_target_effects_storage_insert AFTER INSERT ON mooring_collab_target_effects
BEGIN
 INSERT INTO artifact_storage_workspaces(workspace_id,max_bytes) SELECT NEW.workspace_id,workspace_max_bytes FROM artifact_storage_global WHERE id=1 AND NEW.workspace_id IS NOT NULL ON CONFLICT(workspace_id) DO NOTHING;
 UPDATE artifact_storage_workspaces SET used_bytes=used_bytes+(coalesce(length(CAST(NEW.record_json AS BLOB)),0)+256) WHERE workspace_id=NEW.workspace_id;
 UPDATE artifact_storage_global SET used_bytes=used_bytes+(coalesce(length(CAST(NEW.record_json AS BLOB)),0)+256) WHERE id=1;
END;
CREATE TRIGGER mooring_collab_target_effects_storage_delete AFTER DELETE ON mooring_collab_target_effects
BEGIN
 UPDATE artifact_storage_workspaces SET used_bytes=max(0,used_bytes-(coalesce(length(CAST(OLD.record_json AS BLOB)),0)+256)) WHERE workspace_id=OLD.workspace_id;
 UPDATE artifact_storage_global SET used_bytes=max(0,used_bytes-(coalesce(length(CAST(OLD.record_json AS BLOB)),0)+256)) WHERE id=1;
END;
CREATE TRIGGER mooring_collab_target_effects_storage_update AFTER UPDATE ON mooring_collab_target_effects
BEGIN
 INSERT INTO artifact_storage_workspaces(workspace_id,max_bytes) SELECT NEW.workspace_id,workspace_max_bytes FROM artifact_storage_global WHERE id=1 AND NEW.workspace_id IS NOT NULL ON CONFLICT(workspace_id) DO NOTHING;
 UPDATE artifact_storage_workspaces SET used_bytes=max(0,used_bytes+(coalesce(length(CAST(NEW.record_json AS BLOB)),0)+256)-(coalesce(length(CAST(OLD.record_json AS BLOB)),0)+256)) WHERE workspace_id=NEW.workspace_id AND NEW.workspace_id=OLD.workspace_id;
 UPDATE artifact_storage_workspaces SET used_bytes=max(0,used_bytes-(coalesce(length(CAST(OLD.record_json AS BLOB)),0)+256)) WHERE workspace_id=OLD.workspace_id AND NEW.workspace_id<>OLD.workspace_id;
 UPDATE artifact_storage_workspaces SET used_bytes=used_bytes+(coalesce(length(CAST(NEW.record_json AS BLOB)),0)+256) WHERE workspace_id=NEW.workspace_id AND NEW.workspace_id<>OLD.workspace_id;
 UPDATE artifact_storage_global SET used_bytes=max(0,used_bytes+(coalesce(length(CAST(NEW.record_json AS BLOB)),0)+256)-(coalesce(length(CAST(OLD.record_json AS BLOB)),0)+256)) WHERE id=1;
END;

CREATE TRIGGER mooring_collab_artifact_edits_storage_insert AFTER INSERT ON mooring_collab_artifact_edits
BEGIN
 INSERT INTO artifact_storage_workspaces(workspace_id,max_bytes) SELECT NEW.workspace_id,workspace_max_bytes FROM artifact_storage_global WHERE id=1 AND NEW.workspace_id IS NOT NULL ON CONFLICT(workspace_id) DO NOTHING;
 UPDATE artifact_storage_workspaces SET used_bytes=used_bytes+(coalesce(length(CAST(NEW.application_id AS BLOB)),0)+coalesce(length(CAST(NEW.change_id AS BLOB)),0)+coalesce(length(CAST(NEW.membership_hash AS BLOB)),0)+coalesce(length(CAST(NEW.host_session_id AS BLOB)),0)+coalesce(length(CAST(NEW.human_user_id AS BLOB)),0)+coalesce(length(CAST(NEW.agent_id AS BLOB)),0)+coalesce(length(CAST(NEW.crew_id AS BLOB)),0)+coalesce(length(CAST(NEW.chat_id AS BLOB)),0)+coalesce(length(CAST(NEW.message_id AS BLOB)),0)+coalesce(length(CAST(NEW.work_id AS BLOB)),0)+coalesce(length(CAST(NEW.run_id AS BLOB)),0)+coalesce(length(CAST(NEW.created_at AS BLOB)),0)+256) WHERE workspace_id=NEW.workspace_id;
 UPDATE artifact_storage_global SET used_bytes=used_bytes+(coalesce(length(CAST(NEW.application_id AS BLOB)),0)+coalesce(length(CAST(NEW.change_id AS BLOB)),0)+coalesce(length(CAST(NEW.membership_hash AS BLOB)),0)+coalesce(length(CAST(NEW.host_session_id AS BLOB)),0)+coalesce(length(CAST(NEW.human_user_id AS BLOB)),0)+coalesce(length(CAST(NEW.agent_id AS BLOB)),0)+coalesce(length(CAST(NEW.crew_id AS BLOB)),0)+coalesce(length(CAST(NEW.chat_id AS BLOB)),0)+coalesce(length(CAST(NEW.message_id AS BLOB)),0)+coalesce(length(CAST(NEW.work_id AS BLOB)),0)+coalesce(length(CAST(NEW.run_id AS BLOB)),0)+coalesce(length(CAST(NEW.created_at AS BLOB)),0)+256) WHERE id=1;
END;
CREATE TRIGGER mooring_collab_artifact_edits_storage_delete AFTER DELETE ON mooring_collab_artifact_edits
BEGIN
 UPDATE artifact_storage_workspaces SET used_bytes=max(0,used_bytes-(coalesce(length(CAST(OLD.application_id AS BLOB)),0)+coalesce(length(CAST(OLD.change_id AS BLOB)),0)+coalesce(length(CAST(OLD.membership_hash AS BLOB)),0)+coalesce(length(CAST(OLD.host_session_id AS BLOB)),0)+coalesce(length(CAST(OLD.human_user_id AS BLOB)),0)+coalesce(length(CAST(OLD.agent_id AS BLOB)),0)+coalesce(length(CAST(OLD.crew_id AS BLOB)),0)+coalesce(length(CAST(OLD.chat_id AS BLOB)),0)+coalesce(length(CAST(OLD.message_id AS BLOB)),0)+coalesce(length(CAST(OLD.work_id AS BLOB)),0)+coalesce(length(CAST(OLD.run_id AS BLOB)),0)+coalesce(length(CAST(OLD.created_at AS BLOB)),0)+256)) WHERE workspace_id=OLD.workspace_id;
 UPDATE artifact_storage_global SET used_bytes=max(0,used_bytes-(coalesce(length(CAST(OLD.application_id AS BLOB)),0)+coalesce(length(CAST(OLD.change_id AS BLOB)),0)+coalesce(length(CAST(OLD.membership_hash AS BLOB)),0)+coalesce(length(CAST(OLD.host_session_id AS BLOB)),0)+coalesce(length(CAST(OLD.human_user_id AS BLOB)),0)+coalesce(length(CAST(OLD.agent_id AS BLOB)),0)+coalesce(length(CAST(OLD.crew_id AS BLOB)),0)+coalesce(length(CAST(OLD.chat_id AS BLOB)),0)+coalesce(length(CAST(OLD.message_id AS BLOB)),0)+coalesce(length(CAST(OLD.work_id AS BLOB)),0)+coalesce(length(CAST(OLD.run_id AS BLOB)),0)+coalesce(length(CAST(OLD.created_at AS BLOB)),0)+256)) WHERE id=1;
END;
CREATE TRIGGER mooring_collab_artifact_edits_storage_update AFTER UPDATE ON mooring_collab_artifact_edits
BEGIN
 INSERT INTO artifact_storage_workspaces(workspace_id,max_bytes) SELECT NEW.workspace_id,workspace_max_bytes FROM artifact_storage_global WHERE id=1 AND NEW.workspace_id IS NOT NULL ON CONFLICT(workspace_id) DO NOTHING;
 UPDATE artifact_storage_workspaces SET used_bytes=max(0,used_bytes+(coalesce(length(CAST(NEW.application_id AS BLOB)),0)+coalesce(length(CAST(NEW.change_id AS BLOB)),0)+coalesce(length(CAST(NEW.membership_hash AS BLOB)),0)+coalesce(length(CAST(NEW.host_session_id AS BLOB)),0)+coalesce(length(CAST(NEW.human_user_id AS BLOB)),0)+coalesce(length(CAST(NEW.agent_id AS BLOB)),0)+coalesce(length(CAST(NEW.crew_id AS BLOB)),0)+coalesce(length(CAST(NEW.chat_id AS BLOB)),0)+coalesce(length(CAST(NEW.message_id AS BLOB)),0)+coalesce(length(CAST(NEW.work_id AS BLOB)),0)+coalesce(length(CAST(NEW.run_id AS BLOB)),0)+coalesce(length(CAST(NEW.created_at AS BLOB)),0)+256)-(coalesce(length(CAST(OLD.application_id AS BLOB)),0)+coalesce(length(CAST(OLD.change_id AS BLOB)),0)+coalesce(length(CAST(OLD.membership_hash AS BLOB)),0)+coalesce(length(CAST(OLD.host_session_id AS BLOB)),0)+coalesce(length(CAST(OLD.human_user_id AS BLOB)),0)+coalesce(length(CAST(OLD.agent_id AS BLOB)),0)+coalesce(length(CAST(OLD.crew_id AS BLOB)),0)+coalesce(length(CAST(OLD.chat_id AS BLOB)),0)+coalesce(length(CAST(OLD.message_id AS BLOB)),0)+coalesce(length(CAST(OLD.work_id AS BLOB)),0)+coalesce(length(CAST(OLD.run_id AS BLOB)),0)+coalesce(length(CAST(OLD.created_at AS BLOB)),0)+256)) WHERE workspace_id=NEW.workspace_id AND NEW.workspace_id=OLD.workspace_id;
 UPDATE artifact_storage_workspaces SET used_bytes=max(0,used_bytes-(coalesce(length(CAST(OLD.application_id AS BLOB)),0)+coalesce(length(CAST(OLD.change_id AS BLOB)),0)+coalesce(length(CAST(OLD.membership_hash AS BLOB)),0)+coalesce(length(CAST(OLD.host_session_id AS BLOB)),0)+coalesce(length(CAST(OLD.human_user_id AS BLOB)),0)+coalesce(length(CAST(OLD.agent_id AS BLOB)),0)+coalesce(length(CAST(OLD.crew_id AS BLOB)),0)+coalesce(length(CAST(OLD.chat_id AS BLOB)),0)+coalesce(length(CAST(OLD.message_id AS BLOB)),0)+coalesce(length(CAST(OLD.work_id AS BLOB)),0)+coalesce(length(CAST(OLD.run_id AS BLOB)),0)+coalesce(length(CAST(OLD.created_at AS BLOB)),0)+256)) WHERE workspace_id=OLD.workspace_id AND NEW.workspace_id<>OLD.workspace_id;
 UPDATE artifact_storage_workspaces SET used_bytes=used_bytes+(coalesce(length(CAST(NEW.application_id AS BLOB)),0)+coalesce(length(CAST(NEW.change_id AS BLOB)),0)+coalesce(length(CAST(NEW.membership_hash AS BLOB)),0)+coalesce(length(CAST(NEW.host_session_id AS BLOB)),0)+coalesce(length(CAST(NEW.human_user_id AS BLOB)),0)+coalesce(length(CAST(NEW.agent_id AS BLOB)),0)+coalesce(length(CAST(NEW.crew_id AS BLOB)),0)+coalesce(length(CAST(NEW.chat_id AS BLOB)),0)+coalesce(length(CAST(NEW.message_id AS BLOB)),0)+coalesce(length(CAST(NEW.work_id AS BLOB)),0)+coalesce(length(CAST(NEW.run_id AS BLOB)),0)+coalesce(length(CAST(NEW.created_at AS BLOB)),0)+256) WHERE workspace_id=NEW.workspace_id AND NEW.workspace_id<>OLD.workspace_id;
 UPDATE artifact_storage_global SET used_bytes=max(0,used_bytes+(coalesce(length(CAST(NEW.application_id AS BLOB)),0)+coalesce(length(CAST(NEW.change_id AS BLOB)),0)+coalesce(length(CAST(NEW.membership_hash AS BLOB)),0)+coalesce(length(CAST(NEW.host_session_id AS BLOB)),0)+coalesce(length(CAST(NEW.human_user_id AS BLOB)),0)+coalesce(length(CAST(NEW.agent_id AS BLOB)),0)+coalesce(length(CAST(NEW.crew_id AS BLOB)),0)+coalesce(length(CAST(NEW.chat_id AS BLOB)),0)+coalesce(length(CAST(NEW.message_id AS BLOB)),0)+coalesce(length(CAST(NEW.work_id AS BLOB)),0)+coalesce(length(CAST(NEW.run_id AS BLOB)),0)+coalesce(length(CAST(NEW.created_at AS BLOB)),0)+256)-(coalesce(length(CAST(OLD.application_id AS BLOB)),0)+coalesce(length(CAST(OLD.change_id AS BLOB)),0)+coalesce(length(CAST(OLD.membership_hash AS BLOB)),0)+coalesce(length(CAST(OLD.host_session_id AS BLOB)),0)+coalesce(length(CAST(OLD.human_user_id AS BLOB)),0)+coalesce(length(CAST(OLD.agent_id AS BLOB)),0)+coalesce(length(CAST(OLD.crew_id AS BLOB)),0)+coalesce(length(CAST(OLD.chat_id AS BLOB)),0)+coalesce(length(CAST(OLD.message_id AS BLOB)),0)+coalesce(length(CAST(OLD.work_id AS BLOB)),0)+coalesce(length(CAST(OLD.run_id AS BLOB)),0)+coalesce(length(CAST(OLD.created_at AS BLOB)),0)+256)) WHERE id=1;
END;

CREATE TRIGGER artifact_storage_reservation_insert AFTER INSERT ON artifact_storage_reservations
BEGIN
 INSERT INTO artifact_storage_workspaces(workspace_id,max_bytes) VALUES(NEW.workspace_id,(SELECT workspace_max_bytes FROM artifact_storage_global WHERE id=1)) ON CONFLICT(workspace_id) DO NOTHING;
 UPDATE artifact_storage_workspaces SET reserved_bytes=reserved_bytes+NEW.bytes WHERE workspace_id=NEW.workspace_id;
 UPDATE artifact_storage_global SET reserved_bytes=reserved_bytes+NEW.bytes WHERE id=1;
END;
CREATE TRIGGER artifact_storage_reservation_delete AFTER DELETE ON artifact_storage_reservations
BEGIN
 UPDATE artifact_storage_workspaces SET reserved_bytes=max(0,reserved_bytes-OLD.bytes) WHERE workspace_id=OLD.workspace_id;
 UPDATE artifact_storage_global SET reserved_bytes=max(0,reserved_bytes-OLD.bytes) WHERE id=1;
END;
CREATE TRIGGER artifact_storage_reservation_immutable BEFORE UPDATE ON artifact_storage_reservations
BEGIN SELECT RAISE(ABORT,'artifact storage reservation is immutable'); END;
