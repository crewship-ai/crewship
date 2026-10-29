-- Add human feedback grants without widening Edit, Produce or native leases.
-- Rebuild the closed CHECK, preserving all grants, FK actions and indexes.
-- Remove/recreate dependent views and triggers inside the migration transaction:
-- no PRAGMA toggles, writable_schema edits or authority generation resets.
DROP TRIGGER mooring_epoch_page_grants_insert;
DROP TRIGGER mooring_epoch_page_grants_delete;
DROP TRIGGER mooring_epoch_page_grants_update_old;
DROP TRIGGER mooring_epoch_page_grants_update_new;
DROP TRIGGER mooring_relevant_grant_insert;
DROP TRIGGER mooring_relevant_grant_delete;
DROP TRIGGER mooring_relevant_grant_old;
DROP TRIGGER mooring_relevant_grant_new;
DROP TRIGGER mooring_relevant_workspace_member_insert;
DROP TRIGGER mooring_relevant_workspace_member_delete;
DROP TRIGGER mooring_relevant_workspace_member_old;
DROP TRIGGER mooring_relevant_workspace_member_new;
DROP TRIGGER mooring_relevant_crew_member_insert;
DROP TRIGGER mooring_relevant_crew_member_delete;
DROP TRIGGER mooring_relevant_crew_member_old;
DROP TRIGGER mooring_relevant_crew_member_new;
DROP TRIGGER mooring_relevant_user_delete;
DROP TRIGGER mooring_relevant_user_status;
DROP TRIGGER mooring_relevant_user_insert;
DROP TRIGGER mooring_relevant_user_identity_old;
DROP TRIGGER mooring_relevant_user_identity_new;
DROP VIEW mooring_generation_grant_recipients;

CREATE TABLE page_grants_comment (
 page_id TEXT NOT NULL REFERENCES pages(id) ON DELETE CASCADE,
 subject_type TEXT NOT NULL CHECK(subject_type IN ('user','crew','agent','workspace')),
 subject_id TEXT NOT NULL DEFAULT '',
 level TEXT NOT NULL CHECK(level IN ('read','comment','produce','write')),
 panel_ids TEXT,
 granted_by_user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 granted_at TEXT NOT NULL DEFAULT (datetime('now')),
 PRIMARY KEY(page_id,subject_type,subject_id,level),
 CHECK(panel_ids IS NULL OR level='produce'),
 CHECK(subject_type<>'workspace' OR (subject_id='' AND level IN ('read','comment','write'))),
 CHECK(subject_type<>'agent' OR level<>'comment')
);
INSERT INTO page_grants_comment(page_id,subject_type,subject_id,level,panel_ids,granted_by_user_id,granted_at)
 SELECT page_id,subject_type,subject_id,level,panel_ids,granted_by_user_id,granted_at FROM page_grants;
DROP TABLE page_grants;
ALTER TABLE page_grants_comment RENAME TO page_grants;
CREATE INDEX idx_page_grants_subject ON page_grants(subject_type, subject_id);
CREATE INDEX idx_page_grants_granted_by ON page_grants(granted_by_user_id);

-- Restore the existing recipient expansion and every canonical/non-ABA fence
-- byte-for-byte (apart from surrounding comments). No generation is refreshed.
CREATE VIEW mooring_generation_grant_recipients(workspace_id,page_id,subject_type,subject_id,level,issuer,principal_kind,principal_id) AS
 SELECT p.workspace_id,g.page_id,g.subject_type,g.subject_id,g.level,g.granted_by_user_id,'user',g.subject_id
 FROM page_grants g JOIN pages p ON p.id=g.page_id WHERE g.subject_type='user'
 UNION SELECT p.workspace_id,g.page_id,g.subject_type,g.subject_id,g.level,g.granted_by_user_id,'agent',g.subject_id
 FROM page_grants g JOIN pages p ON p.id=g.page_id WHERE g.subject_type='agent'
 UNION SELECT p.workspace_id,g.page_id,g.subject_type,g.subject_id,g.level,g.granted_by_user_id,'user',cm.user_id
 FROM page_grants g JOIN pages p ON p.id=g.page_id JOIN crews c ON c.id=g.subject_id AND c.workspace_id=p.workspace_id JOIN crew_members cm ON cm.crew_id=c.id WHERE g.subject_type='crew'
 UNION SELECT p.workspace_id,g.page_id,g.subject_type,g.subject_id,g.level,g.granted_by_user_id,'user',m.user_id
 FROM page_grants g JOIN pages p ON p.id=g.page_id JOIN workspace_members m ON m.workspace_id=p.workspace_id WHERE g.subject_type='workspace';

