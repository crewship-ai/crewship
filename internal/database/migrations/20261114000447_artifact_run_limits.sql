-- Agent access modes, enforcement (D1, #5317 PR2). Limits an admin can change
-- per workspace, read on every use. A workspace without a policy row gets the
-- same defaults in code. CHECK keeps a value an admin types inside a range
-- where the limit still means something.
ALTER TABLE workspace_artifact_creation_policy
 ADD COLUMN chat_run_create_limit INTEGER NOT NULL DEFAULT 3
 CHECK(chat_run_create_limit BETWEEN 1 AND 20);
ALTER TABLE workspace_artifact_creation_policy
 ADD COLUMN run_read_limit INTEGER NOT NULL DEFAULT 50
 CHECK(run_read_limit BETWEEN 1 AND 500);
ALTER TABLE workspace_artifact_creation_policy
 ADD COLUMN crew_hourly_page_limit INTEGER NOT NULL DEFAULT 20
 CHECK(crew_hourly_page_limit BETWEEN 1 AND 1000);

-- What a run reached beyond its attached references: an artifact it created
-- ('created') or one it read through the agent's own standing access
-- ('standing'). It counts the per-run caps and stays in reach for the run.
-- Written only by the server inside the operation's own transaction, never by
-- an agent or a token. It goes with the run principal.
CREATE TABLE IF NOT EXISTS artifact_run_access (
 run_id TEXT NOT NULL REFERENCES artifact_run_principals(run_id) ON DELETE CASCADE,
 workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 page_id TEXT NOT NULL REFERENCES pages(id) ON DELETE CASCADE,
 source TEXT NOT NULL CHECK(source IN ('created','standing')),
 created_at TEXT NOT NULL,
 PRIMARY KEY(run_id, page_id)
);
CREATE INDEX IF NOT EXISTS artifact_run_access_page ON artifact_run_access(page_id);
CREATE INDEX IF NOT EXISTS artifact_run_access_workspace ON artifact_run_access(workspace_id);
CREATE TRIGGER IF NOT EXISTS artifact_run_access_same_workspace BEFORE INSERT ON artifact_run_access
WHEN NOT EXISTS(
 SELECT 1 FROM artifact_run_principals p JOIN pages pg ON pg.id=NEW.page_id AND pg.workspace_id=p.workspace_id
 WHERE p.run_id=NEW.run_id AND p.workspace_id=NEW.workspace_id)
BEGIN SELECT RAISE(ABORT,'artifact run access must name a page of the run''s workspace'); END;
CREATE TRIGGER IF NOT EXISTS artifact_run_access_immutable BEFORE UPDATE ON artifact_run_access
BEGIN SELECT RAISE(ABORT,'artifact run access is immutable'); END;
