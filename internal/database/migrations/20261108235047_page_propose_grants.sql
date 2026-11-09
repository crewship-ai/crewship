-- Owner's decision (R71 0.14/7): Comment is not Propose. An agent may hold an
-- explicit Propose grant: it reaches the Page, proposes changes that a person
-- accepts or rejects, and never writes. Edit stays the separate `write` level.
--
-- Additive: the closed level CHECK gains 'propose' for agent subjects only.
-- Every existing row is copied unchanged (level, panel scope, issuer, time,
-- retirement marker). Dependent views and triggers are removed and recreated
-- with their current definitions inside this transaction: no PRAGMA toggles,
-- no writable_schema edits, no authority generation moves.

DROP TRIGGER mooring_epoch_page_grants_delete;
DROP TRIGGER mooring_epoch_page_grants_insert;
DROP TRIGGER mooring_epoch_page_grants_update_new;
DROP TRIGGER mooring_epoch_page_grants_update_old;
DROP TRIGGER mooring_relevant_crew_member_delete;
DROP TRIGGER mooring_relevant_crew_member_insert;
DROP TRIGGER mooring_relevant_crew_member_new;
DROP TRIGGER mooring_relevant_crew_member_old;
DROP TRIGGER mooring_relevant_grant_delete;
DROP TRIGGER mooring_relevant_grant_insert;
DROP TRIGGER mooring_relevant_grant_new;
DROP TRIGGER mooring_relevant_grant_old;
DROP TRIGGER mooring_relevant_user_delete;
DROP TRIGGER mooring_relevant_user_identity_new;
DROP TRIGGER mooring_relevant_user_identity_old;
DROP TRIGGER mooring_relevant_user_insert;
DROP TRIGGER mooring_relevant_user_status;
DROP TRIGGER mooring_relevant_workspace_member_delete;
DROP TRIGGER mooring_relevant_workspace_member_insert;
DROP TRIGGER mooring_relevant_workspace_member_new;
DROP TRIGGER mooring_relevant_workspace_member_old;
DROP TRIGGER page_grant_retire_crew_member_delete;
DROP TRIGGER page_grant_retire_crew_member_update;
DROP TRIGGER page_grant_retire_member_delete;
DROP TRIGGER page_grant_retire_member_insert;
DROP TRIGGER page_grant_retire_member_update;
DROP TRIGGER page_grant_retire_page_owner;
DROP TRIGGER page_grant_retire_user_suspended;
DROP TRIGGER page_grant_retired;
DROP TRIGGER page_grant_retired_import;
DROP VIEW mooring_generation_grant_recipients;
DROP VIEW page_grant_verdicts;

CREATE TABLE page_grants_propose (
 page_id TEXT NOT NULL REFERENCES pages(id) ON DELETE CASCADE,
 subject_type TEXT NOT NULL CHECK(subject_type IN ('user','crew','agent','workspace')),
 subject_id TEXT NOT NULL DEFAULT '',
 level TEXT NOT NULL CHECK(level IN ('read','comment','propose','produce','write')),
 panel_ids TEXT,
 granted_by_user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 granted_at TEXT NOT NULL DEFAULT (datetime('now')),
 retired_at TEXT,
 PRIMARY KEY(page_id,subject_type,subject_id,level),
 CHECK(panel_ids IS NULL OR level='produce'),
 CHECK(subject_type<>'workspace' OR (subject_id='' AND level IN ('read','comment','write'))),
 CHECK(subject_type<>'agent' OR level<>'comment'),
 CHECK(level<>'propose' OR subject_type='agent')
);
INSERT INTO page_grants_propose(page_id,subject_type,subject_id,level,panel_ids,granted_by_user_id,granted_at,retired_at)
 SELECT page_id,subject_type,subject_id,level,panel_ids,granted_by_user_id,granted_at,retired_at FROM page_grants;
DROP TABLE page_grants;
ALTER TABLE page_grants_propose RENAME TO page_grants;
CREATE INDEX idx_page_grants_subject ON page_grants(subject_type, subject_id);
CREATE INDEX idx_page_grants_granted_by ON page_grants(granted_by_user_id);

