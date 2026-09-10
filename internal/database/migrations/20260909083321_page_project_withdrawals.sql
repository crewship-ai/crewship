-- Keep the monotonic publication pointer when withdrawing browser execution.
ALTER TABLE page_project_live ADD COLUMN published INTEGER NOT NULL DEFAULT 1 CHECK(published IN (0,1));
CREATE TABLE page_project_withdrawals (
 page_id TEXT NOT NULL,
 version INTEGER NOT NULL,
 actor_user_id TEXT REFERENCES users(id) ON DELETE SET NULL,
 created_at TEXT NOT NULL,
 PRIMARY KEY(page_id,version),
 FOREIGN KEY(page_id,version) REFERENCES page_project_publications(page_id,version) ON DELETE CASCADE
);
CREATE INDEX idx_page_project_withdrawals_actor ON page_project_withdrawals(actor_user_id);
CREATE INDEX idx_page_project_withdrawals_version ON page_project_withdrawals(version,page_id);
