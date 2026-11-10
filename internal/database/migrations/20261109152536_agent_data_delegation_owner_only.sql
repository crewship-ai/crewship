-- #5260, R71 §0.14/1: delegating data Edit to agents is owner-only by every
-- route, not only by an agent `produce` grant (20261109132621).
--
-- 1. A crew `produce` grant lets every agent and routine of that crew push the
--    page's data. It is owner-only like an agent write/produce grant: the
--    triggers of 20261109132621 are recreated below with the one predicate
--    widened to crew produce rows, and stored non-owner crew produce rows are
--    judged once, here. The audit entry and the grants-list reason are the
--    same.
-- 2. Naming `agent/<slug>` as a panel's producer lets that agent push the
--    panel. page_panels now records who designated the producer, and an agent
--    designation the page's owner did not make is retired for good
--    (producer_retired_at), with a page.agent_producer.retired_not_owner audit
--    entry, when it is written, when the page changes owner, when its
--    designator leaves the owning crew, and when the owning crew is deleted or
--    moves workspace. Keeping the producer unchanged in a later edit is no
--    re-designation. The owner re-grants with an agent produce grant, or names
--    the producer anew.
--
-- Owner: the owning user, or a member of the owning crew while that crew is not
-- deleted and belongs to the page's workspace (internal/api
-- pageOwnerForDelegation).

DROP TRIGGER page_grant_agent_delegation_retired;
DROP TRIGGER page_grant_agent_delegation_insert;
DROP TRIGGER page_grant_agent_delegation_update;
DROP TRIGGER page_grant_agent_delegation_page_owner;
DROP TRIGGER page_grant_agent_delegation_crew_member_delete;
DROP TRIGGER page_grant_agent_delegation_crew_member_update;
DROP TRIGGER page_grant_agent_delegation_crew_deleted;
DROP TRIGGER page_grant_agent_delegation_crew_workspace;

-- The triggers of 20261109132621, with crew produce rows in their predicate
-- and the producer designations in their events.
-- Reason and audit at the moment of retirement, whatever retired the row.
CREATE TRIGGER page_grant_agent_delegation_retired AFTER UPDATE OF retired_at ON page_grants
WHEN OLD.retired_at IS NULL AND NEW.retired_at IS NOT NULL
 AND ((NEW.subject_type='agent' AND NEW.level IN ('write','produce')) OR (NEW.subject_type='crew' AND NEW.level='produce'))
 AND NOT EXISTS(SELECT 1 FROM pages op WHERE op.id=NEW.page_id AND (
  (COALESCE(op.owner_user_id,'')<>'' AND op.owner_user_id=NEW.granted_by_user_id)
  OR (COALESCE(op.owner_user_id,'')='' AND COALESCE(op.owner_crew_id,'')<>''
      AND EXISTS(SELECT 1 FROM crew_members ocm JOIN crews oc ON oc.id=ocm.crew_id AND oc.deleted_at IS NULL AND oc.workspace_id=op.workspace_id WHERE ocm.crew_id=op.owner_crew_id AND ocm.user_id=NEW.granted_by_user_id))))
BEGIN
 INSERT OR IGNORE INTO page_grant_retired_issuances(workspace_id,page_id,subject_type,subject_id,level,granted_by_user_id,granted_at,retired_at)
  SELECT p.workspace_id,NEW.page_id,NEW.subject_type,NEW.subject_id,NEW.level,NEW.granted_by_user_id,NEW.granted_at,NEW.retired_at FROM pages p WHERE p.id=NEW.page_id;
 UPDATE page_grant_retired_issuances SET reason='agent_delegation_not_owner'
  WHERE page_id=NEW.page_id AND subject_type=NEW.subject_type AND subject_id=NEW.subject_id
    AND level=NEW.level AND granted_by_user_id=NEW.granted_by_user_id AND granted_at=NEW.granted_at;
 INSERT INTO audit_logs(id,workspace_id,user_id,action,entity_type,entity_id,metadata,created_at)
  SELECT lower(hex(randomblob(16))),p.workspace_id,NULL,'page.agent_grant.retired_not_owner','page',NEW.page_id,
         json_object('subject_type',NEW.subject_type,'subject_id',NEW.subject_id,'level',NEW.level,
                     'granted_by_user_id',NEW.granted_by_user_id,'granted_at',NEW.granted_at,
                     'owner_user_id',p.owner_user_id,'owner_crew_id',p.owner_crew_id,
                     'reason','only the artifact owner delegates Edit or data Edit to an agent (R71 0.14/1)'),
         strftime('%Y-%m-%dT%H:%M:%fZ','now')
  FROM pages p WHERE p.id=NEW.page_id;
