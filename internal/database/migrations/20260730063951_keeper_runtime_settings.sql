-- Make the credential-access judge configurable at runtime instead of only
-- through KEEPER_* env plus a restart.
--
-- The problem: `keeper.enabled`, `keeper.ollama_url` and `keeper.model` were
-- boot-time only (cfg.Keeper, from KEEPER_* env / YAML). The admin console
-- could therefore DIAGNOSE a dead judge — "Not running — disabled by
-- configuration (keeper.enabled = false)" — but not fix it, and an operator
-- who cannot SSH into the box could not turn Keeper on at all. For a
-- self-hosted product whose pitch is "runs fully local", that is the one case
-- that had to work. Worse, Keeper is fail-closed, so a mis-set endpoint
-- surfaces as a DENY on every credential request rather than as a
-- configuration error.
--
-- One singleton row. Every column defaults to empty/NULL meaning "inherit from
-- cfg.Keeper", so an existing deployment behaves exactly as before until an
-- operator changes something — and clearing a field returns it to the env
-- value rather than to a hardcoded guess.
--
-- `enabled` is deliberately nullable and NOT a plain boolean: three states are
-- needed (inherit / explicitly on / explicitly off), and collapsing them would
-- make "the operator has not touched this" indistinguishable from "the
-- operator turned it off" — the difference between honouring KEEPER_ENABLED and
-- silently overriding it.
--
-- `judge_endpoint_url` is judge-scoped on purpose. cfg.Keeper.OllamaURL also
-- builds the episodic embedder and the chat summarizer (internal/server), so
-- overriding *that* value would repoint all three — and moving the embedder
-- silently invalidates every stored vector. This column moves the judge only.
--
-- Instance-global (no workspace_id): it governs the daemon's own gatekeeper,
-- exactly like the env vars it supersedes. Registered in NonBackedUpTables
-- (internal/backup/intent.go) so restoring a workspace bundle cannot carry one
-- instance's judge wiring onto another.
--
-- The per-workspace governance model (keeper_governance_settings, v137) still
-- overrides all of this at request time; this table is the instance default it
-- overrides.

CREATE TABLE IF NOT EXISTS keeper_runtime_settings (
    id                 TEXT PRIMARY KEY CHECK (id = 'singleton'),
    enabled            INTEGER CHECK (enabled IN (0, 1)),
    judge_provider     TEXT NOT NULL DEFAULT '',
    judge_endpoint_url TEXT NOT NULL DEFAULT '',
    judge_wire         TEXT NOT NULL DEFAULT '',
    judge_model        TEXT NOT NULL DEFAULT '',
    updated_by         TEXT REFERENCES users(id) ON DELETE SET NULL,
    created_at         TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    updated_at         TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
