-- #2072 — an MCP server's audience becomes a stored fact.
--
-- "Available to every agent" used to be inferred: resolution counted rows in
-- agent_mcp_bindings and treated zero as "open to all". Nothing stored that
-- state, so it evaporated the moment the count stopped being zero — one
-- POST /api/v1/agents/{id}/integrations anywhere in the workspace flipped the
-- server to opt-in and revoked it from every agent that had been relying on
-- the default, with no warning and nothing on the integration saying so.
--
-- default_access replaces the inference. 'all' means every agent in scope may
-- use the server; 'bound-only' means only agents with an explicit binding.
-- A binding is now purely additive (credential, config override, opt-out) and
-- can never change what any OTHER agent resolves.
--
-- No CHECK constraint on the column, deliberately: SQLite cannot alter one in
-- place, so a two-value vocabulary that later grows a third value (per-crew
-- audiences are the obvious next ask) would need a full table rebuild inside
-- a migration transaction that cannot have foreign_keys=OFF — the dead end
-- v89 and v167 both document. The vocabulary is enforced where it is written
-- (the workspace/crew integration handlers reject anything else) and the
-- resolvers fail CLOSED on an unrecognised value: only the exact string 'all'
-- opens a server to unbound agents.

ALTER TABLE workspace_mcp_servers ADD COLUMN default_access TEXT NOT NULL DEFAULT 'all';
ALTER TABLE crew_mcp_servers ADD COLUMN default_access TEXT NOT NULL DEFAULT 'all';

-- Freeze the audience each existing server ACTUALLY has today.
--
-- The column default is 'all' so that a workspace which never bound anything
-- upgrades to exactly what it had — open. But a server that already carries a
-- binding is, under the old inference, opt-in RIGHT NOW: leaving it 'all'
-- would hand it to every unbound agent in the workspace on the first boot
-- after the upgrade. That is the same silent audience change as the bug, in
-- the widening direction, and it is the worse one. So bound servers are
-- pinned to 'bound-only' and nobody's effective access moves in either
-- direction; changing it afterwards is a deliberate act.
UPDATE workspace_mcp_servers SET default_access = 'bound-only'
WHERE EXISTS (
    SELECT 1 FROM agent_mcp_bindings b
    WHERE b.mcp_server_id = workspace_mcp_servers.id AND b.mcp_server_scope = 'workspace'
);

UPDATE crew_mcp_servers SET default_access = 'bound-only'
WHERE EXISTS (
    SELECT 1 FROM agent_mcp_bindings b
    WHERE b.mcp_server_id = crew_mcp_servers.id AND b.mcp_server_scope = 'crew'
);

-- mcp_tool_bindings had no referential integrity at all (#2072, "Related").
--
-- Its mcp_server_id is polymorphic — discriminated by mcp_server_scope across
-- workspace_mcp_servers and crew_mcp_servers, two separate ID spaces — so a
-- real REFERENCES clause is not expressible: one column cannot point at two
-- tables. v30 hit exactly this on agent_mcp_bindings and settled on a trigger
-- (trg_agent_mcp_binding_fk_check); these mirror it. Triggers also avoid the
-- table rebuild that adding an FK to a populated table would require, which
-- is unsafe inside the migration transaction.
--
-- Deleting an integration is a hard DELETE with a hand-written cascade in Go
-- (DeleteWorkspaceIntegration / DeleteCrewIntegration), and that cascade never
-- mentioned mcp_tool_bindings — so every per-tool toggle the server ever had
-- outlived it, invisible and unreachable, and would be silently re-adopted by
-- any future row that happened to reuse the ID.

-- Sweep the orphans that already exist. These rows reference servers that are
-- gone; nothing can read them and nothing can delete them through the app.
DELETE FROM mcp_tool_bindings
WHERE (mcp_server_scope = 'workspace'
       AND mcp_server_id NOT IN (SELECT id FROM workspace_mcp_servers))
   OR (mcp_server_scope = 'crew'
       AND mcp_server_id NOT IN (SELECT id FROM crew_mcp_servers));

-- ON DELETE CASCADE, emulated.
DROP TRIGGER IF EXISTS trg_mcp_tool_bindings_cascade_on_ws_server_delete;
CREATE TRIGGER trg_mcp_tool_bindings_cascade_on_ws_server_delete
BEFORE DELETE ON workspace_mcp_servers
FOR EACH ROW
BEGIN
    DELETE FROM mcp_tool_bindings
    WHERE mcp_server_id = OLD.id AND mcp_server_scope = 'workspace';
END;

DROP TRIGGER IF EXISTS trg_mcp_tool_bindings_cascade_on_crew_server_delete;
CREATE TRIGGER trg_mcp_tool_bindings_cascade_on_crew_server_delete
BEFORE DELETE ON crew_mcp_servers
FOR EACH ROW
BEGIN
    DELETE FROM mcp_tool_bindings
    WHERE mcp_server_id = OLD.id AND mcp_server_scope = 'crew';
END;

-- The other half of an FK: no new row may name a server that does not exist.
-- Same shape as trg_agent_mcp_binding_fk_check, including the deleted_at
-- filter — a soft-deleted server is not a valid parent either.
DROP TRIGGER IF EXISTS trg_mcp_tool_binding_fk_check;
CREATE TRIGGER trg_mcp_tool_binding_fk_check
BEFORE INSERT ON mcp_tool_bindings
BEGIN
    SELECT RAISE(ABORT, 'mcp_server_id not found in referenced table')
    WHERE (NEW.mcp_server_scope = 'workspace'
           AND NOT EXISTS (SELECT 1 FROM workspace_mcp_servers
                           WHERE id = NEW.mcp_server_id AND deleted_at IS NULL))
       OR (NEW.mcp_server_scope = 'crew'
           AND NOT EXISTS (SELECT 1 FROM crew_mcp_servers
                           WHERE id = NEW.mcp_server_id AND deleted_at IS NULL));
END;
