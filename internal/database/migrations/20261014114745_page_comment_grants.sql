-- Add human and named-agent comment grants while preserving existing main rows.
-- Only this pre-existing table needs a rebuild; feature tables start at final schema.
CREATE TABLE page_grants_with_comments (
  page_id TEXT NOT NULL REFERENCES pages(id) ON DELETE CASCADE,
  subject_type TEXT NOT NULL CHECK(subject_type IN ('user','crew','agent','workspace')),
  subject_id TEXT NOT NULL DEFAULT '',
  level TEXT NOT NULL CHECK(level IN ('read','comment','produce','write')),
  panel_ids TEXT,
  granted_by_user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  granted_at TEXT NOT NULL DEFAULT(datetime('now')),
  PRIMARY KEY(page_id,subject_type,subject_id,level),
  CHECK(panel_ids IS NULL OR level='produce'),
  CHECK(subject_type<>'workspace' OR(subject_id='' AND level IN ('read','comment','write')))
);

INSERT INTO page_grants_with_comments(page_id,subject_type,subject_id,level,panel_ids,granted_by_user_id,granted_at)
SELECT page_id,subject_type,subject_id,level,panel_ids,granted_by_user_id,granted_at FROM page_grants;

DROP TABLE page_grants;
ALTER TABLE page_grants_with_comments RENAME TO page_grants;

CREATE INDEX idx_page_grants_subject ON page_grants(subject_type,subject_id);

CREATE INDEX idx_page_grants_granted_by ON page_grants(granted_by_user_id);
