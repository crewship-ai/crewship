-- Files kept with an agent: a Sheet or other page a person gave an
-- agent in a chat stays listed for that agent in a later chat. The row grants nothing: the agent
-- reads a listed page only while its run's person may read it, in that person's private chat with
-- this agent (and every such read is audited); writing still needs the page attached to the
-- current message with consent. Additive, no backfill. Removing it sets removed_at.
CREATE TABLE IF NOT EXISTS agent_files (
 agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
 page_id TEXT NOT NULL REFERENCES pages(id) ON DELETE CASCADE,
 workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 added_by TEXT REFERENCES users(id) ON DELETE SET NULL,
 added_at TEXT NOT NULL,
 removed_at TEXT,
 removed_by TEXT REFERENCES users(id) ON DELETE SET NULL,
 PRIMARY KEY(agent_id, page_id)
);
CREATE INDEX IF NOT EXISTS agent_files_page ON agent_files(page_id);
CREATE INDEX IF NOT EXISTS agent_files_workspace ON agent_files(workspace_id);
CREATE INDEX IF NOT EXISTS agent_files_added_by ON agent_files(added_by);
CREATE INDEX IF NOT EXISTS agent_files_removed_by ON agent_files(removed_by);
CREATE TRIGGER IF NOT EXISTS agent_files_same_workspace BEFORE INSERT ON agent_files
WHEN NOT EXISTS(
 SELECT 1 FROM agents a JOIN pages p ON p.id=NEW.page_id AND p.workspace_id=NEW.workspace_id
 WHERE a.id=NEW.agent_id AND a.workspace_id=NEW.workspace_id)
BEGIN SELECT RAISE(ABORT,'an agent file names an agent and a page of the same workspace'); END;
CREATE TRIGGER IF NOT EXISTS agent_files_keep_identity BEFORE UPDATE OF agent_id, page_id, workspace_id ON agent_files
BEGIN SELECT RAISE(ABORT,'an agent file keeps its agent, page and workspace'); END;