-- Current definitions, unchanged.
CREATE VIEW page_grant_verdicts AS
SELECT g.rowid AS grant_rowid, p.workspace_id AS workspace_id, g.page_id AS page_id,
 g.subject_type AS subject_type, g.subject_id AS subject_id, g.level AS level,
 g.panel_ids AS panel_ids, g.granted_by_user_id AS granted_by_user_id,
 g.granted_at AS granted_at, g.retired_at AS retired_at,
 COALESCE(wm.role,'') AS issuer_role,
 (actor.id IS NULL OR actor.suspended_at IS NOT NULL OR COALESCE(wm.access_mode,'')<>'trusted'
  OR EXISTS(SELECT 1 FROM workspace_members ceiling WHERE ceiling.user_id=g.granted_by_user_id AND ceiling.access_mode='restricted')) AS issuer_unavailable,
 (g.level='write' AND EXISTS(SELECT 1 FROM gridcraft_heads gh WHERE gh.page_id=p.id)
  AND (g.subject_type<>'user' OR p.owner_user_id IS NULL OR g.granted_by_user_id<>p.owner_user_id)) AS workbook_inert,
 CASE
  WHEN g.level='write' AND EXISTS(SELECT 1 FROM gridcraft_heads gh WHERE gh.page_id=p.id)
       AND (g.subject_type<>'user' OR p.owner_user_id IS NULL OR g.granted_by_user_id<>p.owner_user_id) THEN 0
  WHEN wm.user_id IS NULL THEN 0
  WHEN actor.id IS NULL OR actor.suspended_at IS NOT NULL OR COALESCE(wm.access_mode,'')<>'trusted'
       OR EXISTS(SELECT 1 FROM workspace_members ceiling WHERE ceiling.user_id=g.granted_by_user_id AND ceiling.access_mode='restricted') THEN 0
  WHEN wm.role IN ('OWNER','ADMIN') THEN 1
  WHEN p.owner_user_id IS NOT NULL AND g.granted_by_user_id=p.owner_user_id THEN 1
  WHEN p.owner_crew_id IS NOT NULL AND EXISTS(SELECT 1 FROM crew_members cm WHERE cm.crew_id=p.owner_crew_id AND cm.user_id=g.granted_by_user_id) THEN 1
  ELSE 0
 END AS issuer_standing
FROM page_grants g
JOIN pages p ON p.id=g.page_id
LEFT JOIN workspace_members wm ON wm.user_id=g.granted_by_user_id AND wm.workspace_id=p.workspace_id
LEFT JOIN users actor ON actor.id=g.granted_by_user_id;

CREATE VIEW mooring_generation_grant_recipients(workspace_id,page_id,subject_type,subject_id,level,issuer,principal_kind,principal_id) AS
 SELECT p.workspace_id,g.page_id,g.subject_type,g.subject_id,g.level,g.granted_by_user_id,'user',g.subject_id
 FROM page_grants g JOIN pages p ON p.id=g.page_id WHERE g.subject_type='user'
 UNION SELECT p.workspace_id,g.page_id,g.subject_type,g.subject_id,g.level,g.granted_by_user_id,'agent',g.subject_id
 FROM page_grants g JOIN pages p ON p.id=g.page_id WHERE g.subject_type='agent'
 UNION SELECT p.workspace_id,g.page_id,g.subject_type,g.subject_id,g.level,g.granted_by_user_id,'user',cm.user_id
 FROM page_grants g JOIN pages p ON p.id=g.page_id JOIN crews c ON c.id=g.subject_id AND c.workspace_id=p.workspace_id JOIN crew_members cm ON cm.crew_id=c.id WHERE g.subject_type='crew'
 UNION SELECT p.workspace_id,g.page_id,g.subject_type,g.subject_id,g.level,g.granted_by_user_id,'user',m.user_id
 FROM page_grants g JOIN pages p ON p.id=g.page_id JOIN workspace_members m ON m.workspace_id=p.workspace_id WHERE g.subject_type='workspace';