END;

-- A live row written by a non-owner is retired as it lands.
CREATE TRIGGER page_grant_agent_delegation_insert AFTER INSERT ON page_grants
WHEN NEW.retired_at IS NULL AND ((NEW.subject_type='agent' AND NEW.level IN ('write','produce')) OR (NEW.subject_type='crew' AND NEW.level='produce'))
 AND NOT EXISTS(SELECT 1 FROM pages op WHERE op.id=NEW.page_id AND (
  (COALESCE(op.owner_user_id,'')<>'' AND op.owner_user_id=NEW.granted_by_user_id)
  OR (COALESCE(op.owner_user_id,'')='' AND COALESCE(op.owner_crew_id,'')<>''
      AND EXISTS(SELECT 1 FROM crew_members ocm JOIN crews oc ON oc.id=ocm.crew_id AND oc.deleted_at IS NULL AND oc.workspace_id=op.workspace_id WHERE ocm.crew_id=op.owner_crew_id AND ocm.user_id=NEW.granted_by_user_id))))
BEGIN
 UPDATE page_grants SET retired_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE rowid=NEW.rowid;
END;

-- So is one an update leaves live under a non-owner: an upsert re-anchoring
-- it, a level change, or clearing the marker by hand.
CREATE TRIGGER page_grant_agent_delegation_update AFTER UPDATE ON page_grants
WHEN NEW.retired_at IS NULL AND ((NEW.subject_type='agent' AND NEW.level IN ('write','produce')) OR (NEW.subject_type='crew' AND NEW.level='produce'))
 AND NOT EXISTS(SELECT 1 FROM pages op WHERE op.id=NEW.page_id AND (
  (COALESCE(op.owner_user_id,'')<>'' AND op.owner_user_id=NEW.granted_by_user_id)
  OR (COALESCE(op.owner_user_id,'')='' AND COALESCE(op.owner_crew_id,'')<>''
      AND EXISTS(SELECT 1 FROM crew_members ocm JOIN crews oc ON oc.id=ocm.crew_id AND oc.deleted_at IS NULL AND oc.workspace_id=op.workspace_id WHERE ocm.crew_id=op.owner_crew_id AND ocm.user_id=NEW.granted_by_user_id))))
BEGIN
 UPDATE page_grants SET retired_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE rowid=NEW.rowid;
END;

-- Ownership moved: the previous owner's delegations end.
CREATE TRIGGER page_grant_agent_delegation_page_owner AFTER UPDATE OF workspace_id,owner_user_id,owner_crew_id ON pages
WHEN OLD.workspace_id IS NOT NEW.workspace_id OR OLD.owner_user_id IS NOT NEW.owner_user_id OR OLD.owner_crew_id IS NOT NEW.owner_crew_id
BEGIN
 UPDATE page_grants SET retired_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
  WHERE page_id=NEW.id AND retired_at IS NULL AND ((subject_type='agent' AND level IN ('write','produce')) OR (subject_type='crew' AND level='produce'))
    AND NOT ((COALESCE(NEW.owner_user_id,'')<>'' AND NEW.owner_user_id=page_grants.granted_by_user_id)
     OR (COALESCE(NEW.owner_user_id,'')='' AND COALESCE(NEW.owner_crew_id,'')<>''
         AND EXISTS(SELECT 1 FROM crew_members ocm JOIN crews oc ON oc.id=ocm.crew_id AND oc.deleted_at IS NULL AND oc.workspace_id=NEW.workspace_id WHERE ocm.crew_id=NEW.owner_crew_id AND ocm.user_id=page_grants.granted_by_user_id)));
 UPDATE page_panels SET producer_retired_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
  WHERE page_id=NEW.id AND producer_kind='agent' AND producer_retired_at IS NULL AND NOT EXISTS(SELECT 1 FROM pages op WHERE op.id=page_panels.page_id AND (
  (COALESCE(op.owner_user_id,'')<>'' AND op.owner_user_id=page_panels.producer_designated_by_user_id)
  OR (COALESCE(op.owner_user_id,'')='' AND COALESCE(op.owner_crew_id,'')<>''
      AND EXISTS(SELECT 1 FROM crew_members ocm JOIN crews oc ON oc.id=ocm.crew_id AND oc.deleted_at IS NULL AND oc.workspace_id=op.workspace_id WHERE ocm.crew_id=op.owner_crew_id AND ocm.user_id=page_panels.producer_designated_by_user_id))));
