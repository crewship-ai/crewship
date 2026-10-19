-- A source conversation's privacy outlives creation permission, results and
-- their cascades. NULL is ordinary; empty is sealed with inconsistent legacy
-- provenance. This historical identity deliberately has no user-delete FK.
ALTER TABLE chats ADD COLUMN private_artifact_author_user_id TEXT;
UPDATE chats AS c SET private_artifact_author_user_id=CASE
 WHEN c.visibility='private' AND COALESCE(c.created_by,'')<>'' AND COALESCE(c.agent_id,'')<>''
 AND NOT (EXISTS(SELECT 1 FROM document_creation_consents s LEFT JOIN conversation_messages m ON m.id=s.message_id
 WHERE s.chat_id=c.id AND (s.workspace_id IS NOT c.workspace_id OR s.human_user_id IS NOT c.created_by
 OR m.id IS NULL OR m.session_id IS NOT c.id OR m.agent_id IS NOT c.agent_id OR m.author_user_id IS NOT s.human_user_id OR m.role IS NOT 'user'))
 OR EXISTS(SELECT 1 FROM document_creations d WHERE d.source_chat_id=c.id AND
 (d.workspace_id IS NOT c.workspace_id OR d.human_user_id IS NOT c.created_by OR d.agent_id IS NOT c.agent_id))) THEN c.created_by ELSE '' END
 WHERE EXISTS(SELECT 1 FROM document_creation_consents s WHERE s.chat_id=c.id) OR EXISTS(SELECT 1 FROM document_creations d WHERE d.source_chat_id=c.id);
CREATE TRIGGER chat_private_artifact_context_immutable
 BEFORE UPDATE OF private_artifact_author_user_id ON chats
 WHEN OLD.private_artifact_author_user_id IS NOT NULL
 AND NEW.private_artifact_author_user_id IS NOT OLD.private_artifact_author_user_id
 BEGIN SELECT RAISE(ABORT,'private artifact conversation classification is immutable'); END;
CREATE TRIGGER document_consent_seals_private_context AFTER INSERT ON document_creation_consents
 BEGIN
 UPDATE chats SET private_artifact_author_user_id=CASE
 WHEN visibility='private' AND created_by=NEW.human_user_id AND workspace_id=NEW.workspace_id
 AND EXISTS(SELECT 1 FROM conversation_messages m WHERE m.id=NEW.message_id AND m.session_id=chats.id
 AND m.agent_id=chats.agent_id AND m.author_user_id=NEW.human_user_id AND m.role='user')
 THEN created_by ELSE '' END
 WHERE id=NEW.chat_id AND private_artifact_author_user_id IS NULL;
 END;
CREATE TRIGGER document_creation_seals_private_context_insert AFTER INSERT ON document_creations
 WHEN NEW.source_chat_id IS NOT NULL
 BEGIN
 UPDATE chats SET private_artifact_author_user_id=CASE
 WHEN visibility='private' AND created_by=NEW.human_user_id AND workspace_id=NEW.workspace_id AND agent_id=NEW.agent_id
 THEN created_by ELSE '' END
 WHERE id=NEW.source_chat_id AND private_artifact_author_user_id IS NULL;
 END;
CREATE TRIGGER document_creation_seals_private_context_update AFTER UPDATE OF source_chat_id,workspace_id,human_user_id,agent_id ON document_creations
 WHEN NEW.source_chat_id IS NOT NULL
 BEGIN
 UPDATE chats SET private_artifact_author_user_id=CASE
 WHEN visibility='private' AND created_by=NEW.human_user_id AND workspace_id=NEW.workspace_id AND agent_id=NEW.agent_id
 THEN created_by ELSE '' END
 WHERE id=NEW.source_chat_id AND private_artifact_author_user_id IS NULL;
 END;
