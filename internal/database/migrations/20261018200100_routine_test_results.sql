-- The result of a routine Test run, kept against the definition it tested
-- (ODCH-RT-13). Keyed by (workspace, definition_hash): a draft and a version
-- with the same definition share one result, and an edited definition has
-- none until it is tested again, so "Test run passed" is never shown for
-- content that was not tested. The latest test of a definition wins.
CREATE TABLE routine_test_results (
    id              TEXT PRIMARY KEY,
    workspace_id    TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    definition_hash TEXT NOT NULL,
    passed          INTEGER NOT NULL,
    status          TEXT NOT NULL,
    tested_by       TEXT,
    tested_at       TEXT NOT NULL,
    UNIQUE (workspace_id, definition_hash)
);
