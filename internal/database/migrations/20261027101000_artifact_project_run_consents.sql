-- Explicit human permission for one exact live chat run. The run principal is
-- provenance; this separate record is the scope of the artifact mandate.
CREATE TABLE artifact_project_run_consents (
 run_id TEXT PRIMARY KEY REFERENCES artifact_run_principals(run_id) ON DELETE CASCADE,
 workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 human_user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 chat_id TEXT NOT NULL REFERENCES chats(id) ON DELETE CASCADE,
 message_id TEXT NOT NULL REFERENCES conversation_messages(id) ON DELETE CASCADE,
 source_kind TEXT NOT NULL DEFAULT 'manual' CHECK(source_kind IN ('manual','chat')),
 expires_at TEXT NOT NULL,
 revoked_at TEXT,
 result_page_id TEXT REFERENCES pages(id) ON DELETE CASCADE,
 created_at TEXT NOT NULL
);
CREATE INDEX artifact_project_run_consents_page ON artifact_project_run_consents(workspace_id,result_page_id);
CREATE TRIGGER artifact_project_consent_identity_immutable BEFORE UPDATE OF run_id,workspace_id,human_user_id,chat_id,message_id,source_kind,expires_at,created_at ON artifact_project_run_consents
BEGIN SELECT RAISE(ABORT,'artifact consent identity is immutable'); END;
CREATE TRIGGER artifact_project_consent_page_immutable BEFORE UPDATE OF result_page_id ON artifact_project_run_consents
WHEN OLD.result_page_id IS NOT NULL AND NEW.result_page_id IS NOT OLD.result_page_id
BEGIN SELECT RAISE(ABORT,'artifact consent Page binding is immutable'); END;
CREATE TRIGGER artifact_project_consent_revocation_terminal BEFORE UPDATE OF revoked_at ON artifact_project_run_consents
WHEN OLD.revoked_at IS NOT NULL AND NEW.revoked_at IS NOT OLD.revoked_at
BEGIN SELECT RAISE(ABORT,'artifact consent revocation is terminal'); END;

-- Trusted pre-send intent, retained before starting a model. Generic transcript
-- metadata cannot insert this table; one original message binds to one run.
CREATE TABLE artifact_project_message_intents (
 message_id TEXT PRIMARY KEY REFERENCES conversation_messages(id) ON DELETE CASCADE,
 workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 human_user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 chat_id TEXT NOT NULL REFERENCES chats(id) ON DELETE CASCADE,
 submission_id TEXT NOT NULL,
 page_id TEXT REFERENCES pages(id) ON DELETE CASCADE,
 comment_id TEXT REFERENCES artifact_project_comments(id) ON DELETE CASCADE,
 base_version INTEGER NOT NULL DEFAULT 0 CHECK(base_version>=0),
 run_id TEXT UNIQUE,
 revoked_at TEXT,
 created_at TEXT NOT NULL,
 UNIQUE(workspace_id,human_user_id,chat_id,submission_id),
 CHECK((page_id IS NULL AND comment_id IS NULL AND base_version=0) OR (page_id IS NOT NULL AND base_version>0))
);
CREATE TRIGGER artifact_project_message_intent_immutable BEFORE UPDATE OF message_id,workspace_id,human_user_id,chat_id,submission_id,page_id,comment_id,base_version,created_at ON artifact_project_message_intents
BEGIN SELECT RAISE(ABORT,'artifact message intent is immutable'); END;
CREATE TRIGGER artifact_project_message_run_immutable BEFORE UPDATE OF run_id ON artifact_project_message_intents
WHEN OLD.run_id IS NOT NULL AND NEW.run_id IS NOT OLD.run_id
BEGIN SELECT RAISE(ABORT,'artifact message run binding is immutable'); END;
CREATE TRIGGER artifact_project_message_revocation_terminal BEFORE UPDATE OF revoked_at ON artifact_project_message_intents
WHEN OLD.revoked_at IS NOT NULL AND NEW.revoked_at IS NOT OLD.revoked_at
BEGIN SELECT RAISE(ABORT,'artifact message intent revocation is terminal'); END;