CREATE TRIGGER mooring_epoch_page_grants_insert AFTER INSERT ON page_grants
BEGIN
 UPDATE workspaces SET authority_revision=authority_revision+1 WHERE id=(SELECT workspace_id FROM pages WHERE id=NEW.page_id);
END;

CREATE TRIGGER mooring_epoch_page_grants_delete BEFORE DELETE ON page_grants
BEGIN
 UPDATE workspaces SET authority_revision=authority_revision+1 WHERE id=(SELECT workspace_id FROM pages WHERE id=OLD.page_id);
END;

CREATE TRIGGER mooring_epoch_page_grants_update_old BEFORE UPDATE OF page_id,subject_type,subject_id,level,panel_ids,granted_by_user_id ON page_grants
WHEN OLD.page_id IS NOT NEW.page_id OR OLD.subject_type IS NOT NEW.subject_type OR OLD.subject_id IS NOT NEW.subject_id OR OLD.level IS NOT NEW.level OR OLD.panel_ids IS NOT NEW.panel_ids OR OLD.granted_by_user_id IS NOT NEW.granted_by_user_id
BEGIN
 UPDATE workspaces SET authority_revision=authority_revision+1 WHERE id=(SELECT workspace_id FROM pages WHERE id=OLD.page_id);
END;

CREATE TRIGGER mooring_epoch_page_grants_update_new AFTER UPDATE OF page_id,subject_type,subject_id,level,panel_ids,granted_by_user_id ON page_grants
WHEN OLD.page_id IS NOT NEW.page_id OR OLD.subject_type IS NOT NEW.subject_type OR OLD.subject_id IS NOT NEW.subject_id OR OLD.level IS NOT NEW.level OR OLD.panel_ids IS NOT NEW.panel_ids OR OLD.granted_by_user_id IS NOT NEW.granted_by_user_id
BEGIN
 UPDATE workspaces SET authority_revision=authority_revision+1 WHERE id=(SELECT workspace_id FROM pages WHERE id=NEW.page_id);
END;

CREATE TRIGGER mooring_relevant_grant_insert AFTER INSERT ON page_grants
BEGIN
 UPDATE mooring_authority_generations SET revision=revision+1 WHERE (workspace_id,scope_kind,scope_id) IN (SELECT workspace_id,principal_kind AS scope_kind,principal_id AS scope_id FROM mooring_generation_grant_recipients WHERE page_id=NEW.page_id AND subject_type=NEW.subject_type AND subject_id=NEW.subject_id AND level=NEW.level);
END;

CREATE TRIGGER mooring_relevant_grant_delete BEFORE DELETE ON page_grants
BEGIN
 UPDATE mooring_authority_generations SET revision=revision+1 WHERE (workspace_id,scope_kind,scope_id) IN (SELECT workspace_id,principal_kind AS scope_kind,principal_id AS scope_id FROM mooring_generation_grant_recipients WHERE page_id=OLD.page_id AND subject_type=OLD.subject_type AND subject_id=OLD.subject_id AND level=OLD.level);
END;

