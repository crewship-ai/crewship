-- Source bytes live in the protected project store. A draft never publishes UI
-- or activates imported actions, schedules or grants.
CREATE TABLE page_project_drafts (
    page_id TEXT PRIMARY KEY NOT NULL REFERENCES pages(id) ON DELETE CASCADE,
    source_digest TEXT NOT NULL CHECK(length(source_digest) = 64),
    revision INTEGER NOT NULL CHECK(revision > 0),
    spec_json TEXT NOT NULL,
    updated_by TEXT REFERENCES users(id) ON DELETE SET NULL,
    updated_at TEXT NOT NULL
);
CREATE INDEX idx_page_project_drafts_updated_by ON page_project_drafts(updated_by);

-- Small durable audit records; source bytes are not duplicated in SQLite.
CREATE TABLE page_project_revisions (
    page_id TEXT NOT NULL REFERENCES pages(id) ON DELETE CASCADE,
    revision INTEGER NOT NULL CHECK(revision > 0),
    source_digest TEXT NOT NULL CHECK(length(source_digest) = 64),
    actor_user_id TEXT REFERENCES users(id) ON DELETE SET NULL,
    created_at TEXT NOT NULL,
    PRIMARY KEY (page_id, revision)
);
CREATE INDEX idx_page_project_revisions_actor ON page_project_revisions(actor_user_id);
