-- Builds only create preview candidates. They never replace a live Page.
CREATE TABLE page_project_builds (
    id TEXT PRIMARY KEY NOT NULL,
    page_id TEXT NOT NULL REFERENCES pages(id) ON DELETE CASCADE,
    source_revision INTEGER NOT NULL CHECK(source_revision > 0),
    source_digest TEXT NOT NULL CHECK(length(source_digest) = 64),
    state TEXT NOT NULL CHECK(state IN ('running','ready','failed','interrupted')),
    artifact_digest TEXT,
    error TEXT NOT NULL DEFAULT '',
    requested_by TEXT REFERENCES users(id) ON DELETE SET NULL,
    created_at TEXT NOT NULL,
    completed_at TEXT
);
CREATE INDEX idx_page_project_builds_page ON page_project_builds(page_id, created_at DESC);
CREATE INDEX idx_page_project_builds_requester ON page_project_builds(requested_by);