CREATE TRIGGER mooring_epoch_page_grants_delete BEFORE DELETE ON page_grants
BEGIN
 UPDATE workspaces SET authority_revision=authority_revision+1 WHERE id=(SELECT workspace_id FROM pages WHERE id=OLD.page_id);
END;

CREATE TRIGGER mooring_epoch_page_grants_insert AFTER INSERT ON page_grants
BEGIN
 UPDATE workspaces SET authority_revision=authority_revision+1 WHERE id=(SELECT workspace_id FROM pages WHERE id=NEW.page_id);
END;

CREATE TRIGGER mooring_epoch_page_grants_update_new AFTER UPDATE OF page_id,subject_type,subject_id,level,panel_ids,granted_by_user_id ON page_grants
WHEN OLD.page_id IS NOT NEW.page_id OR OLD.subject_type IS NOT NEW.subject_type OR OLD.subject_id IS NOT NEW.subject_id OR OLD.level IS NOT NEW.level OR OLD.panel_ids IS NOT NEW.panel_ids OR OLD.granted_by_user_id IS NOT NEW.granted_by_user_id
BEGIN
 UPDATE workspaces SET authority_revision=authority_revision+1 WHERE id=(SELECT workspace_id FROM pages WHERE id=NEW.page_id);
END;

CREATE TRIGGER mooring_epoch_page_grants_update_old BEFORE UPDATE OF page_id,subject_type,subject_id,level,panel_ids,granted_by_user_id ON page_grants
WHEN OLD.page_id IS NOT NEW.page_id OR OLD.subject_type IS NOT NEW.subject_type OR OLD.subject_id IS NOT NEW.subject_id OR OLD.level IS NOT NEW.level OR OLD.panel_ids IS NOT NEW.panel_ids OR OLD.granted_by_user_id IS NOT NEW.granted_by_user_id
BEGIN
 UPDATE workspaces SET authority_revision=authority_revision+1 WHERE id=(SELECT workspace_id FROM pages WHERE id=OLD.page_id);
END;

CREATE TRIGGER mooring_relevant_crew_member_delete BEFORE DELETE ON crew_members
BEGIN
 UPDATE mooring_authority_generations SET revision=revision+1 WHERE (workspace_id,scope_kind,scope_id) IN (SELECT workspace_id AS workspace_id,'user' AS scope_kind,OLD.user_id AS scope_id FROM crews WHERE id=OLD.crew_id UNION SELECT workspace_id,principal_kind AS scope_kind,principal_id AS scope_id FROM mooring_generation_grant_recipients WHERE issuer=OLD.user_id);
END;

CREATE TRIGGER mooring_relevant_crew_member_insert AFTER INSERT ON crew_members
BEGIN
 UPDATE mooring_authority_generations SET revision=revision+1 WHERE (workspace_id,scope_kind,scope_id) IN (SELECT workspace_id AS workspace_id,'user' AS scope_kind,NEW.user_id AS scope_id FROM crews WHERE id=NEW.crew_id UNION SELECT workspace_id,principal_kind AS scope_kind,principal_id AS scope_id FROM mooring_generation_grant_recipients WHERE issuer=NEW.user_id);
END;

CREATE TRIGGER mooring_relevant_crew_member_new AFTER UPDATE OF id,crew_id,user_id ON crew_members
WHEN OLD.id IS NOT NEW.id OR OLD.crew_id IS NOT NEW.crew_id OR OLD.user_id IS NOT NEW.user_id
BEGIN
 UPDATE mooring_authority_generations SET revision=revision+1 WHERE (workspace_id,scope_kind,scope_id) IN (SELECT workspace_id AS workspace_id,'user' AS scope_kind,NEW.user_id AS scope_id FROM crews WHERE id=NEW.crew_id UNION SELECT workspace_id,principal_kind AS scope_kind,principal_id AS scope_id FROM mooring_generation_grant_recipients WHERE issuer=NEW.user_id);
END;

