-- R71 §0.14/1: only the artifact's owner delegates Edit to an agent, and that
-- covers the artifact's data as well as its spec: an agent `write` grant edits
-- the page, an agent `produce` grant pushes payloads into its panels. The owner
-- is the owning user, or a member of the owning crew when no user owns it and
-- the crew is not deleted and belongs to the page's workspace: a deleted crew
-- owns nothing it can delegate. internal/api pageOwnerForDelegation and
-- agentEditIssuerNotOwnerSQL read the same definition.
--
-- PUT /grants refuses anyone else. A row that is or becomes non-owner-issued
-- by any other path (an older build, a restore, an ownership transfer, the
-- issuer leaving the owning crew, the owning crew being deleted) is retired here, in the transaction of the
-- change, with an audit entry. Retired is for good: the issuer owning the page
-- later does not revive it; only a new grant by the then-owner (the PUT clears
-- the marker as a new operation) delegates again. Revoke still deletes the row.

-- Why an issuance was retired, read by the grants list. NULL is the issuer
-- standing rule of 20261106225704.
ALTER TABLE page_grant_retired_issuances ADD COLUMN reason TEXT;

-- Reason and audit at the moment of retirement, whatever retired the row.
CREATE TRIGGER page_grant_agent_delegation_retired AFTER UPDATE OF retired_at ON page_grants
WHEN OLD.retired_at IS NULL AND NEW.retired_at IS NOT NULL
 AND NEW.subject_type='agent' AND NEW.level IN ('write','produce')
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
WHEN NEW.retired_at IS NULL AND NEW.subject_type='agent' AND NEW.level IN ('write','produce')
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
WHEN NEW.retired_at IS NULL AND NEW.subject_type='agent' AND NEW.level IN ('write','produce')
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
  WHERE page_id=NEW.id AND retired_at IS NULL AND subject_type='agent' AND level IN ('write','produce')
    AND NOT ((COALESCE(NEW.owner_user_id,'')<>'' AND NEW.owner_user_id=page_grants.granted_by_user_id)
     OR (COALESCE(NEW.owner_user_id,'')='' AND COALESCE(NEW.owner_crew_id,'')<>''
         AND EXISTS(SELECT 1 FROM crew_members ocm JOIN crews oc ON oc.id=ocm.crew_id AND oc.deleted_at IS NULL AND oc.workspace_id=NEW.workspace_id WHERE ocm.crew_id=NEW.owner_crew_id AND ocm.user_id=page_grants.granted_by_user_id)));
END;

-- The issuer left the owning crew.
CREATE TRIGGER page_grant_agent_delegation_crew_member_delete AFTER DELETE ON crew_members
BEGIN
 UPDATE page_grants SET retired_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
  WHERE retired_at IS NULL AND subject_type='agent' AND level IN ('write','produce') AND granted_by_user_id=OLD.user_id
    AND page_id IN (SELECT id FROM pages WHERE owner_crew_id=OLD.crew_id AND COALESCE(owner_user_id,'')='')
    AND NOT EXISTS(SELECT 1 FROM crew_members ocm JOIN crews oc ON oc.id=ocm.crew_id AND oc.deleted_at IS NULL WHERE ocm.crew_id=OLD.crew_id AND ocm.user_id=OLD.user_id);
END;

CREATE TRIGGER page_grant_agent_delegation_crew_member_update AFTER UPDATE OF crew_id,user_id ON crew_members
WHEN OLD.crew_id IS NOT NEW.crew_id OR OLD.user_id IS NOT NEW.user_id
BEGIN
 UPDATE page_grants SET retired_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
  WHERE retired_at IS NULL AND subject_type='agent' AND level IN ('write','produce') AND granted_by_user_id=OLD.user_id
    AND page_id IN (SELECT id FROM pages WHERE owner_crew_id=OLD.crew_id AND COALESCE(owner_user_id,'')='')
    AND NOT EXISTS(SELECT 1 FROM crew_members ocm JOIN crews oc ON oc.id=ocm.crew_id AND oc.deleted_at IS NULL WHERE ocm.crew_id=OLD.crew_id AND ocm.user_id=OLD.user_id);
END;

-- The owning crew was deleted: its members' delegations end. Restoring the
-- crew does not revive them.
CREATE TRIGGER page_grant_agent_delegation_crew_deleted AFTER UPDATE OF deleted_at ON crews
WHEN OLD.deleted_at IS NULL AND NEW.deleted_at IS NOT NULL
BEGIN
 UPDATE page_grants SET retired_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
  WHERE retired_at IS NULL AND subject_type='agent' AND level IN ('write','produce')
    AND page_id IN (SELECT id FROM pages WHERE owner_crew_id=NEW.id AND COALESCE(owner_user_id,'')='');
END;

-- An owning crew moved to another workspace owns nothing on the page it left.
-- Nothing moves a crew today; this keeps a move back from reviving anything.
CREATE TRIGGER page_grant_agent_delegation_crew_workspace AFTER UPDATE OF workspace_id ON crews
WHEN OLD.workspace_id IS NOT NEW.workspace_id
BEGIN
 UPDATE page_grants SET retired_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
  WHERE retired_at IS NULL AND subject_type='agent' AND level IN ('write','produce')
    AND page_id IN (SELECT id FROM pages WHERE owner_crew_id=NEW.id AND COALESCE(owner_user_id,'')='');
END;

-- Every live non-owner-issued delegation stored today is judged once, here.
UPDATE page_grants SET retired_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
 WHERE retired_at IS NULL AND subject_type='agent' AND level IN ('write','produce')
   AND NOT EXISTS(SELECT 1 FROM pages op WHERE op.id=page_grants.page_id AND (
    (COALESCE(op.owner_user_id,'')<>'' AND op.owner_user_id=page_grants.granted_by_user_id)
    OR (COALESCE(op.owner_user_id,'')='' AND COALESCE(op.owner_crew_id,'')<>''
        AND EXISTS(SELECT 1 FROM crew_members ocm JOIN crews oc ON oc.id=ocm.crew_id AND oc.deleted_at IS NULL AND oc.workspace_id=op.workspace_id WHERE ocm.crew_id=op.owner_crew_id AND ocm.user_id=page_grants.granted_by_user_id))));
