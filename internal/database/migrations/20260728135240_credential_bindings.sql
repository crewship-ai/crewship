-- Credential bindings — unfusing the credential's NAME from the agent's ENV VAR
-- (PRD-CREDENTIALS-V2-2026 §2.5b, phase P3).
--
-- The defect. credentials.name was two things at once: the human identity of
-- the credential, and the environment variable the agent reads (recipes.go:
-- "EnvVarName … Doubles as the credential name inside the workspace per
-- existing convention"). It also carries UNIQUE(workspace_id, name). So a
-- workspace could hold exactly one GitHub account, because a second one would
-- also have to be called GH_TOKEN and the INSERT is rejected by the schema.
-- Ten crews with ten GitHub bots was not awkward, it was unrepresentable.
--
-- The split:
--
--   credentials.name  → the human name of ONE ACCOUNT (github-acme). Keeps its
--                       UNIQUE, because that is the correct constraint for a
--                       human name and removing it would only trade one bug for
--                       an ambiguous vault listing.
--   slot              → the env var, and it now lives on a BINDING:
--                       (scope, slot) → credential, scope ∈ workspace|crew|agent.
--
-- Two crews can therefore both bind GH_TOKEN, to two different accounts.
--
-- WHY THE SCOPE OWNER IS TWO TYPED COLUMNS, NOT ONE POLYMORPHIC scope_id.
-- crew_id/agent_id carry real foreign keys with ON DELETE CASCADE. A deleted
-- crew takes its bindings with it, so a (scope, slot) claim cannot outlive the
-- thing it was scoped to and keep delivering a credential to whoever is
-- reparented into that id's place. A single scope_id TEXT column could not have
-- an FK at all — SQLite has no polymorphic references — and the orphan rows
-- would have to be swept by code nobody would remember to write.
--
-- WHY THE UNIQUE INDEX USES COALESCE. The invariant is: within one scope, one
-- slot resolves to exactly one credential. For WORKSPACE scope BOTH owner
-- columns are NULL, and SQLite treats NULLs as distinct in a UNIQUE index — so
-- a plain UNIQUE(workspace_id, scope, crew_id, agent_id, slot) would enforce
-- the invariant for crews and agents and silently permit unlimited duplicates
-- at exactly the scope that applies to every agent in the tenant. COALESCE
-- folds the owner to a single non-NULL key. It is deterministic, which is the
-- only thing SQLite requires of an index expression.
--
-- The API returns 409 on a conflicting write rather than replacing the row: a
-- silent last-write-wins would repoint every agent in a crew at a different
-- GitHub account with no request having said so.
--
-- NO DATA BACKFILL, AND THAT IS THE COMPATIBILITY GUARANTEE.
-- Nothing here renames a credential and nothing materialises a binding out of
-- existing credential_crews / agent_credentials rows. Backward compatibility is
-- delivered by the QUERY instead: a credential with no binding keeps being
-- delivered under credentials.name, exactly as before, so an existing workspace
-- upgrades to zero binding rows and byte-identical delivery.
--
-- A backfill would also be actively wrong. credential_delivery.go derives the
-- crew fanout at READ time precisely so that unlinking a credential stops
-- delivery on the next boot; copying those links into binding rows would
-- reintroduce the materialised state that outlives the unlink and keeps
-- handing out a revoked credential forever. A one-time backfill would not even
-- cover future credential_crews writes, so the read path has to keep the legacy
-- arm regardless — which makes the copy pure downside.

CREATE TABLE IF NOT EXISTS credential_bindings (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    credential_id TEXT NOT NULL REFERENCES credentials(id) ON DELETE CASCADE,
    scope TEXT NOT NULL CHECK (scope IN ('WORKSPACE', 'CREW', 'AGENT')),
    crew_id TEXT REFERENCES crews(id) ON DELETE CASCADE,
    agent_id TEXT REFERENCES agents(id) ON DELETE CASCADE,
    -- slot is the environment variable name the agent reads (GH_TOKEN).
    slot TEXT NOT NULL,
    created_by TEXT REFERENCES users(id),
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at TEXT NOT NULL DEFAULT (datetime('now')),
    -- scope and owner must agree. Without this a row could claim scope='CREW'
    -- with a NULL crew_id, which the delivery join reads as "matches no crew"
    -- while every listing renders it as a live crew binding — a credential the
    -- UI says is assigned and the container never receives.
    CHECK (
        (scope = 'WORKSPACE' AND crew_id IS NULL     AND agent_id IS NULL)
     OR (scope = 'CREW'      AND crew_id IS NOT NULL AND agent_id IS NULL)
     OR (scope = 'AGENT'     AND crew_id IS NULL     AND agent_id IS NOT NULL)
    ),
    -- An empty or blank slot would export an unusable env var and, worse,
    -- would collide with every other blank slot in the same scope.
    CHECK (length(trim(slot)) > 0)
);

-- The invariant, enforced on the binding rather than on the credential name.
CREATE UNIQUE INDEX IF NOT EXISTS idx_credential_bindings_slot
    ON credential_bindings(workspace_id, scope, COALESCE(crew_id, agent_id, ''), slot);

-- Delivery looks bindings up by owner; the listing and the cascade-on-revoke
-- path look them up by credential.
CREATE INDEX IF NOT EXISTS idx_credential_bindings_cred ON credential_bindings(credential_id);
CREATE INDEX IF NOT EXISTS idx_credential_bindings_crew ON credential_bindings(crew_id);
CREATE INDEX IF NOT EXISTS idx_credential_bindings_agent ON credential_bindings(agent_id);
CREATE INDEX IF NOT EXISTS idx_credential_bindings_ws ON credential_bindings(workspace_id, scope);

-- Cross-tenant guard, mirroring trg_credential_crews_workspace_check (v17).
-- A WORKSPACE-scope binding has no crew or agent to narrow it, so
-- credential_bindings.workspace_id is the only thing keeping one tenant's
-- credential out of another tenant's containers. The handler checks the same
-- three facts; this is the copy that survives a new write path being added
-- somewhere nobody thought to look.
CREATE TRIGGER IF NOT EXISTS trg_credential_bindings_workspace_check
BEFORE INSERT ON credential_bindings
BEGIN
    SELECT RAISE(ABORT, 'credential must belong to the binding workspace')
    WHERE (SELECT workspace_id FROM credentials WHERE id = NEW.credential_id) IS NOT NEW.workspace_id;

    SELECT RAISE(ABORT, 'crew must belong to the binding workspace')
    WHERE NEW.crew_id IS NOT NULL
      AND (SELECT workspace_id FROM crews WHERE id = NEW.crew_id) IS NOT NEW.workspace_id;

    SELECT RAISE(ABORT, 'agent must belong to the binding workspace')
    WHERE NEW.agent_id IS NOT NULL
      AND (SELECT workspace_id FROM agents WHERE id = NEW.agent_id) IS NOT NEW.workspace_id;
END;

CREATE TRIGGER IF NOT EXISTS trg_credential_bindings_workspace_check_upd
BEFORE UPDATE ON credential_bindings
BEGIN
    SELECT RAISE(ABORT, 'credential must belong to the binding workspace')
    WHERE (SELECT workspace_id FROM credentials WHERE id = NEW.credential_id) IS NOT NEW.workspace_id;

    SELECT RAISE(ABORT, 'crew must belong to the binding workspace')
    WHERE NEW.crew_id IS NOT NULL
      AND (SELECT workspace_id FROM crews WHERE id = NEW.crew_id) IS NOT NEW.workspace_id;

    SELECT RAISE(ABORT, 'agent must belong to the binding workspace')
    WHERE NEW.agent_id IS NOT NULL
      AND (SELECT workspace_id FROM agents WHERE id = NEW.agent_id) IS NOT NEW.workspace_id;
END;
