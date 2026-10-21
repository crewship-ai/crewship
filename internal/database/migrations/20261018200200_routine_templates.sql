-- Routine templates (ODCH-RT-22): a named, reusable routine definition a
-- workspace keeps, and starts new routines from. Starting from one saves a
-- DRAFT (never a live routine): it is tested and published like any other.
-- The definition is stored as authored; nothing in it is resolved against a
-- crew until a draft is made from it.
CREATE TABLE routine_templates (
    id              TEXT PRIMARY KEY,
    workspace_id    TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    name            TEXT NOT NULL,
    description     TEXT,
    category        TEXT,
    definition_json TEXT NOT NULL CHECK (json_valid(definition_json)),
    created_by      TEXT,
    created_at      TEXT NOT NULL,
    updated_at      TEXT NOT NULL
);
CREATE UNIQUE INDEX idx_routine_templates_name ON routine_templates(workspace_id, lower(name));
