CREATE TABLE page_project_publications (
 page_id TEXT NOT NULL REFERENCES pages(id) ON DELETE CASCADE,
 version INTEGER NOT NULL CHECK(version>0),
 build_id TEXT NOT NULL REFERENCES page_project_builds(id),
 source_revision INTEGER NOT NULL,
 source_digest TEXT NOT NULL,
 git_commit TEXT NOT NULL,
 artifact_digest TEXT NOT NULL,
 spec_json TEXT NOT NULL,
 checks_json TEXT NOT NULL,
 actor_user_id TEXT REFERENCES users(id) ON DELETE SET NULL,
 created_at TEXT NOT NULL,
 rollback_of INTEGER,
 PRIMARY KEY(page_id,version)
);
CREATE INDEX idx_page_project_publications_actor ON page_project_publications(actor_user_id);
CREATE TABLE page_project_live (
 page_id TEXT PRIMARY KEY NOT NULL REFERENCES pages(id) ON DELETE CASCADE,
 version INTEGER NOT NULL,
 FOREIGN KEY(page_id,version) REFERENCES page_project_publications(page_id,version)
);
CREATE INDEX idx_page_project_publications_build ON page_project_publications(build_id);
CREATE INDEX idx_page_project_live_version ON page_project_live(version,page_id);
