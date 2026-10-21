-- Offline workbook candidates have one host-owned committed head per Page.
-- Permissions remain in the existing Page/run authority, not these tables.
CREATE TABLE gridcraft_writer_fence (id INTEGER PRIMARY KEY CHECK(id=1), seq INTEGER NOT NULL);
INSERT INTO gridcraft_writer_fence VALUES(1,0);
CREATE TABLE gridcraft_heads (
 page_id TEXT PRIMARY KEY,
 workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 schema_version INTEGER NOT NULL CHECK(schema_version=1),
 head_rev INTEGER NOT NULL CHECK(head_rev>0),
 generation INTEGER NOT NULL CHECK(generation>0),
 snapshot BLOB NOT NULL,
 UNIQUE(workspace_id,page_id),
 FOREIGN KEY(workspace_id,page_id) REFERENCES pages(workspace_id,id) ON DELETE CASCADE
);
CREATE TABLE gridcraft_operations (
 workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 page_id TEXT NOT NULL REFERENCES pages(id) ON DELETE CASCADE,
 authority_id TEXT NOT NULL,
 mutation_id TEXT NOT NULL,
 payload_hash TEXT NOT NULL,
 state TEXT NOT NULL CHECK(state IN ('in_progress','failed','applied')),
 token INTEGER NOT NULL CHECK(token>0),
 lease_until INTEGER NOT NULL,
 receipt BLOB,
 PRIMARY KEY(workspace_id,page_id,authority_id,mutation_id),
 FOREIGN KEY(workspace_id,page_id) REFERENCES pages(workspace_id,id) ON DELETE CASCADE,
 CHECK((state='applied')=(receipt IS NOT NULL))
);
CREATE INDEX gridcraft_operations_active ON gridcraft_operations(state,lease_until,workspace_id,page_id);
CREATE TABLE gridcraft_revisions (
 page_id TEXT NOT NULL REFERENCES gridcraft_heads(page_id) ON DELETE CASCADE,
 rev INTEGER NOT NULL,
 snapshot BLOB NOT NULL,
 PRIMARY KEY(page_id,rev)
);
CREATE TABLE gridcraft_audit (
 page_id TEXT NOT NULL REFERENCES gridcraft_heads(page_id) ON DELETE CASCADE,
 rev INTEGER NOT NULL,
 authority_id TEXT NOT NULL,
 mutation_id TEXT NOT NULL,
 mandate_id TEXT NOT NULL,
 base_rev INTEGER NOT NULL,
 receipt BLOB NOT NULL,
 PRIMARY KEY(page_id,rev)
);
CREATE TABLE gridcraft_outbox (
 page_id TEXT NOT NULL REFERENCES gridcraft_heads(page_id) ON DELETE CASCADE,
 rev INTEGER NOT NULL,
 state TEXT NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','sent')),
 PRIMARY KEY(page_id,rev)
);
CREATE TRIGGER gridcraft_require_personal_page_insert BEFORE INSERT ON gridcraft_heads
WHEN NOT EXISTS(SELECT 1 FROM pages WHERE id=NEW.page_id AND workspace_id=NEW.workspace_id AND owner_user_id IS NOT NULL AND owner_crew_id IS NULL AND private_audience=1 AND folder_id IS NULL)
 OR EXISTS(SELECT 1 FROM documents WHERE page_id=NEW.page_id)
 OR EXISTS(SELECT 1 FROM slides_heads WHERE page_id=NEW.page_id)
 OR EXISTS(SELECT 1 FROM page_panels WHERE page_id=NEW.page_id)
 OR EXISTS(SELECT 1 FROM page_project_drafts WHERE page_id=NEW.page_id)
 OR EXISTS(SELECT 1 FROM page_project_live WHERE page_id=NEW.page_id)
BEGIN SELECT RAISE(ABORT,'Workbook requires an empty personal Page'); END;
CREATE TRIGGER gridcraft_require_personal_page_update BEFORE UPDATE OF page_id,workspace_id ON gridcraft_heads
WHEN NOT EXISTS(SELECT 1 FROM pages WHERE id=NEW.page_id AND workspace_id=NEW.workspace_id AND owner_user_id IS NOT NULL AND owner_crew_id IS NULL AND private_audience=1 AND folder_id IS NULL)
 OR EXISTS(SELECT 1 FROM documents WHERE page_id=NEW.page_id)
 OR EXISTS(SELECT 1 FROM slides_heads WHERE page_id=NEW.page_id)
 OR EXISTS(SELECT 1 FROM page_panels WHERE page_id=NEW.page_id)
 OR EXISTS(SELECT 1 FROM page_project_drafts WHERE page_id=NEW.page_id)
 OR EXISTS(SELECT 1 FROM page_project_live WHERE page_id=NEW.page_id)
