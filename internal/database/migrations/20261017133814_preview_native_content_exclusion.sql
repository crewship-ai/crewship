-- Preview bridge for the already-issued Artifact/Board storage and typed Docs/Slides.
-- Preserve the original migrations; add reciprocal insert and move guards.

CREATE TRIGGER page_artifacts_exclude_documents_insert BEFORE INSERT ON page_artifacts
WHEN EXISTS(SELECT 1 FROM documents WHERE page_id=NEW.page_id)
BEGIN SELECT RAISE(ABORT,'Page already has another content type'); END;

CREATE TRIGGER page_artifacts_exclude_documents_update BEFORE UPDATE OF page_id ON page_artifacts
WHEN EXISTS(SELECT 1 FROM documents WHERE page_id=NEW.page_id)
BEGIN SELECT RAISE(ABORT,'Page already has another content type'); END;

CREATE TRIGGER documents_exclude_page_artifacts_insert BEFORE INSERT ON documents
WHEN EXISTS(SELECT 1 FROM page_artifacts WHERE page_id=NEW.page_id)
BEGIN SELECT RAISE(ABORT,'Page already has another content type'); END;

CREATE TRIGGER documents_exclude_page_artifacts_update BEFORE UPDATE OF page_id ON documents
WHEN EXISTS(SELECT 1 FROM page_artifacts WHERE page_id=NEW.page_id)
BEGIN SELECT RAISE(ABORT,'Page already has another content type'); END;

CREATE TRIGGER page_artifacts_exclude_slides_heads_insert BEFORE INSERT ON page_artifacts
WHEN EXISTS(SELECT 1 FROM slides_heads WHERE page_id=NEW.page_id)
BEGIN SELECT RAISE(ABORT,'Page already has another content type'); END;

CREATE TRIGGER page_artifacts_exclude_slides_heads_update BEFORE UPDATE OF page_id ON page_artifacts
WHEN EXISTS(SELECT 1 FROM slides_heads WHERE page_id=NEW.page_id)
BEGIN SELECT RAISE(ABORT,'Page already has another content type'); END;

CREATE TRIGGER slides_heads_exclude_page_artifacts_insert BEFORE INSERT ON slides_heads
WHEN EXISTS(SELECT 1 FROM page_artifacts WHERE page_id=NEW.page_id)
BEGIN SELECT RAISE(ABORT,'Page already has another content type'); END;

CREATE TRIGGER slides_heads_exclude_page_artifacts_update BEFORE UPDATE OF page_id ON slides_heads
WHEN EXISTS(SELECT 1 FROM page_artifacts WHERE page_id=NEW.page_id)
BEGIN SELECT RAISE(ABORT,'Page already has another content type'); END;

CREATE TRIGGER page_boards_exclude_documents_insert BEFORE INSERT ON page_boards
WHEN EXISTS(SELECT 1 FROM documents WHERE page_id=NEW.page_id)
BEGIN SELECT RAISE(ABORT,'Page already has another content type'); END;

CREATE TRIGGER page_boards_exclude_documents_update BEFORE UPDATE OF page_id ON page_boards
WHEN EXISTS(SELECT 1 FROM documents WHERE page_id=NEW.page_id)
BEGIN SELECT RAISE(ABORT,'Page already has another content type'); END;

CREATE TRIGGER documents_exclude_page_boards_insert BEFORE INSERT ON documents
WHEN EXISTS(SELECT 1 FROM page_boards WHERE page_id=NEW.page_id)
BEGIN SELECT RAISE(ABORT,'Page already has another content type'); END;

CREATE TRIGGER documents_exclude_page_boards_update BEFORE UPDATE OF page_id ON documents
WHEN EXISTS(SELECT 1 FROM page_boards WHERE page_id=NEW.page_id)
BEGIN SELECT RAISE(ABORT,'Page already has another content type'); END;

CREATE TRIGGER page_boards_exclude_slides_heads_insert BEFORE INSERT ON page_boards
WHEN EXISTS(SELECT 1 FROM slides_heads WHERE page_id=NEW.page_id)
BEGIN SELECT RAISE(ABORT,'Page already has another content type'); END;

CREATE TRIGGER page_boards_exclude_slides_heads_update BEFORE UPDATE OF page_id ON page_boards
WHEN EXISTS(SELECT 1 FROM slides_heads WHERE page_id=NEW.page_id)
BEGIN SELECT RAISE(ABORT,'Page already has another content type'); END;

CREATE TRIGGER slides_heads_exclude_page_boards_insert BEFORE INSERT ON slides_heads
WHEN EXISTS(SELECT 1 FROM page_boards WHERE page_id=NEW.page_id)
BEGIN SELECT RAISE(ABORT,'Page already has another content type'); END;

CREATE TRIGGER slides_heads_exclude_page_boards_update BEFORE UPDATE OF page_id ON slides_heads
WHEN EXISTS(SELECT 1 FROM page_boards WHERE page_id=NEW.page_id)
BEGIN SELECT RAISE(ABORT,'Page already has another content type'); END;
