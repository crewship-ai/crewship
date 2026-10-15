-- Explicit preferences use a structured source of truth, never peer Markdown.
ALTER TABLE message_feedback ADD COLUMN revision INTEGER NOT NULL DEFAULT 1;
CREATE TABLE agent_user_preferences (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    feedback_id TEXT NOT NULL,
    source_revision INTEGER NOT NULL,
    revision INTEGER NOT NULL DEFAULT 1,
    chat_id TEXT NOT NULL,
    message_id TEXT NOT NULL,
    reason TEXT NOT NULL,
    value TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL CHECK(status IN ('feedback_only','processing','saved','not_eligible','disabled','failed','withdrawn')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE INDEX idx_agent_user_preferences_subject ON agent_user_preferences(workspace_id,agent_id,user_id,status);
CREATE TRIGGER feedback_preference_insert AFTER INSERT ON message_feedback
WHEN NEW.user_id IS NOT NULL AND COALESCE(NEW.reason,'')!='' AND NEW.signal IN ('helpful','not_helpful')
BEGIN
    INSERT INTO agent_user_preferences(id,workspace_id,agent_id,user_id,feedback_id,source_revision,chat_id,message_id,reason,status)
    SELECT NEW.id,NEW.workspace_id,c.agent_id,NEW.user_id,NEW.id,NEW.revision,c.id,NEW.message_id,COALESCE(NEW.reason,''),
        CASE WHEN COALESCE(NEW.reason,'')='' OR NEW.signal NOT IN ('helpful','not_helpful') THEN 'feedback_only' ELSE 'processing' END
    FROM chats c WHERE c.id=NEW.chat_id AND c.workspace_id=NEW.workspace_id;
END;
CREATE TRIGGER feedback_preference_update AFTER UPDATE OF revision ON message_feedback
WHEN NEW.revision != OLD.revision
BEGIN
    UPDATE agent_user_preferences SET source_revision=NEW.revision,revision=revision+1,
        reason=COALESCE(NEW.reason,''),value='',status=CASE WHEN COALESCE(NEW.reason,'')='' OR NEW.signal NOT IN ('helpful','not_helpful') THEN 'feedback_only' ELSE 'processing' END,
        updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE feedback_id=NEW.id;
    INSERT INTO agent_user_preferences(id,workspace_id,agent_id,user_id,feedback_id,source_revision,chat_id,message_id,reason,status)
    SELECT NEW.id,NEW.workspace_id,c.agent_id,NEW.user_id,NEW.id,NEW.revision,c.id,NEW.message_id,NEW.reason,'processing'
    FROM chats c WHERE c.id=NEW.chat_id AND c.workspace_id=NEW.workspace_id
      AND NEW.user_id IS NOT NULL AND COALESCE(NEW.reason,'')!='' AND NEW.signal IN ('helpful','not_helpful')
      AND NOT EXISTS(SELECT 1 FROM agent_user_preferences p WHERE p.id=NEW.id);
END;
CREATE TRIGGER feedback_preference_withdraw AFTER DELETE ON message_feedback
BEGIN
    UPDATE agent_user_preferences SET status='withdrawn',value='',reason='',revision=revision+1,
        updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE feedback_id=OLD.id;
END;

CREATE TRIGGER feedback_preference_opt_out_insert AFTER INSERT ON user_peer_consent WHEN NEW.opted_out=1
BEGIN
 UPDATE agent_user_preferences SET status='withdrawn',value='',reason='',revision=revision+1
 WHERE workspace_id=NEW.workspace_id AND user_id=NEW.user_id AND status!='withdrawn';
END;
CREATE TRIGGER feedback_preference_opt_out_update AFTER UPDATE OF opted_out ON user_peer_consent WHEN NEW.opted_out=1
BEGIN
 UPDATE agent_user_preferences SET status='withdrawn',value='',reason='',revision=revision+1
 WHERE workspace_id=NEW.workspace_id AND user_id=NEW.user_id AND status!='withdrawn';
END;

CREATE TRIGGER feedback_preference_audit AFTER UPDATE OF status ON agent_user_preferences
WHEN NEW.status IN ('saved','withdrawn') AND OLD.status!=NEW.status
BEGIN
 INSERT INTO peer_card_audit(id,workspace_id,actor_kind,action,target_user_id,agent_id,metadata)
 VALUES(lower(hex(randomblob(16))),NEW.workspace_id,'system',CASE WHEN NEW.status='saved' THEN 'write' ELSE 'delete' END,
 NEW.user_id,NEW.agent_id,json_object('kind','feedback_preference','feedback_id',NEW.feedback_id,'source_revision',NEW.source_revision,'revision',NEW.revision));
END;
