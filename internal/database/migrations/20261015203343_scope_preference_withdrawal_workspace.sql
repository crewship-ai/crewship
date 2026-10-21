-- A workspace administrator may withdraw memory only in that workspace.
-- Preserve foreign-workspace values; the existing recall ceiling still applies.
-- Previously erased values cannot be reconstructed by this migration.
DROP TRIGGER feedback_preference_restricted_insert;
DROP TRIGGER feedback_preference_restricted_update;

CREATE TRIGGER feedback_preference_restricted_insert AFTER INSERT ON workspace_members
WHEN NEW.access_mode='restricted'
BEGIN
 UPDATE agent_user_preferences SET status='withdrawn',value='',reason='',revision=revision+1,
 updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
 WHERE user_id=NEW.user_id AND workspace_id=NEW.workspace_id AND status!='withdrawn';
END;

CREATE TRIGGER feedback_preference_restricted_update AFTER UPDATE OF access_mode ON workspace_members
WHEN NEW.access_mode='restricted' AND OLD.access_mode!='restricted'
BEGIN
 UPDATE agent_user_preferences SET status='withdrawn',value='',reason='',revision=revision+1,
 updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
 WHERE user_id=NEW.user_id AND workspace_id=NEW.workspace_id AND status!='withdrawn';
END;

UPDATE agent_user_preferences SET status='withdrawn',value='',reason='',revision=revision+1,
 updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
WHERE status!='withdrawn' AND EXISTS (
 SELECT 1 FROM workspace_members wm WHERE wm.user_id=agent_user_preferences.user_id
 AND wm.workspace_id=agent_user_preferences.workspace_id AND wm.access_mode='restricted'
);