CREATE TRIGGER mooring_relevant_grant_old BEFORE UPDATE OF page_id,subject_type,subject_id,level,panel_ids,granted_by_user_id ON page_grants
WHEN OLD.page_id IS NOT NEW.page_id OR OLD.subject_type IS NOT NEW.subject_type OR OLD.subject_id IS NOT NEW.subject_id OR OLD.level IS NOT NEW.level OR OLD.panel_ids IS NOT NEW.panel_ids OR OLD.granted_by_user_id IS NOT NEW.granted_by_user_id
BEGIN
 UPDATE mooring_authority_generations SET revision=revision+1 WHERE (workspace_id,scope_kind,scope_id) IN (SELECT workspace_id,principal_kind AS scope_kind,principal_id AS scope_id FROM mooring_generation_grant_recipients WHERE page_id=OLD.page_id AND subject_type=OLD.subject_type AND subject_id=OLD.subject_id AND level=OLD.level);
END;

CREATE TRIGGER mooring_relevant_grant_new AFTER UPDATE OF page_id,subject_type,subject_id,level,panel_ids,granted_by_user_id ON page_grants
WHEN OLD.page_id IS NOT NEW.page_id OR OLD.subject_type IS NOT NEW.subject_type OR OLD.subject_id IS NOT NEW.subject_id OR OLD.level IS NOT NEW.level OR OLD.panel_ids IS NOT NEW.panel_ids OR OLD.granted_by_user_id IS NOT NEW.granted_by_user_id
BEGIN
 UPDATE mooring_authority_generations SET revision=revision+1 WHERE (workspace_id,scope_kind,scope_id) IN (SELECT workspace_id,principal_kind AS scope_kind,principal_id AS scope_id FROM mooring_generation_grant_recipients WHERE page_id=NEW.page_id AND subject_type=NEW.subject_type AND subject_id=NEW.subject_id AND level=NEW.level);
END;

CREATE TRIGGER mooring_relevant_workspace_member_insert AFTER INSERT ON workspace_members
BEGIN
 UPDATE mooring_authority_generations SET revision=revision+1 WHERE (workspace_id,scope_kind,scope_id) IN (SELECT workspace_id AS workspace_id,'user' AS scope_kind,NEW.user_id AS scope_id FROM workspace_members WHERE user_id=NEW.user_id UNION SELECT workspace_id,scope_kind,scope_id FROM mooring_authority_generations WHERE scope_kind='user' AND scope_id=NEW.user_id UNION SELECT workspace_id,principal_kind AS scope_kind,principal_id AS scope_id FROM mooring_generation_grant_recipients WHERE issuer=NEW.user_id);
END;

CREATE TRIGGER mooring_relevant_workspace_member_delete BEFORE DELETE ON workspace_members
BEGIN
 UPDATE mooring_authority_generations SET revision=revision+1 WHERE (workspace_id,scope_kind,scope_id) IN (SELECT workspace_id AS workspace_id,'user' AS scope_kind,OLD.user_id AS scope_id FROM workspace_members WHERE user_id=OLD.user_id UNION SELECT workspace_id,scope_kind,scope_id FROM mooring_authority_generations WHERE scope_kind='user' AND scope_id=OLD.user_id UNION SELECT workspace_id,principal_kind AS scope_kind,principal_id AS scope_id FROM mooring_generation_grant_recipients WHERE issuer=OLD.user_id);
END;

CREATE TRIGGER mooring_relevant_workspace_member_old BEFORE UPDATE OF id,workspace_id,user_id,role,access_mode ON workspace_members
WHEN OLD.id IS NOT NEW.id OR OLD.workspace_id IS NOT NEW.workspace_id OR OLD.user_id IS NOT NEW.user_id OR OLD.role IS NOT NEW.role OR OLD.access_mode IS NOT NEW.access_mode
BEGIN
 UPDATE mooring_authority_generations SET revision=revision+1 WHERE (workspace_id,scope_kind,scope_id) IN (SELECT workspace_id AS workspace_id,'user' AS scope_kind,OLD.user_id AS scope_id FROM workspace_members WHERE user_id=OLD.user_id UNION SELECT workspace_id,scope_kind,scope_id FROM mooring_authority_generations WHERE scope_kind='user' AND scope_id=OLD.user_id UNION SELECT workspace_id,principal_kind AS scope_kind,principal_id AS scope_id FROM mooring_generation_grant_recipients WHERE issuer=OLD.user_id);
