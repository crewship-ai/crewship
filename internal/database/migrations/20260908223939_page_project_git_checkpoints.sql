-- Append only: old source-only drafts retain an empty commit until next save.
ALTER TABLE page_project_drafts ADD COLUMN git_commit TEXT NOT NULL DEFAULT '';
ALTER TABLE page_project_revisions ADD COLUMN git_commit TEXT NOT NULL DEFAULT '';
ALTER TABLE page_project_revisions ADD COLUMN spec_json TEXT NOT NULL DEFAULT '';
