-- Add crews.kind so the onboarding setup crew can be excluded from the
-- user-facing crew list without a schema rebuild (docs/prd/conversational-onboarding.md §5.3 item 2).
--
-- Today every crew is implicitly "the kind a human made on purpose", and
-- CrewHandler.List (internal/api/crews_query.go) has no way to say otherwise.
-- The conversational-onboarding setup crew — a single-agent, no-write crew
-- created so a new user has something to talk to before any real crew
-- exists — must never show up in that list, in a crew-count badge, or in
-- any other surface that enumerates "your crews". A boolean "hidden" flag
-- would answer only this one case; "kind" is chosen instead because the
-- next thing that needs hiding (a template-preview crew, a health-check
-- crew) is a new value on the same column, not a second flag to remember to
-- check everywhere the first one is checked.
--
-- ADD COLUMN with a column-level CHECK is supported directly — no SQLite
-- rebuild dance needed for a net-new column. Same shape as v101's
-- autonomy_level / behavior_mode (migrate_consts_v101_autonomy.go) and v18's
-- network_mode (migrate_consts_v16_v25.go): NOT NULL + DEFAULT so every
-- existing crew backfills to 'standard' for free, and a bad INSERT fails
-- loudly instead of landing NULL — the exact failure mode §3 of the PRD
-- documents for devcontainer_config.
ALTER TABLE crews ADD COLUMN kind TEXT NOT NULL DEFAULT 'standard'
    CHECK (kind IN ('standard', 'setup'));

-- CrewHandler.List filters WHERE kind != 'setup' on every call; an index
-- keeps that filter from ever needing to be the thing that makes a large
-- workspace's list slow.
CREATE INDEX IF NOT EXISTS idx_crews_kind ON crews (kind);