CREATE TRIGGER mooring_relevant_crew_member_old BEFORE UPDATE OF id,crew_id,user_id ON crew_members
WHEN OLD.id IS NOT NEW.id OR OLD.crew_id IS NOT NEW.crew_id OR OLD.user_id IS NOT NEW.user_id
BEGIN
 UPDATE mooring_authority_generations SET revision=revision+1 WHERE (workspace_id,scope_kind,scope_id) IN (SELECT workspace_id AS workspace_id,'user' AS scope_kind,OLD.user_id AS scope_id FROM crews WHERE id=OLD.crew_id UNION SELECT workspace_id,principal_kind AS scope_kind,principal_id AS scope_id FROM mooring_generation_grant_recipients WHERE issuer=OLD.user_id);
END;

CREATE TRIGGER mooring_relevant_grant_delete BEFORE DELETE ON page_grants
BEGIN
 UPDATE mooring_page_grant_generations SET revision=revision+1 WHERE (workspace_id,page_id,principal_kind,principal_id) IN (SELECT workspace_id,page_id,principal_kind,principal_id FROM mooring_generation_grant_recipients WHERE page_id=OLD.page_id AND subject_type=OLD.subject_type AND subject_id=OLD.subject_id AND level=OLD.level);
END;

CREATE TRIGGER mooring_relevant_grant_insert AFTER INSERT ON page_grants
BEGIN
 UPDATE mooring_page_grant_generations SET revision=revision+1 WHERE (workspace_id,page_id,principal_kind,principal_id) IN (SELECT workspace_id,page_id,principal_kind,principal_id FROM mooring_generation_grant_recipients WHERE page_id=NEW.page_id AND subject_type=NEW.subject_type AND subject_id=NEW.subject_id AND level=NEW.level);
END;

CREATE TRIGGER mooring_relevant_grant_new AFTER UPDATE OF page_id,subject_type,subject_id,level,panel_ids,granted_by_user_id ON page_grants
WHEN OLD.page_id IS NOT NEW.page_id OR OLD.subject_type IS NOT NEW.subject_type OR OLD.subject_id IS NOT NEW.subject_id OR OLD.level IS NOT NEW.level OR OLD.panel_ids IS NOT NEW.panel_ids OR OLD.granted_by_user_id IS NOT NEW.granted_by_user_id
BEGIN
 UPDATE mooring_page_grant_generations SET revision=revision+1 WHERE (workspace_id,page_id,principal_kind,principal_id) IN (SELECT workspace_id,page_id,principal_kind,principal_id FROM mooring_generation_grant_recipients WHERE page_id=NEW.page_id AND subject_type=NEW.subject_type AND subject_id=NEW.subject_id AND level=NEW.level);
END;

CREATE TRIGGER mooring_relevant_grant_old BEFORE UPDATE OF page_id,subject_type,subject_id,level,panel_ids,granted_by_user_id ON page_grants
WHEN OLD.page_id IS NOT NEW.page_id OR OLD.subject_type IS NOT NEW.subject_type OR OLD.subject_id IS NOT NEW.subject_id OR OLD.level IS NOT NEW.level OR OLD.panel_ids IS NOT NEW.panel_ids OR OLD.granted_by_user_id IS NOT NEW.granted_by_user_id
BEGIN
 UPDATE mooring_page_grant_generations SET revision=revision+1 WHERE (workspace_id,page_id,principal_kind,principal_id) IN (SELECT workspace_id,page_id,principal_kind,principal_id FROM mooring_generation_grant_recipients WHERE page_id=OLD.page_id AND subject_type=OLD.subject_type AND subject_id=OLD.subject_id AND level=OLD.level);
END;

CREATE TRIGGER mooring_relevant_user_delete BEFORE DELETE ON users
BEGIN
 UPDATE mooring_authority_generations SET revision=revision+1 WHERE (workspace_id,scope_kind,scope_id) IN (SELECT workspace_id AS workspace_id,'user' AS scope_kind,OLD.id AS scope_id FROM workspace_members WHERE user_id=OLD.id UNION SELECT workspace_id,scope_kind,scope_id FROM mooring_authority_generations WHERE scope_kind='user' AND scope_id=OLD.id UNION SELECT workspace_id,principal_kind AS scope_kind,principal_id AS scope_id FROM mooring_generation_grant_recipients WHERE issuer=OLD.id);
END;