BEGIN SELECT RAISE(ABORT,'Workbook requires an empty personal Page'); END;
CREATE TRIGGER documents_exclude_gridcraft_insert BEFORE INSERT ON documents
WHEN EXISTS(SELECT 1 FROM gridcraft_heads WHERE page_id=NEW.page_id)
BEGIN SELECT RAISE(ABORT,'Page already contains a workbook'); END;
CREATE TRIGGER documents_exclude_gridcraft_update BEFORE UPDATE OF page_id ON documents
WHEN EXISTS(SELECT 1 FROM gridcraft_heads WHERE page_id=NEW.page_id)
BEGIN SELECT RAISE(ABORT,'Page already contains a workbook'); END;
CREATE TRIGGER slides_heads_exclude_gridcraft_insert BEFORE INSERT ON slides_heads
WHEN EXISTS(SELECT 1 FROM gridcraft_heads WHERE page_id=NEW.page_id)
BEGIN SELECT RAISE(ABORT,'Page already contains a workbook'); END;
CREATE TRIGGER slides_heads_exclude_gridcraft_update BEFORE UPDATE OF page_id ON slides_heads
WHEN EXISTS(SELECT 1 FROM gridcraft_heads WHERE page_id=NEW.page_id)
BEGIN SELECT RAISE(ABORT,'Page already contains a workbook'); END;
CREATE TRIGGER page_panels_exclude_gridcraft_insert BEFORE INSERT ON page_panels
WHEN EXISTS(SELECT 1 FROM gridcraft_heads WHERE page_id=NEW.page_id)
BEGIN SELECT RAISE(ABORT,'Page already contains a workbook'); END;
CREATE TRIGGER page_panels_exclude_gridcraft_update BEFORE UPDATE OF page_id ON page_panels
WHEN EXISTS(SELECT 1 FROM gridcraft_heads WHERE page_id=NEW.page_id)
BEGIN SELECT RAISE(ABORT,'Page already contains a workbook'); END;
CREATE TRIGGER page_project_drafts_exclude_gridcraft_insert BEFORE INSERT ON page_project_drafts
WHEN EXISTS(SELECT 1 FROM gridcraft_heads WHERE page_id=NEW.page_id)
BEGIN SELECT RAISE(ABORT,'Page already contains a workbook'); END;
CREATE TRIGGER page_project_drafts_exclude_gridcraft_update BEFORE UPDATE OF page_id ON page_project_drafts
WHEN EXISTS(SELECT 1 FROM gridcraft_heads WHERE page_id=NEW.page_id)
BEGIN SELECT RAISE(ABORT,'Page already contains a workbook'); END;
CREATE TRIGGER page_project_live_exclude_gridcraft_insert BEFORE INSERT ON page_project_live
WHEN EXISTS(SELECT 1 FROM gridcraft_heads WHERE page_id=NEW.page_id)
BEGIN SELECT RAISE(ABORT,'Page already contains a workbook'); END;
CREATE TRIGGER page_project_live_exclude_gridcraft_update BEFORE UPDATE OF page_id ON page_project_live
WHEN EXISTS(SELECT 1 FROM gridcraft_heads WHERE page_id=NEW.page_id)
BEGIN SELECT RAISE(ABORT,'Page already contains a workbook'); END;
CREATE TRIGGER gridcraft_receipt_immutable BEFORE UPDATE ON gridcraft_operations
WHEN OLD.state='applied'
BEGIN SELECT RAISE(ABORT,'Confirmed workbook receipt is immutable'); END;
CREATE TRIGGER gridcraft_operation_identity_immutable BEFORE UPDATE OF workspace_id,page_id,authority_id,mutation_id,payload_hash ON gridcraft_operations
BEGIN SELECT RAISE(ABORT,'Workbook operation identity is immutable'); END;
-- An immutable, short-lived targeted instruction augments existing run authority.
-- This is not an ACL: the host rechecks the run, human and Page on every use.
CREATE TABLE gridcraft_intents (
 workspace_id TEXT NOT NULL, page_id TEXT NOT NULL, mandate_id TEXT NOT NULL,
 human_user_id TEXT NOT NULL REFERENCES users(id), run_id TEXT NOT NULL,
 base_rev INTEGER NOT NULL, generation INTEGER NOT NULL, page_version INTEGER NOT NULL, policy_version INTEGER NOT NULL,
 targets_json TEXT NOT NULL, payload_hash TEXT NOT NULL,
 revoked_at TEXT,
 PRIMARY KEY(workspace_id,page_id,mandate_id),
 FOREIGN KEY(workspace_id,page_id) REFERENCES gridcraft_heads(workspace_id,page_id) ON DELETE CASCADE
);
CREATE TRIGGER gridcraft_intent_identity_immutable BEFORE UPDATE OF workspace_id,page_id,mandate_id,human_user_id,run_id,base_rev,generation,page_version,policy_version,targets_json,payload_hash ON gridcraft_intents
BEGIN SELECT RAISE(ABORT,'Workbook mandate identity is immutable'); END;
CREATE TRIGGER gridcraft_intent_revocation_terminal BEFORE UPDATE OF revoked_at ON gridcraft_intents
WHEN OLD.revoked_at IS NOT NULL
BEGIN SELECT RAISE(ABORT,'Workbook mandate cannot be renewed'); END;