END;

CREATE TRIGGER mooring_relevant_workspace_member_new AFTER UPDATE OF id,workspace_id,user_id,role,access_mode ON workspace_members
WHEN OLD.id IS NOT NEW.id OR OLD.workspace_id IS NOT NEW.workspace_id OR OLD.user_id IS NOT NEW.user_id OR OLD.role IS NOT NEW.role OR OLD.access_mode IS NOT NEW.access_mode
BEGIN
 UPDATE mooring_authority_generations SET revision=revision+1 WHERE (workspace_id,scope_kind,scope_id) IN (SELECT workspace_id AS workspace_id,'user' AS scope_kind,NEW.user_id AS scope_id FROM workspace_members WHERE user_id=NEW.user_id UNION SELECT workspace_id,scope_kind,scope_id FROM mooring_authority_generations WHERE scope_kind='user' AND scope_id=NEW.user_id UNION SELECT workspace_id,principal_kind AS scope_kind,principal_id AS scope_id FROM mooring_generation_grant_recipients WHERE issuer=NEW.user_id);
END;

CREATE TRIGGER mooring_relevant_crew_member_insert AFTER INSERT ON crew_members
BEGIN
 UPDATE mooring_authority_generations SET revision=revision+1 WHERE (workspace_id,scope_kind,scope_id) IN (SELECT workspace_id AS workspace_id,'user' AS scope_kind,NEW.user_id AS scope_id FROM crews WHERE id=NEW.crew_id UNION SELECT workspace_id,principal_kind AS scope_kind,principal_id AS scope_id FROM mooring_generation_grant_recipients WHERE issuer=NEW.user_id);
END;

CREATE TRIGGER mooring_relevant_crew_member_delete BEFORE DELETE ON crew_members
BEGIN
 UPDATE mooring_authority_generations SET revision=revision+1 WHERE (workspace_id,scope_kind,scope_id) IN (SELECT workspace_id AS workspace_id,'user' AS scope_kind,OLD.user_id AS scope_id FROM crews WHERE id=OLD.crew_id UNION SELECT workspace_id,principal_kind AS scope_kind,principal_id AS scope_id FROM mooring_generation_grant_recipients WHERE issuer=OLD.user_id);
END;

CREATE TRIGGER mooring_relevant_crew_member_old BEFORE UPDATE OF id,crew_id,user_id ON crew_members
WHEN OLD.id IS NOT NEW.id OR OLD.crew_id IS NOT NEW.crew_id OR OLD.user_id IS NOT NEW.user_id
BEGIN
 UPDATE mooring_authority_generations SET revision=revision+1 WHERE (workspace_id,scope_kind,scope_id) IN (SELECT workspace_id AS workspace_id,'user' AS scope_kind,OLD.user_id AS scope_id FROM crews WHERE id=OLD.crew_id UNION SELECT workspace_id,principal_kind AS scope_kind,principal_id AS scope_id FROM mooring_generation_grant_recipients WHERE issuer=OLD.user_id);
END;

CREATE TRIGGER mooring_relevant_crew_member_new AFTER UPDATE OF id,crew_id,user_id ON crew_members
WHEN OLD.id IS NOT NEW.id OR OLD.crew_id IS NOT NEW.crew_id OR OLD.user_id IS NOT NEW.user_id
BEGIN
 UPDATE mooring_authority_generations SET revision=revision+1 WHERE (workspace_id,scope_kind,scope_id) IN (SELECT workspace_id AS workspace_id,'user' AS scope_kind,NEW.user_id AS scope_id FROM crews WHERE id=NEW.crew_id UNION SELECT workspace_id,principal_kind AS scope_kind,principal_id AS scope_id FROM mooring_generation_grant_recipients WHERE issuer=NEW.user_id);