CREATE TRIGGER mooring_relevant_user_identity_new AFTER UPDATE OF id ON users
WHEN OLD.id IS NOT NEW.id
BEGIN
 UPDATE mooring_authority_generations SET revision=revision+1 WHERE (workspace_id,scope_kind,scope_id) IN (SELECT workspace_id AS workspace_id,'user' AS scope_kind,NEW.id AS scope_id FROM workspace_members WHERE user_id=NEW.id UNION SELECT workspace_id,scope_kind,scope_id FROM mooring_authority_generations WHERE scope_kind='user' AND scope_id=NEW.id UNION SELECT workspace_id,principal_kind AS scope_kind,principal_id AS scope_id FROM mooring_generation_grant_recipients WHERE issuer=NEW.id);
END;

CREATE TRIGGER mooring_relevant_user_identity_old BEFORE UPDATE OF id ON users
WHEN OLD.id IS NOT NEW.id
BEGIN
 UPDATE mooring_authority_generations SET revision=revision+1 WHERE (workspace_id,scope_kind,scope_id) IN (SELECT workspace_id AS workspace_id,'user' AS scope_kind,OLD.id AS scope_id FROM workspace_members WHERE user_id=OLD.id UNION SELECT workspace_id,scope_kind,scope_id FROM mooring_authority_generations WHERE scope_kind='user' AND scope_id=OLD.id UNION SELECT workspace_id,principal_kind AS scope_kind,principal_id AS scope_id FROM mooring_generation_grant_recipients WHERE issuer=OLD.id);
END;

CREATE TRIGGER mooring_relevant_user_insert AFTER INSERT ON users
BEGIN
 UPDATE mooring_authority_generations SET revision=revision+1 WHERE (workspace_id,scope_kind,scope_id) IN (SELECT workspace_id AS workspace_id,'user' AS scope_kind,NEW.id AS scope_id FROM workspace_members WHERE user_id=NEW.id UNION SELECT workspace_id,scope_kind,scope_id FROM mooring_authority_generations WHERE scope_kind='user' AND scope_id=NEW.id UNION SELECT workspace_id,principal_kind AS scope_kind,principal_id AS scope_id FROM mooring_generation_grant_recipients WHERE issuer=NEW.id);
END;

CREATE TRIGGER mooring_relevant_user_status AFTER UPDATE OF suspended_at ON users
WHEN OLD.suspended_at IS NOT NEW.suspended_at
BEGIN
 UPDATE mooring_authority_generations SET revision=revision+1 WHERE (workspace_id,scope_kind,scope_id) IN (SELECT workspace_id AS workspace_id,'user' AS scope_kind,NEW.id AS scope_id FROM workspace_members WHERE user_id=NEW.id UNION SELECT workspace_id,scope_kind,scope_id FROM mooring_authority_generations WHERE scope_kind='user' AND scope_id=NEW.id UNION SELECT workspace_id,principal_kind AS scope_kind,principal_id AS scope_id FROM mooring_generation_grant_recipients WHERE issuer=NEW.id);
END;

CREATE TRIGGER mooring_relevant_workspace_member_delete BEFORE DELETE ON workspace_members
BEGIN
 UPDATE mooring_authority_generations SET revision=revision+1 WHERE (OLD.access_mode IS NOT 'trusted' OR workspace_id=OLD.workspace_id) AND (workspace_id,scope_kind,scope_id) IN (SELECT workspace_id AS workspace_id,'user' AS scope_kind,OLD.user_id AS scope_id FROM workspace_members WHERE user_id=OLD.user_id UNION SELECT workspace_id,scope_kind,scope_id FROM mooring_authority_generations WHERE scope_kind='user' AND scope_id=OLD.user_id UNION SELECT workspace_id,principal_kind AS scope_kind,principal_id AS scope_id FROM mooring_generation_grant_recipients WHERE issuer=OLD.user_id);
END;

