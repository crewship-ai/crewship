-- #5069, owner's decision: a grant ends when its issuer loses the authority
-- needed to issue it. Restoring the role does not revive the old grant; a new
-- grant is a new operation.
--
-- Retirement happens here, in the transaction of the change that removed the
-- standing, so it needs no read in between and no code path can forget it. An
-- authority generation compared at use time was rejected: users.authority_revision
-- and access_revision also move for promotions and capability changes, and
-- neither moves for crew membership or Page ownership. It would end grants whose
-- issuer kept standing and miss grants whose issuer lost it.
--
-- The row stays: the grants list keeps inactive metadata (docs/specs/pages.md),
-- revoke still removes it, and reissuing clears the marker as a new operation.
ALTER TABLE page_grants ADD COLUMN retired_at TEXT;

-- The one definition of a grant's standing. The authorization reader
-- (pages_grants_authz.go) and every trigger below ask this view.
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

-- What retirement does beyond the marker, in the transaction that sets it.
--
-- A retired issuance is remembered without the grant row: revoke deletes the row
-- and reissue overwrites it, and an earlier bundle restored over this target must
-- still meet the retirement. Target-local, like workspace_member_retired_ids: no
-- foreign key, so a replacing restore leaves it, and it never travels in a bundle.
CREATE TABLE page_grant_retired_issuances (
 workspace_id TEXT NOT NULL,
 page_id TEXT NOT NULL,
 subject_type TEXT NOT NULL,
 subject_id TEXT NOT NULL,
 level TEXT NOT NULL,
 granted_by_user_id TEXT NOT NULL,
 granted_at TEXT NOT NULL,
 retired_at TEXT NOT NULL,
 PRIMARY KEY(page_id,subject_type,subject_id,level,granted_by_user_id,granted_at)
) WITHOUT ROWID;
CREATE INDEX idx_page_grant_retired_issuer ON page_grant_retired_issuances(granted_by_user_id);
CREATE INDEX idx_page_grant_retired_subject ON page_grant_retired_issuances(subject_id);

-- A workbook ticket is opened on its editor's write grant and checks no grant
-- identity afterwards, so it closes here: the sweep only visits Pages with
-- subscribers, and a reissue would otherwise authorize the old ticket again.
CREATE TRIGGER page_grant_retired AFTER UPDATE OF retired_at ON page_grants
WHEN OLD.retired_at IS NULL AND NEW.retired_at IS NOT NULL
BEGIN
 INSERT OR IGNORE INTO page_grant_retired_issuances(workspace_id,page_id,subject_type,subject_id,level,granted_by_user_id,granted_at,retired_at)
  SELECT p.workspace_id,NEW.page_id,NEW.subject_type,NEW.subject_id,NEW.level,NEW.granted_by_user_id,NEW.granted_at,NEW.retired_at FROM pages p WHERE p.id=NEW.page_id;
 UPDATE gridcraft_sessions SET closed_at=CAST(strftime('%s','now') AS INTEGER)*1000
  WHERE NEW.level='write' AND NEW.subject_type='user' AND page_id=NEW.page_id AND user_id=NEW.subject_id AND closed_at IS NULL;
END;

-- A bundle's retired row is remembered like one retired here.
CREATE TRIGGER page_grant_retired_import AFTER INSERT ON page_grants
WHEN NEW.retired_at IS NOT NULL
BEGIN
 INSERT OR IGNORE INTO page_grant_retired_issuances(workspace_id,page_id,subject_type,subject_id,level,granted_by_user_id,granted_at,retired_at)
  SELECT p.workspace_id,NEW.page_id,NEW.subject_type,NEW.subject_id,NEW.level,NEW.granted_by_user_id,NEW.granted_at,NEW.retired_at FROM pages p WHERE p.id=NEW.page_id;
END;

CREATE TRIGGER page_grant_retired_user_deleted AFTER DELETE ON users
BEGIN
 DELETE FROM page_grant_retired_issuances WHERE granted_by_user_id=OLD.id OR (subject_type='user' AND subject_id=OLD.id);
END;

-- A grant whose issuer has no standing today is retired once. A live grant is
-- not touched.
UPDATE page_grants SET retired_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
 WHERE rowid IN (SELECT grant_rowid FROM page_grant_verdicts WHERE issuer_standing=0);

-- Every input of the view that can take standing away. A hard-deleted issuer
-- needs no trigger: page_grants.granted_by_user_id cascades. An inserted member
-- row can only take standing away by being restricted.
CREATE TRIGGER page_grant_retire_member_insert AFTER INSERT ON workspace_members
WHEN NEW.access_mode IS NOT 'trusted'
BEGIN
 UPDATE page_grants SET retired_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
  WHERE rowid IN (SELECT grant_rowid FROM page_grant_verdicts WHERE retired_at IS NULL AND issuer_standing=0 AND granted_by_user_id=NEW.user_id);
END;

CREATE TRIGGER page_grant_retire_member_delete AFTER DELETE ON workspace_members
BEGIN
 UPDATE page_grants SET retired_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
  WHERE rowid IN (SELECT grant_rowid FROM page_grant_verdicts WHERE retired_at IS NULL AND issuer_standing=0 AND granted_by_user_id=OLD.user_id);
END;

CREATE TRIGGER page_grant_retire_member_update AFTER UPDATE OF workspace_id,user_id,role,access_mode ON workspace_members
WHEN OLD.workspace_id IS NOT NEW.workspace_id OR OLD.user_id IS NOT NEW.user_id OR OLD.role IS NOT NEW.role OR OLD.access_mode IS NOT NEW.access_mode
BEGIN
 UPDATE page_grants SET retired_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
  WHERE rowid IN (SELECT grant_rowid FROM page_grant_verdicts WHERE retired_at IS NULL AND issuer_standing=0 AND granted_by_user_id IN (OLD.user_id,NEW.user_id));
END;

CREATE TRIGGER page_grant_retire_user_suspended AFTER UPDATE OF suspended_at ON users
WHEN NEW.suspended_at IS NOT NULL
BEGIN
 UPDATE page_grants SET retired_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
  WHERE rowid IN (SELECT grant_rowid FROM page_grant_verdicts WHERE retired_at IS NULL AND issuer_standing=0 AND granted_by_user_id=NEW.id);
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

CREATE TRIGGER page_grant_retire_page_owner AFTER UPDATE OF workspace_id,owner_user_id,owner_crew_id ON pages
WHEN OLD.workspace_id IS NOT NEW.workspace_id OR OLD.owner_user_id IS NOT NEW.owner_user_id OR OLD.owner_crew_id IS NOT NEW.owner_crew_id
BEGIN
 UPDATE page_grants SET retired_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
  WHERE rowid IN (SELECT grant_rowid FROM page_grant_verdicts WHERE retired_at IS NULL AND issuer_standing=0 AND page_id=NEW.id);
END;
