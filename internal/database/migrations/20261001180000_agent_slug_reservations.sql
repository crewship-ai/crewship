-- Agent durable data lives under slug-keyed paths of its crew
-- (/crew/agents/<slug>, /output/<slug>). A slug left behind by a deleted,
-- renamed or moved agent still names that agent's memory and output, so a
-- new agent taking it in the same crew would inherit them. Interim until agent
-- data is keyed by agent_id: the old (crew, slug) is reserved for the agent
-- that used it, and nothing else may take it in that crew.
-- FKs cascade on hard deletes only: a hard-deleted crew's data root is gone
-- with it, and a hard-deleted agent (create compensation) never ran. They also
-- let a forked restore remap the row to the new crew and agent IDs.
CREATE TABLE agent_slug_reservations (
 crew_id TEXT NOT NULL REFERENCES crews(id) ON DELETE CASCADE,
 slug TEXT NOT NULL,
 workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
 reason TEXT NOT NULL,
 created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
 PRIMARY KEY (crew_id, slug)
);

CREATE INDEX idx_agent_slug_reservations_agent ON agent_slug_reservations(agent_id);
CREATE INDEX idx_agent_slug_reservations_workspace ON agent_slug_reservations(workspace_id);

-- Existing tombstones keep their slug reserved. Tombstones whose slug was
-- already released (renamed to <slug>_deleted_<id>) are reserved under the
-- original slug unless a live agent of the same crew already holds it: that
-- data is ambiguous legacy and is reported, not assigned, by the migration
-- dry-run.
INSERT OR IGNORE INTO agent_slug_reservations (crew_id, slug, workspace_id, agent_id, reason, created_at)
SELECT t.crew_id, t.original_slug, t.workspace_id, t.id, 'deleted', COALESCE(t.deleted_at, strftime('%Y-%m-%dT%H:%M:%fZ','now'))
FROM (
 SELECT a.id, a.crew_id, a.workspace_id, a.deleted_at,
  CASE WHEN instr(a.slug, '_deleted_') > 0 THEN substr(a.slug, 1, instr(a.slug, '_deleted_') - 1) ELSE a.slug END AS original_slug
 FROM agents a JOIN crews c ON c.id = a.crew_id
 WHERE a.deleted_at IS NOT NULL
) t
WHERE NOT EXISTS (
 SELECT 1 FROM agents live
 WHERE live.crew_id = t.crew_id AND live.slug = t.original_slug AND live.deleted_at IS NULL
);

CREATE TRIGGER agent_slug_reserve_on_change AFTER UPDATE OF deleted_at, slug, crew_id ON agents
WHEN OLD.deleted_at IS NULL AND OLD.crew_id IS NOT NULL AND OLD.crew_id != ''
 AND (NEW.deleted_at IS NOT NULL OR NEW.slug != OLD.slug OR NEW.crew_id IS NOT OLD.crew_id)
BEGIN
 INSERT OR IGNORE INTO agent_slug_reservations (crew_id, slug, workspace_id, agent_id, reason)
 VALUES (OLD.crew_id, OLD.slug, OLD.workspace_id, OLD.id,
  CASE WHEN NEW.deleted_at IS NOT NULL THEN 'deleted' WHEN NEW.crew_id IS NOT OLD.crew_id THEN 'moved' ELSE 'renamed' END);
END;

-- Only a slug that is free in the agents table can be inherited: while a row
-- still holds it, the UNIQUE (workspace_id, slug) constraint decides (an
-- INSERT OR IGNORE then skips, as the onboarding setup crew relies on).
CREATE TRIGGER agent_slug_reserved_insert BEFORE INSERT ON agents
WHEN NEW.crew_id IS NOT NULL
 AND NOT EXISTS (SELECT 1 FROM agents a WHERE a.workspace_id = NEW.workspace_id AND a.slug = NEW.slug)
 AND EXISTS (
 SELECT 1 FROM agent_slug_reservations r WHERE r.crew_id = NEW.crew_id AND r.slug = NEW.slug AND r.agent_id != NEW.id)
BEGIN
 SELECT RAISE(ABORT, 'agent slug is reserved in this crew: another agent''s memory and output are stored under it');
END;

CREATE TRIGGER agent_slug_reserved_update BEFORE UPDATE OF slug, crew_id ON agents
WHEN NEW.deleted_at IS NULL AND NEW.crew_id IS NOT NULL AND EXISTS (
 SELECT 1 FROM agent_slug_reservations r WHERE r.crew_id = NEW.crew_id AND r.slug = NEW.slug AND r.agent_id != NEW.id)
BEGIN
 SELECT RAISE(ABORT, 'agent slug is reserved in this crew: another agent''s memory and output are stored under it');
END;
