-- Default-off collaboration is a distinct content type, not an alternate
-- transport into another engine. These writer-time checks cover both orders
-- of attachment and identity movement; route preflights alone are insufficient.
-- Optional output schemas installed later must add their symmetric fence before
-- being qualified with collaboration. They are not enabled by this migration.
CREATE TRIGGER mooring_collab_exclusive_type_insert BEFORE INSERT ON mooring_collab_documents
WHEN NOT EXISTS(SELECT 1 FROM pages WHERE id=NEW.page_id AND workspace_id=NEW.workspace_id AND owner_user_id IS NOT NULL AND owner_crew_id IS NULL)
 OR EXISTS(SELECT 1 FROM documents WHERE page_id=NEW.page_id)
 OR EXISTS(SELECT 1 FROM slides_heads WHERE page_id=NEW.page_id)
 OR EXISTS(SELECT 1 FROM gridcraft_heads WHERE page_id=NEW.page_id)
 OR EXISTS(SELECT 1 FROM mooring_documents WHERE page_id=NEW.page_id)
 OR EXISTS(SELECT 1 FROM page_panels WHERE page_id=NEW.page_id)
 OR EXISTS(SELECT 1 FROM page_project_drafts WHERE page_id=NEW.page_id)
 OR EXISTS(SELECT 1 FROM page_project_live WHERE page_id=NEW.page_id)
BEGIN SELECT RAISE(ABORT,'Collaboration requires an empty human-owned Page in its workspace'); END;
CREATE TRIGGER mooring_collab_exclusive_type_update BEFORE UPDATE OF page_id,workspace_id ON mooring_collab_documents
WHEN NOT EXISTS(SELECT 1 FROM pages WHERE id=NEW.page_id AND workspace_id=NEW.workspace_id AND owner_user_id IS NOT NULL AND owner_crew_id IS NULL)
 OR EXISTS(SELECT 1 FROM documents WHERE page_id=NEW.page_id)
 OR EXISTS(SELECT 1 FROM slides_heads WHERE page_id=NEW.page_id)
 OR EXISTS(SELECT 1 FROM gridcraft_heads WHERE page_id=NEW.page_id)
 OR EXISTS(SELECT 1 FROM mooring_documents WHERE page_id=NEW.page_id)
 OR EXISTS(SELECT 1 FROM page_panels WHERE page_id=NEW.page_id)
 OR EXISTS(SELECT 1 FROM page_project_drafts WHERE page_id=NEW.page_id)
 OR EXISTS(SELECT 1 FROM page_project_live WHERE page_id=NEW.page_id)
