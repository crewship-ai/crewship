-- Per-category wording for the notifications the product generates itself.
--
-- A routine's notify step has always written its own message. Everything
-- Crewship generates — "Pipeline x completed", "Scheduled routine failed: y" —
-- had its wording computed at the producer, in Go, one string per site, with
-- no way for an operator to change it.
--
-- Scope is (workspace, category, channel). Category is the primary axis
-- because wording is a property of the EVENT: "a routine failed" reads the
-- same wherever it goes. channel_id narrows it for the case where one
-- destination genuinely wants something different — a terse line for a pager,
-- a fuller one for e-mail — and is nullable so the common case is one row per
-- category rather than one per category per channel.
--
-- The unique index treats NULL channel_id as its own slot via COALESCE:
-- SQLite considers two NULLs distinct in a UNIQUE constraint, so without it a
-- workspace could accumulate any number of all-channel templates for the same
-- category and the last write would win at random.
--
-- No CHECK on category. The vocabulary lives in internal/notify.AllCategories
-- and has already been rewritten once (taxonomy v2); pinning it here would
-- mean a table rebuild every time it moves, and the write path validates
-- against the Go list, which is the one that decides routing anyway.

CREATE TABLE IF NOT EXISTS notification_templates (
    id             TEXT PRIMARY KEY,
    workspace_id   TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    category       TEXT NOT NULL,
    channel_id     TEXT REFERENCES notification_channels(id) ON DELETE CASCADE,
    title_template TEXT NOT NULL DEFAULT '',
    body_template  TEXT NOT NULL DEFAULT '',
    created_at     TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    updated_at     TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_notification_templates_scope
    ON notification_templates(workspace_id, category, COALESCE(channel_id, ''));

CREATE INDEX IF NOT EXISTS idx_notification_templates_ws
    ON notification_templates(workspace_id);
