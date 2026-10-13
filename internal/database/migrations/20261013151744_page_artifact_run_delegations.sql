-- Narrow immutable human admission for chat runs, separate from mutable run metadata.
CREATE TABLE artifact_run_principals (
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
CREATE TABLE page_artifact_run_delegations (
 page_id TEXT NOT NULL REFERENCES pages(id) ON DELETE CASCADE,
 run_id TEXT NOT NULL REFERENCES artifact_run_principals(run_id) ON DELETE CASCADE,
 granted_by_user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 expires_at TEXT NOT NULL,
 granted_at TEXT NOT NULL,
 revoked_at TEXT,
 revoked_by_user_id TEXT REFERENCES users(id) ON DELETE SET NULL,
 PRIMARY KEY(page_id,run_id)
);
ALTER TABLE page_artifact_revisions ADD COLUMN author_agent_id TEXT REFERENCES agents(id) ON DELETE SET NULL;
ALTER TABLE page_artifact_revisions ADD COLUMN author_run_id TEXT;
CREATE INDEX idx_artifact_principal_workspace ON artifact_run_principals(workspace_id);
CREATE INDEX idx_artifact_principal_agent ON artifact_run_principals(agent_id);
CREATE INDEX idx_artifact_principal_crew ON artifact_run_principals(crew_id);
CREATE INDEX idx_artifact_principal_human ON artifact_run_principals(human_user_id);
CREATE INDEX idx_artifact_principal_chat ON artifact_run_principals(chat_id);
CREATE INDEX idx_artifact_delegation_run ON page_artifact_run_delegations(run_id);
CREATE INDEX idx_artifact_delegation_human ON page_artifact_run_delegations(granted_by_user_id);
CREATE INDEX idx_artifact_revision_agent ON page_artifact_revisions(author_agent_id);
CREATE INDEX idx_artifact_delegation_revoker ON page_artifact_run_delegations(revoked_by_user_id);
