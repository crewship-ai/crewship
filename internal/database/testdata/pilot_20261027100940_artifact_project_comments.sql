-- Version-bound feedback. Sending creates a durable task in the same commit;
-- a stale target is never silently relocated to a newer project version.
CREATE TABLE artifact_project_comments (
 id TEXT PRIMARY KEY,
 workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 page_id TEXT NOT NULL REFERENCES pages(id) ON DELETE CASCADE,
 version INTEGER NOT NULL,
 path TEXT NOT NULL,
 selector TEXT NOT NULL,
 quote TEXT NOT NULL,
 text TEXT NOT NULL,
 author TEXT NOT NULL,
 task_id TEXT,
 created_at TEXT NOT NULL,
 FOREIGN KEY(workspace_id,page_id,version) REFERENCES artifact_versions(workspace_id,page_id,version)
);
CREATE INDEX artifact_project_comments_page ON artifact_project_comments(workspace_id,page_id,created_at);
CREATE TABLE artifact_project_comment_tasks (
 id TEXT PRIMARY KEY,
 comment_id TEXT NOT NULL UNIQUE REFERENCES artifact_project_comments(id) ON DELETE CASCADE,
 workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 page_id TEXT NOT NULL REFERENCES pages(id) ON DELETE CASCADE,
 base_version INTEGER NOT NULL,
 status TEXT NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','working','answered','needs_clarification')),
 active_run_id TEXT,
 created_at TEXT NOT NULL
);
CREATE TABLE artifact_project_comment_replies (
 id TEXT PRIMARY KEY,
 comment_id TEXT NOT NULL REFERENCES artifact_project_comments(id) ON DELETE CASCADE,
 needs_clarification INTEGER NOT NULL DEFAULT 0 CHECK(needs_clarification IN (0,1)),
 version INTEGER NOT NULL,
 text TEXT NOT NULL,
 author TEXT NOT NULL,
 created_at TEXT NOT NULL
);
CREATE TRIGGER artifact_project_comments_scope BEFORE INSERT ON artifact_project_comments
WHEN NOT EXISTS(SELECT 1 FROM pages WHERE id=NEW.page_id AND workspace_id=NEW.workspace_id)
BEGIN SELECT RAISE(ABORT,'artifact comment scope mismatch'); END;
CREATE TABLE artifact_project_origins (
 page_id TEXT PRIMARY KEY REFERENCES pages(id) ON DELETE CASCADE,
 workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 human_user_id TEXT NOT NULL,
 chat_id TEXT REFERENCES chats(id) ON DELETE SET NULL,
 agent_id TEXT REFERENCES agents(id) ON DELETE SET NULL,
 message_id TEXT NOT NULL,
 created_at TEXT NOT NULL
);
INSERT INTO feature_flags(id,key,description,enabled,created_at,updated_at)
 VALUES('ff_artifact_projects','artifact_projects','Multi-screen HTML projects with immutable versions and anchored feedback',0,datetime('now'),datetime('now'))
 ON CONFLICT(key) DO NOTHING;
-- Parent deletion and history lookup must not scan the whole feedback corpus.
CREATE INDEX artifact_project_replies_comment ON artifact_project_comment_replies(comment_id,created_at,id);
CREATE INDEX artifact_project_tasks_page ON artifact_project_comment_tasks(page_id);
CREATE INDEX artifact_project_tasks_workspace ON artifact_project_comment_tasks(workspace_id);
CREATE INDEX artifact_project_comments_page_id ON artifact_project_comments(page_id);
CREATE INDEX artifact_project_comments_version ON artifact_project_comments(version);
CREATE INDEX artifact_project_origins_agent ON artifact_project_origins(agent_id);
CREATE INDEX artifact_project_origins_chat ON artifact_project_origins(chat_id);
CREATE INDEX artifact_project_origins_workspace ON artifact_project_origins(workspace_id);