END;

CREATE TRIGGER mooring_relevant_user_delete BEFORE DELETE ON users
BEGIN
 UPDATE mooring_authority_generations SET revision=revision+1 WHERE (workspace_id,scope_kind,scope_id) IN (SELECT workspace_id AS workspace_id,'user' AS scope_kind,OLD.id AS scope_id FROM workspace_members WHERE user_id=OLD.id UNION SELECT workspace_id,scope_kind,scope_id FROM mooring_authority_generations WHERE scope_kind='user' AND scope_id=OLD.id UNION SELECT workspace_id,principal_kind AS scope_kind,principal_id AS scope_id FROM mooring_generation_grant_recipients WHERE issuer=OLD.id);
END;

CREATE TRIGGER mooring_relevant_user_status AFTER UPDATE OF suspended_at ON users
WHEN OLD.suspended_at IS NOT NEW.suspended_at
BEGIN
 UPDATE mooring_authority_generations SET revision=revision+1 WHERE (workspace_id,scope_kind,scope_id) IN (SELECT workspace_id AS workspace_id,'user' AS scope_kind,NEW.id AS scope_id FROM workspace_members WHERE user_id=NEW.id UNION SELECT workspace_id,scope_kind,scope_id FROM mooring_authority_generations WHERE scope_kind='user' AND scope_id=NEW.id UNION SELECT workspace_id,principal_kind AS scope_kind,principal_id AS scope_id FROM mooring_generation_grant_recipients WHERE issuer=NEW.id);
END;

CREATE TRIGGER mooring_relevant_user_insert AFTER INSERT ON users
BEGIN
 UPDATE mooring_authority_generations SET revision=revision+1 WHERE (workspace_id,scope_kind,scope_id) IN (SELECT workspace_id AS workspace_id,'user' AS scope_kind,NEW.id AS scope_id FROM workspace_members WHERE user_id=NEW.id UNION SELECT workspace_id,scope_kind,scope_id FROM mooring_authority_generations WHERE scope_kind='user' AND scope_id=NEW.id UNION SELECT workspace_id,principal_kind AS scope_kind,principal_id AS scope_id FROM mooring_generation_grant_recipients WHERE issuer=NEW.id);
END;

CREATE TRIGGER mooring_relevant_user_identity_old BEFORE UPDATE OF id ON users
WHEN OLD.id IS NOT NEW.id
BEGIN
 UPDATE mooring_authority_generations SET revision=revision+1 WHERE (workspace_id,scope_kind,scope_id) IN (SELECT workspace_id AS workspace_id,'user' AS scope_kind,OLD.id AS scope_id FROM workspace_members WHERE user_id=OLD.id UNION SELECT workspace_id,scope_kind,scope_id FROM mooring_authority_generations WHERE scope_kind='user' AND scope_id=OLD.id UNION SELECT workspace_id,principal_kind AS scope_kind,principal_id AS scope_id FROM mooring_generation_grant_recipients WHERE issuer=OLD.id);
END;

CREATE TRIGGER mooring_relevant_user_identity_new AFTER UPDATE OF id ON users
WHEN OLD.id IS NOT NEW.id
BEGIN
 UPDATE mooring_authority_generations SET revision=revision+1 WHERE (workspace_id,scope_kind,scope_id) IN (SELECT workspace_id AS workspace_id,'user' AS scope_kind,NEW.id AS scope_id FROM workspace_members WHERE user_id=NEW.id UNION SELECT workspace_id,scope_kind,scope_id FROM mooring_authority_generations WHERE scope_kind='user' AND scope_id=NEW.id UNION SELECT workspace_id,principal_kind AS scope_kind,principal_id AS scope_id FROM mooring_generation_grant_recipients WHERE issuer=NEW.id);
END;