END;

-- The issuer left the owning crew.
CREATE TRIGGER page_grant_agent_delegation_crew_member_delete AFTER DELETE ON crew_members
BEGIN
 UPDATE page_grants SET retired_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
  WHERE retired_at IS NULL AND ((subject_type='agent' AND level IN ('write','produce')) OR (subject_type='crew' AND level='produce')) AND granted_by_user_id=OLD.user_id
    AND page_id IN (SELECT id FROM pages WHERE owner_crew_id=OLD.crew_id AND COALESCE(owner_user_id,'')='')
    AND NOT EXISTS(SELECT 1 FROM crew_members ocm JOIN crews oc ON oc.id=ocm.crew_id AND oc.deleted_at IS NULL WHERE ocm.crew_id=OLD.crew_id AND ocm.user_id=OLD.user_id);
 UPDATE page_panels SET producer_retired_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
  WHERE producer_kind='agent' AND producer_retired_at IS NULL AND producer_designated_by_user_id=OLD.user_id
    AND page_id IN (SELECT id FROM pages WHERE owner_crew_id=OLD.crew_id AND COALESCE(owner_user_id,'')='')
    AND NOT EXISTS(SELECT 1 FROM pages op WHERE op.id=page_panels.page_id AND (
  (COALESCE(op.owner_user_id,'')<>'' AND op.owner_user_id=page_panels.producer_designated_by_user_id)
  OR (COALESCE(op.owner_user_id,'')='' AND COALESCE(op.owner_crew_id,'')<>''
      AND EXISTS(SELECT 1 FROM crew_members ocm JOIN crews oc ON oc.id=ocm.crew_id AND oc.deleted_at IS NULL AND oc.workspace_id=op.workspace_id WHERE ocm.crew_id=op.owner_crew_id AND ocm.user_id=page_panels.producer_designated_by_user_id))));
END;

CREATE TRIGGER page_grant_agent_delegation_crew_member_update AFTER UPDATE OF crew_id,user_id ON crew_members
WHEN OLD.crew_id IS NOT NEW.crew_id OR OLD.user_id IS NOT NEW.user_id
BEGIN
 UPDATE page_grants SET retired_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
  WHERE retired_at IS NULL AND ((subject_type='agent' AND level IN ('write','produce')) OR (subject_type='crew' AND level='produce')) AND granted_by_user_id=OLD.user_id
    AND page_id IN (SELECT id FROM pages WHERE owner_crew_id=OLD.crew_id AND COALESCE(owner_user_id,'')='')
    AND NOT EXISTS(SELECT 1 FROM crew_members ocm JOIN crews oc ON oc.id=ocm.crew_id AND oc.deleted_at IS NULL WHERE ocm.crew_id=OLD.crew_id AND ocm.user_id=OLD.user_id);
 UPDATE page_panels SET producer_retired_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
  WHERE producer_kind='agent' AND producer_retired_at IS NULL AND producer_designated_by_user_id=OLD.user_id
    AND page_id IN (SELECT id FROM pages WHERE owner_crew_id=OLD.crew_id AND COALESCE(owner_user_id,'')='')
    AND NOT EXISTS(SELECT 1 FROM pages op WHERE op.id=page_panels.page_id AND (
  (COALESCE(op.owner_user_id,'')<>'' AND op.owner_user_id=page_panels.producer_designated_by_user_id)
  OR (COALESCE(op.owner_user_id,'')='' AND COALESCE(op.owner_crew_id,'')<>''
      AND EXISTS(SELECT 1 FROM crew_members ocm JOIN crews oc ON oc.id=ocm.crew_id AND oc.deleted_at IS NULL AND oc.workspace_id=op.workspace_id WHERE ocm.crew_id=op.owner_crew_id AND ocm.user_id=page_panels.producer_designated_by_user_id))));
END;