CREATE TRIGGER mooring_relevant_workspace_member_insert AFTER INSERT ON workspace_members
BEGIN
 UPDATE mooring_authority_generations SET revision=revision+1 WHERE (NEW.access_mode IS NOT 'trusted' OR workspace_id=NEW.workspace_id) AND (workspace_id,scope_kind,scope_id) IN (SELECT workspace_id AS workspace_id,'user' AS scope_kind,NEW.user_id AS scope_id FROM workspace_members WHERE user_id=NEW.user_id UNION SELECT workspace_id,scope_kind,scope_id FROM mooring_authority_generations WHERE scope_kind='user' AND scope_id=NEW.user_id UNION SELECT workspace_id,principal_kind AS scope_kind,principal_id AS scope_id FROM mooring_generation_grant_recipients WHERE issuer=NEW.user_id);
END;

CREATE TRIGGER mooring_relevant_workspace_member_new AFTER UPDATE OF id,workspace_id,user_id,role,access_mode ON workspace_members
WHEN OLD.id IS NOT NEW.id OR OLD.workspace_id IS NOT NEW.workspace_id OR OLD.user_id IS NOT NEW.user_id OR OLD.role IS NOT NEW.role OR OLD.access_mode IS NOT NEW.access_mode
BEGIN
 UPDATE mooring_authority_generations SET revision=revision+1 WHERE ((OLD.access_mode IS NOT 'trusted' OR NEW.access_mode IS NOT 'trusted' OR OLD.id IS NOT NEW.id OR OLD.workspace_id IS NOT NEW.workspace_id OR OLD.user_id IS NOT NEW.user_id) OR workspace_id=NEW.workspace_id) AND (workspace_id,scope_kind,scope_id) IN (SELECT workspace_id AS workspace_id,'user' AS scope_kind,NEW.user_id AS scope_id FROM workspace_members WHERE user_id=NEW.user_id UNION SELECT workspace_id,scope_kind,scope_id FROM mooring_authority_generations WHERE scope_kind='user' AND scope_id=NEW.user_id UNION SELECT workspace_id,principal_kind AS scope_kind,principal_id AS scope_id FROM mooring_generation_grant_recipients WHERE issuer=NEW.user_id);
END;

CREATE TRIGGER mooring_relevant_workspace_member_old BEFORE UPDATE OF id,workspace_id,user_id,role,access_mode ON workspace_members
WHEN OLD.id IS NOT NEW.id OR OLD.workspace_id IS NOT NEW.workspace_id OR OLD.user_id IS NOT NEW.user_id OR OLD.role IS NOT NEW.role OR OLD.access_mode IS NOT NEW.access_mode
BEGIN
 UPDATE mooring_authority_generations SET revision=revision+1 WHERE ((OLD.access_mode IS NOT 'trusted' OR NEW.access_mode IS NOT 'trusted' OR OLD.id IS NOT NEW.id OR OLD.workspace_id IS NOT NEW.workspace_id OR OLD.user_id IS NOT NEW.user_id) OR workspace_id=OLD.workspace_id) AND (workspace_id,scope_kind,scope_id) IN (SELECT workspace_id AS workspace_id,'user' AS scope_kind,OLD.user_id AS scope_id FROM workspace_members WHERE user_id=OLD.user_id UNION SELECT workspace_id,scope_kind,scope_id FROM mooring_authority_generations WHERE scope_kind='user' AND scope_id=OLD.user_id UNION SELECT workspace_id,principal_kind AS scope_kind,principal_id AS scope_id FROM mooring_generation_grant_recipients WHERE issuer=OLD.user_id);
END;

CREATE TRIGGER page_grant_retire_crew_member_delete AFTER DELETE ON crew_members
BEGIN
 UPDATE page_grants SET retired_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
  WHERE rowid IN (SELECT grant_rowid FROM page_grant_verdicts WHERE retired_at IS NULL AND issuer_standing=0 AND granted_by_user_id=OLD.user_id);
END;

CREATE TRIGGER page_grant_retire_crew_member_update AFTER UPDATE OF crew_id,user_id ON crew_members
WHEN OLD.crew_id IS NOT NEW.crew_id OR OLD.user_id IS NOT NEW.user_id
BEGIN
 UPDATE page_grants SET retired_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
  WHERE rowid IN (SELECT grant_rowid FROM page_grant_verdicts WHERE retired_at IS NULL AND issuer_standing=0 AND granted_by_user_id IN (OLD.user_id,NEW.user_id));
