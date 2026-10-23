-- Generic Mooring JSON profile. Application registration is compiled, not data.
CREATE TABLE mooring_documents (
 workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 page_id TEXT PRIMARY KEY REFERENCES pages(id) ON DELETE CASCADE,
 app_id TEXT NOT NULL,
 rev INTEGER NOT NULL,
 generation INTEGER NOT NULL DEFAULT 1,
 snapshot TEXT NOT NULL
);
CREATE TABLE mooring_receipts (
 page_id TEXT NOT NULL REFERENCES mooring_documents(page_id) ON DELETE CASCADE,
 authority_id TEXT NOT NULL,
 mutation_id TEXT NOT NULL,
 fingerprint TEXT NOT NULL,
 receipt TEXT NOT NULL,
 snapshot TEXT NOT NULL,
 PRIMARY KEY(page_id,authority_id,mutation_id)
);
CREATE TABLE mooring_intents (
 page_id TEXT NOT NULL REFERENCES mooring_documents(page_id) ON DELETE CASCADE,
 mandate_id TEXT NOT NULL,
 run_id TEXT NOT NULL,
 human_user_id TEXT NOT NULL,
 page_version INTEGER NOT NULL,
 policy_version INTEGER NOT NULL,
 generation INTEGER NOT NULL,
 base_rev INTEGER NOT NULL,
 command TEXT NOT NULL,
 fingerprint TEXT NOT NULL,
 revoked_at TEXT,
 PRIMARY KEY(page_id,mandate_id)
);
CREATE INDEX mooring_documents_workspace ON mooring_documents(workspace_id);
CREATE TRIGGER mooring_receipt_immutable BEFORE UPDATE ON mooring_receipts
BEGIN SELECT RAISE(ABORT,'Confirmed Mooring receipt is immutable'); END;
CREATE TRIGGER mooring_intent_immutable BEFORE UPDATE OF page_id,mandate_id,run_id,human_user_id,page_version,policy_version,generation,base_rev,command,fingerprint ON mooring_intents
BEGIN SELECT RAISE(ABORT,'Mooring mandate identity is immutable'); END;
CREATE TRIGGER mooring_revocation_terminal BEFORE UPDATE OF revoked_at ON mooring_intents
WHEN OLD.revoked_at IS NOT NULL
BEGIN SELECT RAISE(ABORT,'Mooring mandate cannot be renewed'); END;
CREATE TRIGGER mooring_require_personal_page_insert BEFORE INSERT ON mooring_documents
WHEN NOT EXISTS(SELECT 1 FROM pages WHERE id=NEW.page_id AND workspace_id=NEW.workspace_id AND owner_user_id IS NOT NULL AND owner_crew_id IS NULL AND private_audience=1 AND folder_id IS NULL)
 OR EXISTS(SELECT 1 FROM documents WHERE page_id=NEW.page_id)
 OR EXISTS(SELECT 1 FROM slides_heads WHERE page_id=NEW.page_id)
 OR EXISTS(SELECT 1 FROM gridcraft_heads WHERE page_id=NEW.page_id)
 OR EXISTS(SELECT 1 FROM page_panels WHERE page_id=NEW.page_id)
 OR EXISTS(SELECT 1 FROM page_project_drafts WHERE page_id=NEW.page_id)
 OR EXISTS(SELECT 1 FROM page_project_live WHERE page_id=NEW.page_id)
BEGIN SELECT RAISE(ABORT,'Mooring JSON requires an empty personal Page'); END;
CREATE TRIGGER mooring_require_personal_page_revision BEFORE UPDATE OF rev,generation,snapshot ON mooring_documents
WHEN NOT EXISTS(SELECT 1 FROM pages WHERE id=NEW.page_id AND workspace_id=NEW.workspace_id AND owner_user_id IS NOT NULL AND owner_crew_id IS NULL AND private_audience=1 AND folder_id IS NULL)
BEGIN SELECT RAISE(ABORT,'Mooring JSON requires a private personal Page'); END;
