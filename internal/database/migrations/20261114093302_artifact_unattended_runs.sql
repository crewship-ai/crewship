-- Runs without a human (#5317 step 5, #5339). A routine, webhook or scheduled
-- run reaches artifacts only through a run the HOST recorded before the agent
-- started. The row names the run's agent, crew, trigger and source, and the
-- SHA-256 of a random capability the server minted for it. The capability is
-- never derived from a key a crew container holds, so a container can neither
-- record a run nor present one for another agent or a fresh run id.
-- Written only by host code (internal/artifactrun); no HTTP endpoint and no
-- internal token writes it.
CREATE TABLE IF NOT EXISTS artifact_unattended_runs (
 run_id TEXT PRIMARY KEY CHECK(length(run_id) BETWEEN 1 AND 200),
 workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 crew_id TEXT NOT NULL REFERENCES crews(id) ON DELETE CASCADE,
 agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
 trigger_type TEXT NOT NULL CHECK(trigger_type IN ('ROUTINE','WEBHOOK','SCHEDULED')),
 source_ref TEXT NOT NULL DEFAULT '' CHECK(length(source_ref) <= 500),
 capability_sha256 TEXT NOT NULL CHECK(length(capability_sha256) = 64),
 created_at TEXT NOT NULL,
 expires_at TEXT NOT NULL,
 ended_at TEXT
);
CREATE INDEX IF NOT EXISTS artifact_unattended_runs_workspace ON artifact_unattended_runs(workspace_id);
CREATE INDEX IF NOT EXISTS artifact_unattended_runs_agent ON artifact_unattended_runs(agent_id);
-- The agent must belong to the crew in the same workspace when recorded.
CREATE TRIGGER IF NOT EXISTS artifact_unattended_runs_shape BEFORE INSERT ON artifact_unattended_runs
WHEN NOT EXISTS(
 SELECT 1 FROM agents a JOIN crews c ON c.id=a.crew_id AND c.workspace_id=a.workspace_id
 WHERE a.id=NEW.agent_id AND a.workspace_id=NEW.workspace_id AND a.crew_id=NEW.crew_id)
BEGIN SELECT RAISE(ABORT,'unattended run must name an agent of its crew and workspace'); END;
-- Only the end of the run may change the row, once.
CREATE TRIGGER IF NOT EXISTS artifact_unattended_runs_immutable BEFORE UPDATE ON artifact_unattended_runs
WHEN OLD.ended_at IS NOT NULL OR NEW.ended_at IS NULL
 OR NEW.run_id IS NOT OLD.run_id OR NEW.workspace_id IS NOT OLD.workspace_id
 OR NEW.crew_id IS NOT OLD.crew_id OR NEW.agent_id IS NOT OLD.agent_id
 OR NEW.trigger_type IS NOT OLD.trigger_type OR NEW.source_ref IS NOT OLD.source_ref
 OR NEW.capability_sha256 IS NOT OLD.capability_sha256 OR NEW.created_at IS NOT OLD.created_at
 OR NEW.expires_at IS NOT OLD.expires_at
BEGIN SELECT RAISE(ABORT,'unattended run is immutable except its end'); END;

-- The distinct artifacts such a run reached, for its per-run read cap
-- (run_read_limit). Written only by the server inside the access's own
-- transaction.
CREATE TABLE IF NOT EXISTS artifact_unattended_run_access (
 run_id TEXT NOT NULL REFERENCES artifact_unattended_runs(run_id) ON DELETE CASCADE,
 workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 page_id TEXT NOT NULL REFERENCES pages(id) ON DELETE CASCADE,
 created_at TEXT NOT NULL,
 PRIMARY KEY(run_id, page_id)
);
CREATE INDEX IF NOT EXISTS artifact_unattended_run_access_page ON artifact_unattended_run_access(page_id);
CREATE INDEX IF NOT EXISTS artifact_unattended_run_access_workspace ON artifact_unattended_run_access(workspace_id);
CREATE TRIGGER IF NOT EXISTS artifact_unattended_run_access_same_workspace BEFORE INSERT ON artifact_unattended_run_access
WHEN NOT EXISTS(
 SELECT 1 FROM artifact_unattended_runs u JOIN pages p ON p.id=NEW.page_id AND p.workspace_id=u.workspace_id
 WHERE u.run_id=NEW.run_id AND u.workspace_id=NEW.workspace_id)
BEGIN SELECT RAISE(ABORT,'unattended run access must name a page of the run''s workspace'); END;
CREATE TRIGGER IF NOT EXISTS artifact_unattended_run_access_immutable BEFORE UPDATE ON artifact_unattended_run_access
BEGIN SELECT RAISE(ABORT,'unattended run access is immutable'); END;
