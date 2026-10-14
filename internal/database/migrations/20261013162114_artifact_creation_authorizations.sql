-- Explicit human opt-in, default off, limited to one artifact in one live run.
-- This authority is runtime-only and must never survive backup/restore.
CREATE TABLE artifact_creation_authorizations (
 run_id TEXT PRIMARY KEY REFERENCES artifact_run_principals(run_id) ON DELETE CASCADE,
 authorized_by_user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 expires_at TEXT NOT NULL,
 granted_at TEXT NOT NULL,
 revoked_at TEXT,
 revoked_by_user_id TEXT REFERENCES users(id) ON DELETE SET NULL,
 consumed_at TEXT,
 consumed_page_id TEXT REFERENCES pages(id) ON DELETE SET NULL
);
CREATE INDEX idx_artifact_creation_authorizer ON artifact_creation_authorizations(authorized_by_user_id);
CREATE INDEX idx_artifact_creation_revoker ON artifact_creation_authorizations(revoked_by_user_id);
CREATE INDEX idx_artifact_creation_page ON artifact_creation_authorizations(consumed_page_id);
-- Durable attribution and retry result, not authority. Run IDs remain opaque
-- history when a restored instance deliberately drops the live run admission.
CREATE TABLE page_artifact_creations (
 page_id TEXT PRIMARY KEY REFERENCES pages(id) ON DELETE CASCADE,
 run_id TEXT NOT NULL,
 agent_id TEXT REFERENCES agents(id) ON DELETE SET NULL,
 human_user_id TEXT REFERENCES users(id) ON DELETE SET NULL,
 mutation_id TEXT NOT NULL,
 request_digest TEXT NOT NULL,
 created_at TEXT NOT NULL,
 UNIQUE(run_id,mutation_id)
);
CREATE INDEX idx_page_artifact_creation_agent ON page_artifact_creations(agent_id);
CREATE INDEX idx_page_artifact_creation_human ON page_artifact_creations(human_user_id);
