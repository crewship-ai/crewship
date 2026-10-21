-- Runtime human-chat admissions; backup restoration requires fresh authority.
CREATE TABLE IF NOT EXISTS artifact_run_principals (
  run_id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
  agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
  crew_id TEXT NOT NULL REFERENCES crews(id) ON DELETE CASCADE,
  human_user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  chat_id TEXT NOT NULL REFERENCES chats(id) ON DELETE CASCADE,
  message_id TEXT NOT NULL,
  member_id TEXT NOT NULL,
  member_revision INTEGER NOT NULL,
  chat_generation TEXT NOT NULL,
  chat_revision INTEGER NOT NULL,
  expires_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_artifact_principal_workspace ON artifact_run_principals(workspace_id);

CREATE INDEX IF NOT EXISTS idx_artifact_principal_agent ON artifact_run_principals(agent_id);

CREATE INDEX IF NOT EXISTS idx_artifact_principal_crew ON artifact_run_principals(crew_id);

CREATE INDEX IF NOT EXISTS idx_artifact_principal_human ON artifact_run_principals(human_user_id);

CREATE INDEX IF NOT EXISTS idx_artifact_principal_chat ON artifact_run_principals(chat_id);

CREATE INDEX IF NOT EXISTS idx_artifact_run_principals_expiry ON artifact_run_principals(expires_at);
