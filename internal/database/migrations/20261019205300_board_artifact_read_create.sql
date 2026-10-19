-- Empty Board read/create baseline. Profile and init receipt conventions are
-- shared with the existing Board foundation; drawing writes remain disabled.
CREATE TABLE page_boards (
 page_id TEXT PRIMARY KEY REFERENCES pages(id) ON DELETE CASCADE,
 seq INTEGER NOT NULL CHECK(seq>=0),
 format TEXT NOT NULL CHECK(format='crewship-board-excalidraw-0.18.1-v1')
);
CREATE TABLE page_board_mutations (
 board_page_id TEXT NOT NULL REFERENCES page_boards(page_id) ON DELETE CASCADE,
 mutation_id TEXT NOT NULL,
 author_user_id TEXT REFERENCES users(id) ON DELETE SET NULL,
 fingerprint TEXT NOT NULL,
 result_seq INTEGER NOT NULL,
 created_at TEXT NOT NULL,
 PRIMARY KEY(board_page_id,mutation_id)
);
CREATE TRIGGER page_boards_require_private_human_insert BEFORE INSERT ON page_boards
WHEN NOT EXISTS(SELECT 1 FROM pages WHERE id=NEW.page_id AND owner_user_id IS NOT NULL AND owner_crew_id IS NULL AND private_audience=1)
BEGIN SELECT RAISE(ABORT,'board requires private human ownership'); END;
CREATE TRIGGER page_boards_require_private_human_update BEFORE UPDATE OF page_id ON page_boards
WHEN NOT EXISTS(SELECT 1 FROM pages WHERE id=NEW.page_id AND owner_user_id IS NOT NULL AND owner_crew_id IS NULL AND private_audience=1)
BEGIN SELECT RAISE(ABORT,'board requires private human ownership'); END;
CREATE TRIGGER pages_preserve_board_custody BEFORE UPDATE OF owner_user_id,owner_crew_id,private_audience ON pages
WHEN EXISTS(SELECT 1 FROM page_boards WHERE page_id=OLD.id)
 AND (NEW.owner_user_id IS NULL OR NEW.owner_crew_id IS NOT NULL OR NEW.private_audience<>1)
BEGIN SELECT RAISE(ABORT,'board requires private human ownership'); END;
CREATE TRIGGER page_boards_exclusive_insert BEFORE INSERT ON page_boards
WHEN EXISTS(SELECT 1 FROM documents WHERE page_id=NEW.page_id)
 OR EXISTS(SELECT 1 FROM slides_heads WHERE page_id=NEW.page_id)
 OR EXISTS(SELECT 1 FROM page_panels WHERE page_id=NEW.page_id)
 OR EXISTS(SELECT 1 FROM page_project_drafts WHERE page_id=NEW.page_id)
 OR EXISTS(SELECT 1 FROM page_project_live WHERE page_id=NEW.page_id)
BEGIN SELECT RAISE(ABORT,'board page already has content'); END;
CREATE TRIGGER page_boards_exclusive_update BEFORE UPDATE OF page_id ON page_boards
WHEN EXISTS(SELECT 1 FROM documents WHERE page_id=NEW.page_id)
 OR EXISTS(SELECT 1 FROM slides_heads WHERE page_id=NEW.page_id)
 OR EXISTS(SELECT 1 FROM page_panels WHERE page_id=NEW.page_id)
 OR EXISTS(SELECT 1 FROM page_project_drafts WHERE page_id=NEW.page_id)
 OR EXISTS(SELECT 1 FROM page_project_live WHERE page_id=NEW.page_id)
BEGIN SELECT RAISE(ABORT,'board page already has content'); END;
CREATE TRIGGER documents_exclude_board_insert BEFORE INSERT ON documents
WHEN EXISTS(SELECT 1 FROM page_boards WHERE page_id=NEW.page_id)
BEGIN SELECT RAISE(ABORT,'board page cannot attach another content type'); END;
CREATE TRIGGER documents_exclude_board_update BEFORE UPDATE OF page_id ON documents
WHEN EXISTS(SELECT 1 FROM page_boards WHERE page_id=NEW.page_id)
BEGIN SELECT RAISE(ABORT,'board page cannot attach another content type'); END;
CREATE TRIGGER slides_heads_exclude_board_insert BEFORE INSERT ON slides_heads
WHEN EXISTS(SELECT 1 FROM page_boards WHERE page_id=NEW.page_id)
BEGIN SELECT RAISE(ABORT,'board page cannot attach another content type'); END;
CREATE TRIGGER slides_heads_exclude_board_update BEFORE UPDATE OF page_id ON slides_heads
WHEN EXISTS(SELECT 1 FROM page_boards WHERE page_id=NEW.page_id)
BEGIN SELECT RAISE(ABORT,'board page cannot attach another content type'); END;
CREATE TRIGGER page_panels_exclude_board_insert BEFORE INSERT ON page_panels
WHEN EXISTS(SELECT 1 FROM page_boards WHERE page_id=NEW.page_id)
BEGIN SELECT RAISE(ABORT,'board page cannot attach another content type'); END;
CREATE TRIGGER page_panels_exclude_board_update BEFORE UPDATE OF page_id ON page_panels
WHEN EXISTS(SELECT 1 FROM page_boards WHERE page_id=NEW.page_id)
BEGIN SELECT RAISE(ABORT,'board page cannot attach another content type'); END;
CREATE TRIGGER page_project_drafts_exclude_board_insert BEFORE INSERT ON page_project_drafts
WHEN EXISTS(SELECT 1 FROM page_boards WHERE page_id=NEW.page_id)
BEGIN SELECT RAISE(ABORT,'board page cannot attach another content type'); END;
CREATE TRIGGER page_project_drafts_exclude_board_update BEFORE UPDATE OF page_id ON page_project_drafts
WHEN EXISTS(SELECT 1 FROM page_boards WHERE page_id=NEW.page_id)
BEGIN SELECT RAISE(ABORT,'board page cannot attach another content type'); END;
CREATE TRIGGER page_project_live_exclude_board_insert BEFORE INSERT ON page_project_live
WHEN EXISTS(SELECT 1 FROM page_boards WHERE page_id=NEW.page_id)
BEGIN SELECT RAISE(ABORT,'board page cannot attach another content type'); END;
CREATE TRIGGER page_project_live_exclude_board_update BEFORE UPDATE OF page_id ON page_project_live
WHEN EXISTS(SELECT 1 FROM page_boards WHERE page_id=NEW.page_id)
BEGIN SELECT RAISE(ABORT,'board page cannot attach another content type'); END;