BEGIN SELECT RAISE(ABORT,'Collaboration requires an empty human-owned Page in its workspace'); END;
CREATE TRIGGER documents_exclude_mooring_collab_insert BEFORE INSERT ON documents
WHEN EXISTS(SELECT 1 FROM mooring_collab_documents WHERE page_id=NEW.page_id)
BEGIN SELECT RAISE(ABORT,'Page already contains collaboration content'); END;
CREATE TRIGGER documents_exclude_mooring_collab_update BEFORE UPDATE OF page_id ON documents
WHEN EXISTS(SELECT 1 FROM mooring_collab_documents WHERE page_id=NEW.page_id)
BEGIN SELECT RAISE(ABORT,'Page already contains collaboration content'); END;
CREATE TRIGGER slides_heads_exclude_mooring_collab_insert BEFORE INSERT ON slides_heads
WHEN EXISTS(SELECT 1 FROM mooring_collab_documents WHERE page_id=NEW.page_id)
BEGIN SELECT RAISE(ABORT,'Page already contains collaboration content'); END;
CREATE TRIGGER slides_heads_exclude_mooring_collab_update BEFORE UPDATE OF page_id ON slides_heads
WHEN EXISTS(SELECT 1 FROM mooring_collab_documents WHERE page_id=NEW.page_id)
BEGIN SELECT RAISE(ABORT,'Page already contains collaboration content'); END;
CREATE TRIGGER gridcraft_heads_exclude_mooring_collab_insert BEFORE INSERT ON gridcraft_heads
WHEN EXISTS(SELECT 1 FROM mooring_collab_documents WHERE page_id=NEW.page_id)
BEGIN SELECT RAISE(ABORT,'Page already contains collaboration content'); END;
CREATE TRIGGER gridcraft_heads_exclude_mooring_collab_update BEFORE UPDATE OF page_id ON gridcraft_heads
WHEN EXISTS(SELECT 1 FROM mooring_collab_documents WHERE page_id=NEW.page_id)
BEGIN SELECT RAISE(ABORT,'Page already contains collaboration content'); END;
CREATE TRIGGER mooring_documents_exclude_mooring_collab_insert BEFORE INSERT ON mooring_documents
WHEN EXISTS(SELECT 1 FROM mooring_collab_documents WHERE page_id=NEW.page_id)
BEGIN SELECT RAISE(ABORT,'Page already contains collaboration content'); END;
CREATE TRIGGER mooring_documents_exclude_mooring_collab_update BEFORE UPDATE OF page_id ON mooring_documents
WHEN EXISTS(SELECT 1 FROM mooring_collab_documents WHERE page_id=NEW.page_id)
BEGIN SELECT RAISE(ABORT,'Page already contains collaboration content'); END;
CREATE TRIGGER page_panels_exclude_mooring_collab_insert BEFORE INSERT ON page_panels
WHEN EXISTS(SELECT 1 FROM mooring_collab_documents WHERE page_id=NEW.page_id)
BEGIN SELECT RAISE(ABORT,'Page already contains collaboration content'); END;
CREATE TRIGGER page_panels_exclude_mooring_collab_update BEFORE UPDATE OF page_id ON page_panels
WHEN EXISTS(SELECT 1 FROM mooring_collab_documents WHERE page_id=NEW.page_id)
BEGIN SELECT RAISE(ABORT,'Page already contains collaboration content'); END;
CREATE TRIGGER page_project_drafts_exclude_mooring_collab_insert BEFORE INSERT ON page_project_drafts
WHEN EXISTS(SELECT 1 FROM mooring_collab_documents WHERE page_id=NEW.page_id)
BEGIN SELECT RAISE(ABORT,'Page already contains collaboration content'); END;
CREATE TRIGGER page_project_drafts_exclude_mooring_collab_update BEFORE UPDATE OF page_id ON page_project_drafts
WHEN EXISTS(SELECT 1 FROM mooring_collab_documents WHERE page_id=NEW.page_id)
BEGIN SELECT RAISE(ABORT,'Page already contains collaboration content'); END;
CREATE TRIGGER page_project_live_exclude_mooring_collab_insert BEFORE INSERT ON page_project_live
WHEN EXISTS(SELECT 1 FROM mooring_collab_documents WHERE page_id=NEW.page_id)
BEGIN SELECT RAISE(ABORT,'Page already contains collaboration content'); END;
CREATE TRIGGER page_project_live_exclude_mooring_collab_update BEFORE UPDATE OF page_id ON page_project_live
WHEN EXISTS(SELECT 1 FROM mooring_collab_documents WHERE page_id=NEW.page_id)
BEGIN SELECT RAISE(ABORT,'Page already contains collaboration content'); END;
CREATE TRIGGER pages_preserve_collaboration_scope BEFORE UPDATE OF id,workspace_id,owner_user_id,owner_crew_id ON pages
WHEN EXISTS(SELECT 1 FROM mooring_collab_documents WHERE page_id=OLD.id)
 AND (NEW.id IS NOT OLD.id OR NEW.workspace_id IS NOT OLD.workspace_id OR NEW.owner_user_id IS NULL OR NEW.owner_crew_id IS NOT NULL)
BEGIN SELECT RAISE(ABORT,'Collaboration requires fixed scope and explicit human ownership succession'); END;

-- Saved history/checkpoints cannot be rebound to a different application or
-- Page. A future qualified migration needs an explicit migration contract.
CREATE TRIGGER mooring_collab_identity_immutable BEFORE UPDATE OF page_id,workspace_id,application_id ON mooring_collab_documents
WHEN OLD.page_id IS NOT NEW.page_id OR OLD.workspace_id IS NOT NEW.workspace_id OR OLD.application_id IS NOT NEW.application_id
BEGIN SELECT RAISE(ABORT,'Collaboration content identity is immutable'); END;
