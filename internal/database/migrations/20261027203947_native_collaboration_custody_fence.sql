-- Native whole-document custody and collaboration custody are mutually
-- exclusive. Both insert orders are fenced in the writer transaction, including
-- direct NativeStore.Initialize and a late competing collaboration insert.
-- Existing immutable-profile/identity triggers already forbid moving either
-- row to another Page by UPDATE. No existing document is converted or removed.
CREATE TRIGGER craft_native_excludes_collaboration_insert
BEFORE INSERT ON craft_native_documents
WHEN EXISTS(SELECT 1 FROM mooring_collab_documents WHERE page_id=NEW.page_id)
BEGIN SELECT RAISE(ABORT,'Page already contains collaboration custody'); END;

CREATE TRIGGER mooring_collaboration_excludes_native_insert
BEFORE INSERT ON mooring_collab_documents
WHEN EXISTS(SELECT 1 FROM craft_native_documents WHERE page_id=NEW.page_id)
BEGIN SELECT RAISE(ABORT,'Page already contains native Craft custody'); END;
