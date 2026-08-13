-- Widen automations.action_kind to admit 'issue'.
--
-- The original migration's own comment says "The column exists so
-- 'issue'/'notify' can land without a migration" — and then constrains the
-- column to CHECK (action_kind IN ('routine')). The intent was right and the
-- constraint contradicts it: an issue rule was unstorable, and the first
-- attempt to write one would have failed the CHECK inside a page save.
--
-- 'issue' is the action Pages' wake gates compile to (docs/prd/pages.md §5):
-- a threshold on a pushed payload opens an issue on the crew the page author
-- named, through the matcher, debounce and burst brake internal/automation
-- already provides. See internal/automation/issue.go for what that action may
-- and may not do.
--
-- 'notify' is deliberately NOT admitted here. It has no implementation, and a
-- constraint that accepts a value nothing can execute is how a rule gets saved
-- and silently never fires — the exact failure this package apologises for in
-- three other places.
--
-- SQLite has no ALTER CHECK, so the table is recreated per the documented
-- pattern (v90, v162, inbox_kinds): create _new with the widened constraint,
-- copy positionally, drop, rename, recreate the one index. Nothing references
-- automations and it carries no triggers, so this runs INSIDE the wrapper
-- transaction and needs no PRAGMA foreign_keys=OFF.

CREATE TABLE automations_new (
    id             TEXT PRIMARY KEY,
    workspace_id   TEXT NOT NULL,
    name           TEXT NOT NULL,
    enabled        INTEGER NOT NULL DEFAULT 1,
    event_type     TEXT NOT NULL,
    matcher_json   TEXT NOT NULL DEFAULT '{}',
    -- 'routine' parks a deferred run; 'issue' opens an issue on a crew.
    action_kind    TEXT NOT NULL CHECK (action_kind IN ('routine', 'issue')),
    action_config_json TEXT NOT NULL DEFAULT '{}',
    debounce_seconds INTEGER NOT NULL DEFAULT 10,
    max_per_hour   INTEGER NOT NULL DEFAULT 60,
    created_by     TEXT,
    created_at     TEXT NOT NULL,
    updated_at     TEXT NOT NULL,
    deleted_at     TEXT
);

INSERT INTO automations_new SELECT * FROM automations;

DROP TABLE automations;
ALTER TABLE automations_new RENAME TO automations;

CREATE INDEX IF NOT EXISTS idx_automations_event ON automations (workspace_id, event_type, enabled)
    WHERE deleted_at IS NULL;