END;

CREATE TRIGGER page_grant_retire_member_delete AFTER DELETE ON workspace_members
BEGIN
 UPDATE page_grants SET retired_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
  WHERE rowid IN (SELECT grant_rowid FROM page_grant_verdicts WHERE retired_at IS NULL AND issuer_standing=0 AND granted_by_user_id=OLD.user_id);
END;

CREATE TRIGGER page_grant_retire_member_insert AFTER INSERT ON workspace_members
WHEN NEW.access_mode IS NOT 'trusted'
BEGIN
 UPDATE page_grants SET retired_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
  WHERE rowid IN (SELECT grant_rowid FROM page_grant_verdicts WHERE retired_at IS NULL AND issuer_standing=0 AND granted_by_user_id=NEW.user_id);
END;

CREATE TRIGGER page_grant_retire_member_update AFTER UPDATE OF workspace_id,user_id,role,access_mode ON workspace_members
WHEN OLD.workspace_id IS NOT NEW.workspace_id OR OLD.user_id IS NOT NEW.user_id OR OLD.role IS NOT NEW.role OR OLD.access_mode IS NOT NEW.access_mode
BEGIN
 UPDATE page_grants SET retired_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
  WHERE rowid IN (SELECT grant_rowid FROM page_grant_verdicts WHERE retired_at IS NULL AND issuer_standing=0 AND granted_by_user_id IN (OLD.user_id,NEW.user_id));
END;

CREATE TRIGGER page_grant_retire_page_owner AFTER UPDATE OF workspace_id,owner_user_id,owner_crew_id ON pages
WHEN OLD.workspace_id IS NOT NEW.workspace_id OR OLD.owner_user_id IS NOT NEW.owner_user_id OR OLD.owner_crew_id IS NOT NEW.owner_crew_id
BEGIN
 UPDATE page_grants SET retired_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
  WHERE rowid IN (SELECT grant_rowid FROM page_grant_verdicts WHERE retired_at IS NULL AND issuer_standing=0 AND page_id=NEW.id);
END;

CREATE TRIGGER page_grant_retire_user_suspended AFTER UPDATE OF suspended_at ON users
WHEN NEW.suspended_at IS NOT NULL
BEGIN
 UPDATE page_grants SET retired_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
  WHERE rowid IN (SELECT grant_rowid FROM page_grant_verdicts WHERE retired_at IS NULL AND issuer_standing=0 AND granted_by_user_id=NEW.id);
END;

CREATE TRIGGER page_grant_retired AFTER UPDATE OF retired_at ON page_grants
WHEN OLD.retired_at IS NULL AND NEW.retired_at IS NOT NULL
BEGIN
 INSERT OR IGNORE INTO page_grant_retired_issuances(workspace_id,page_id,subject_type,subject_id,level,granted_by_user_id,granted_at,retired_at)
  SELECT p.workspace_id,NEW.page_id,NEW.subject_type,NEW.subject_id,NEW.level,NEW.granted_by_user_id,NEW.granted_at,NEW.retired_at FROM pages p WHERE p.id=NEW.page_id;
 UPDATE gridcraft_sessions SET closed_at=CAST(strftime('%s','now') AS INTEGER)*1000
  WHERE NEW.level='write' AND NEW.subject_type='user' AND page_id=NEW.page_id AND user_id=NEW.subject_id AND closed_at IS NULL;
END;

CREATE TRIGGER page_grant_retired_import AFTER INSERT ON page_grants
WHEN NEW.retired_at IS NOT NULL
BEGIN
 INSERT OR IGNORE INTO page_grant_retired_issuances(workspace_id,page_id,subject_type,subject_id,level,granted_by_user_id,granted_at,retired_at)
  SELECT p.workspace_id,NEW.page_id,NEW.subject_type,NEW.subject_id,NEW.level,NEW.granted_by_user_id,NEW.granted_at,NEW.retired_at FROM pages p WHERE p.id=NEW.page_id;
END;
