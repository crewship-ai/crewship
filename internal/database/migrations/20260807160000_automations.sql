-- automations: workspace-scoped rules that turn a journal event into a
-- deferred routine run. The trigger path is
--
--     journal commit → AddCommitObserver → match (in memory) → pending_runs
--
-- and this table is the only durable part of it.
--
-- Why a NEW table rather than a mode on hooks_config. hooks_config is an
-- INTERCEPT layer: crew-scoped, blocking, and it holds a veto ("stop this
-- tool call"). An automation is the opposite animal — workspace-scoped,
-- non-blocking, reacting after the fact, with no ability to refuse anything.
-- The two share a row shape and nothing else, and one table serving both
-- would have to lie about what it guarantees to at least one of them.
--
-- The predicate column deliberately mirrors hooks.Matcher's SHAPE
-- ({crew_ids, agent_ids, severities, mission_ids, payload_equals}) so the
-- two can converge later if we ever decide they should, without a rewrite
-- of every stored rule.

CREATE TABLE automations (
    id             TEXT PRIMARY KEY,
    workspace_id   TEXT NOT NULL,
    name           TEXT NOT NULL,
    enabled        INTEGER NOT NULL DEFAULT 1,
    -- A journal EntryType. Exactly one per row: an automation that fires on
    -- "anything" is a support ticket waiting to happen.
    event_type     TEXT NOT NULL,
    -- Predicate, same SHAPE as hooks.Matcher so the two can converge later
    -- if we ever decide they should: {crew_ids, agent_ids, severities,
    -- mission_ids, payload_equals}. Empty object = match all of this type.
    matcher_json   TEXT NOT NULL DEFAULT '{}',
    -- 'routine' only in v1. The column exists so 'issue'/'notify' can land
    -- without a migration; reject anything else at write time.
    action_kind    TEXT NOT NULL CHECK (action_kind IN ('routine')),
    action_config_json TEXT NOT NULL DEFAULT '{}',
    -- Burst control. A status-change storm must produce one run, not 200.
    debounce_seconds INTEGER NOT NULL DEFAULT 10,
    max_per_hour   INTEGER NOT NULL DEFAULT 60,
    created_by     TEXT,
    created_at     TEXT NOT NULL,
    updated_at     TEXT NOT NULL,
    deleted_at     TEXT
);

-- The registry refresh reads exactly this shape: every live, enabled rule of
-- one event type in one workspace. Partial on deleted_at so a soft-deleted
-- rule costs the hot refresh nothing.
CREATE INDEX idx_automations_event ON automations (workspace_id, event_type, enabled)
    WHERE deleted_at IS NULL;