-- The owning crew was deleted: its members' delegations end. Restoring the
-- crew does not revive them.
CREATE TRIGGER page_grant_agent_delegation_crew_deleted AFTER UPDATE OF deleted_at ON crews
WHEN OLD.deleted_at IS NULL AND NEW.deleted_at IS NOT NULL
BEGIN
 UPDATE page_grants SET retired_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
  WHERE retired_at IS NULL AND ((subject_type='agent' AND level IN ('write','produce')) OR (subject_type='crew' AND level='produce'))
    AND page_id IN (SELECT id FROM pages WHERE owner_crew_id=NEW.id AND COALESCE(owner_user_id,'')='');
 UPDATE page_panels SET producer_retired_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
  WHERE producer_kind='agent' AND producer_retired_at IS NULL
    AND page_id IN (SELECT id FROM pages WHERE owner_crew_id=NEW.id AND COALESCE(owner_user_id,'')='');
END;

-- An owning crew moved to another workspace owns nothing on the page it left.
-- Nothing moves a crew today; this keeps a move back from reviving anything.
CREATE TRIGGER page_grant_agent_delegation_crew_workspace AFTER UPDATE OF workspace_id ON crews
WHEN OLD.workspace_id IS NOT NEW.workspace_id
BEGIN
 UPDATE page_grants SET retired_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
  WHERE retired_at IS NULL AND ((subject_type='agent' AND level IN ('write','produce')) OR (subject_type='crew' AND level='produce'))
    AND page_id IN (SELECT id FROM pages WHERE owner_crew_id=NEW.id AND COALESCE(owner_user_id,'')='');
 UPDATE page_panels SET producer_retired_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
  WHERE producer_kind='agent' AND producer_retired_at IS NULL
    AND page_id IN (SELECT id FROM pages WHERE owner_crew_id=NEW.id AND COALESCE(owner_user_id,'')='');
END;

-- Every live non-owner-issued delegation stored today is judged once, here.
UPDATE page_grants SET retired_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
 WHERE retired_at IS NULL AND ((subject_type='agent' AND level IN ('write','produce')) OR (subject_type='crew' AND level='produce'))
   AND NOT EXISTS(SELECT 1 FROM pages op WHERE op.id=page_grants.page_id AND (
    (COALESCE(op.owner_user_id,'')<>'' AND op.owner_user_id=page_grants.granted_by_user_id)
    OR (COALESCE(op.owner_user_id,'')='' AND COALESCE(op.owner_crew_id,'')<>''
        AND EXISTS(SELECT 1 FROM crew_members ocm JOIN crews oc ON oc.id=ocm.crew_id AND oc.deleted_at IS NULL AND oc.workspace_id=op.workspace_id WHERE ocm.crew_id=op.owner_crew_id AND ocm.user_id=page_grants.granted_by_user_id))));

-- Who designated each panel's producer. ON DELETE SET NULL: a designation
-- whose designator is gone is nobody's, and the update trigger retires it.
ALTER TABLE page_panels ADD COLUMN producer_designated_by_user_id TEXT REFERENCES users(id) ON DELETE SET NULL;
ALTER TABLE page_panels ADD COLUMN producer_retired_at TEXT;
-- The designator's deletion sets the column NULL and the crew triggers look
-- designations up by it; both need the index.
CREATE INDEX idx_page_panels_producer_designator ON page_panels(producer_designated_by_user_id);

CREATE TRIGGER page_panel_agent_producer_retired AFTER UPDATE OF producer_retired_at ON page_panels
WHEN OLD.producer_retired_at IS NULL AND NEW.producer_retired_at IS NOT NULL AND NEW.producer_kind='agent'
BEGIN
 INSERT INTO audit_logs(id,workspace_id,user_id,action,entity_type,entity_id,metadata,created_at)
  SELECT lower(hex(randomblob(16))),p.workspace_id,NULL,'page.agent_producer.retired_not_owner','page',NEW.page_id,
         json_object('panel_id',NEW.panel_id,'producer','agent/'||NEW.producer_ref,
                     'designated_by_user_id',NEW.producer_designated_by_user_id,
                     'owner_user_id',p.owner_user_id,'owner_crew_id',p.owner_crew_id,
                     'reason','only the artifact owner names an agent as a panel producer (R71 0.14/1)'),
         strftime('%Y-%m-%dT%H:%M:%fZ','now')
  FROM pages p WHERE p.id=NEW.page_id;
