-- Every workbook revision, not only the first, stays on a personal human Page.
-- Audience is deliberately not checked: an owner may explicitly publish it.
CREATE TRIGGER gridcraft_require_personal_page_revision BEFORE UPDATE OF head_rev,generation,snapshot ON gridcraft_heads
WHEN NOT EXISTS(SELECT 1 FROM pages WHERE id=NEW.page_id AND workspace_id=NEW.workspace_id AND owner_user_id IS NOT NULL AND owner_crew_id IS NULL AND folder_id IS NULL)
BEGIN SELECT RAISE(ABORT,'Workbook requires a personal Page'); END;
