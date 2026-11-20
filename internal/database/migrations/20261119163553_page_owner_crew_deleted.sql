-- A deleted crew owns nothing (#5259). Crew deletion through the API removes
-- the crew's crew_members rows, but rows can outlive a crew's tombstone (a
-- restore, an older build, a direct write), and the issuer_standing crew arm
-- of page_grant_verdicts counted them: a human grant issued by a deleted
-- owning crew's member stayed live. The arm now reads ownership as
-- pageOwnerForDelegation and isPageOwner do: a member of the owning crew
-- while that crew is not deleted and belongs to the page's workspace. Every
-- other column is unchanged from 20261106225704.
DROP VIEW page_grant_verdicts;
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
  WHEN p.owner_crew_id IS NOT NULL AND EXISTS(SELECT 1 FROM crew_members cm JOIN crews oc ON oc.id=cm.crew_id AND oc.deleted_at IS NULL AND oc.workspace_id=p.workspace_id
       WHERE cm.crew_id=p.owner_crew_id AND cm.user_id=g.granted_by_user_id) THEN 1
  ELSE 0
 END AS issuer_standing
FROM page_grants g
JOIN pages p ON p.id=g.page_id
LEFT JOIN workspace_members wm ON wm.user_id=g.granted_by_user_id AND wm.workspace_id=p.workspace_id
LEFT JOIN users actor ON actor.id=g.granted_by_user_id;

-- The view is now an input that can take standing away: deleting the owning
-- crew, or moving it out of the page's workspace, ends the standing its
-- members held through it, in the transaction of the change, like the other
-- retirement triggers of 20261106225704. Restoring the crew does not revive
-- a retired grant.
CREATE TRIGGER page_grant_retire_crew_deleted AFTER UPDATE OF deleted_at ON crews
WHEN OLD.deleted_at IS NULL AND NEW.deleted_at IS NOT NULL
BEGIN
 UPDATE page_grants SET retired_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
  WHERE rowid IN (SELECT v.grant_rowid FROM page_grant_verdicts v JOIN pages p ON p.id=v.page_id
                  WHERE v.retired_at IS NULL AND v.issuer_standing=0 AND p.owner_crew_id=NEW.id);
END;

CREATE TRIGGER page_grant_retire_crew_workspace AFTER UPDATE OF workspace_id ON crews
WHEN OLD.workspace_id IS NOT NEW.workspace_id
BEGIN
 UPDATE page_grants SET retired_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
  WHERE rowid IN (SELECT v.grant_rowid FROM page_grant_verdicts v JOIN pages p ON p.id=v.page_id
                  WHERE v.retired_at IS NULL AND v.issuer_standing=0 AND p.owner_crew_id=NEW.id);
END;

-- A live grant stored today whose issuer held standing only through a
-- deleted (or foreign) owning crew is retired once, here. Nothing else is
-- touched: the sweep is limited to pages whose owning crew is deleted or in
-- another workspace.
UPDATE page_grants SET retired_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
 WHERE rowid IN (SELECT v.grant_rowid FROM page_grant_verdicts v JOIN pages p ON p.id=v.page_id
                 JOIN crews oc ON oc.id=p.owner_crew_id
                 WHERE v.retired_at IS NULL AND v.issuer_standing=0
                   AND (oc.deleted_at IS NOT NULL OR oc.workspace_id<>p.workspace_id));