END;

CREATE TRIGGER page_panel_agent_producer_insert AFTER INSERT ON page_panels
WHEN NEW.producer_kind='agent' AND NEW.producer_retired_at IS NULL AND NOT EXISTS(SELECT 1 FROM pages op WHERE op.id=NEW.page_id AND (
  (COALESCE(op.owner_user_id,'')<>'' AND op.owner_user_id=NEW.producer_designated_by_user_id)
  OR (COALESCE(op.owner_user_id,'')='' AND COALESCE(op.owner_crew_id,'')<>''
      AND EXISTS(SELECT 1 FROM crew_members ocm JOIN crews oc ON oc.id=ocm.crew_id AND oc.deleted_at IS NULL AND oc.workspace_id=op.workspace_id WHERE ocm.crew_id=op.owner_crew_id AND ocm.user_id=NEW.producer_designated_by_user_id))))
BEGIN
 UPDATE page_panels SET producer_retired_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE rowid=NEW.rowid;
END;

CREATE TRIGGER page_panel_agent_producer_update AFTER UPDATE ON page_panels
WHEN NEW.producer_kind='agent' AND NEW.producer_retired_at IS NULL AND NOT EXISTS(SELECT 1 FROM pages op WHERE op.id=NEW.page_id AND (
  (COALESCE(op.owner_user_id,'')<>'' AND op.owner_user_id=NEW.producer_designated_by_user_id)
  OR (COALESCE(op.owner_user_id,'')='' AND COALESCE(op.owner_crew_id,'')<>''
      AND EXISTS(SELECT 1 FROM crew_members ocm JOIN crews oc ON oc.id=ocm.crew_id AND oc.deleted_at IS NULL AND oc.workspace_id=op.workspace_id WHERE ocm.crew_id=op.owner_crew_id AND ocm.user_id=NEW.producer_designated_by_user_id))))
BEGIN
 UPDATE page_panels SET producer_retired_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE rowid=NEW.rowid;
END;

-- Provenance of the agent producers stored today: the human author of the
-- first version in the latest unbroken run of versions naming this producer
-- for this panel. An agent author, or no version at all, is nobody. So is a
-- run that reaches back to the oldest kept version when that is not version
-- 1: history was trimmed (trimPageVersions) and whoever named the producer is
-- gone from it.
UPDATE page_panels SET producer_designated_by_user_id=(
  SELECT CASE
          WHEN pv.author_agent_id IS NOT NULL THEN NULL
          WHEN pv.seq > 1 AND NOT EXISTS(SELECT 1 FROM page_versions older WHERE older.page_id=pv.page_id AND older.seq < pv.seq) THEN NULL
          ELSE pv.author_user_id
         END
    FROM page_versions pv
   WHERE pv.page_id=page_panels.page_id
     AND pv.seq > COALESCE((SELECT max(pb.seq) FROM page_versions pb
                             WHERE pb.page_id=page_panels.page_id
                               AND NOT EXISTS(SELECT 1 FROM json_each(CASE WHEN json_valid(pb.spec_json) THEN pb.spec_json ELSE '{}' END,'$.spec.panels') j
                                              WHERE json_extract(j.value,'$.id')=page_panels.panel_id
                                                AND json_extract(j.value,'$.producer')='agent/'||page_panels.producer_ref)), 0)
   ORDER BY pv.seq LIMIT 1)
 WHERE producer_kind='agent';

-- Judged once: every stored agent designation the owner did not make.
UPDATE page_panels SET producer_retired_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
 WHERE producer_kind='agent' AND producer_retired_at IS NULL AND NOT EXISTS(SELECT 1 FROM pages op WHERE op.id=page_panels.page_id AND (
  (COALESCE(op.owner_user_id,'')<>'' AND op.owner_user_id=page_panels.producer_designated_by_user_id)
  OR (COALESCE(op.owner_user_id,'')='' AND COALESCE(op.owner_crew_id,'')<>''
      AND EXISTS(SELECT 1 FROM crew_members ocm JOIN crews oc ON oc.id=ocm.crew_id AND oc.deleted_at IS NULL AND oc.workspace_id=op.workspace_id WHERE ocm.crew_id=op.owner_crew_id AND ocm.user_id=page_panels.producer_designated_by_user_id))));
