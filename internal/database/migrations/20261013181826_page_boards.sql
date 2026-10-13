CREATE TABLE page_boards (
 page_id TEXT PRIMARY KEY REFERENCES pages(id) ON DELETE CASCADE,
 seq INTEGER NOT NULL CHECK(seq>=0),
 format TEXT NOT NULL CHECK(format='crewship-board-excalidraw-0.18.1-v1')
);
CREATE TABLE page_board_elements (
 board_page_id TEXT NOT NULL REFERENCES page_boards(page_id) ON DELETE CASCADE,
 element_id TEXT NOT NULL,
 element_rev INTEGER NOT NULL CHECK(element_rev>0),
 element_json TEXT,
 author_user_id TEXT REFERENCES users(id) ON DELETE SET NULL,
 updated_seq INTEGER NOT NULL,
 PRIMARY KEY(board_page_id,element_id)
);
CREATE INDEX idx_board_element_author ON page_board_elements(author_user_id);
CREATE TABLE page_board_mutations (
 board_page_id TEXT NOT NULL REFERENCES page_boards(page_id) ON DELETE CASCADE,
 mutation_id TEXT NOT NULL,
 author_user_id TEXT REFERENCES users(id) ON DELETE SET NULL,
 fingerprint TEXT NOT NULL,
 result_seq INTEGER NOT NULL,
 placement_id TEXT NOT NULL DEFAULT '',
 created_at TEXT NOT NULL,
 PRIMARY KEY(board_page_id,mutation_id)
);
CREATE INDEX idx_board_mutation_author ON page_board_mutations(author_user_id);
CREATE TABLE page_board_artifact_placements (
 id TEXT PRIMARY KEY,
 board_page_id TEXT NOT NULL REFERENCES page_boards(page_id) ON DELETE CASCADE,
 source_page_id TEXT,
 source_revision INTEGER,
 x REAL NOT NULL, y REAL NOT NULL, width REAL NOT NULL, height REAL NOT NULL,
 updated_seq INTEGER NOT NULL,
 author_user_id TEXT REFERENCES users(id) ON DELETE SET NULL,
 CHECK((source_page_id IS NULL AND source_revision IS NULL) OR (source_page_id IS NOT NULL AND source_revision>0)),
 FOREIGN KEY(source_page_id,source_revision) REFERENCES page_artifact_revisions(page_id,revision) ON DELETE SET NULL
);
CREATE INDEX idx_board_placement_board ON page_board_artifact_placements(board_page_id);
CREATE INDEX idx_board_placement_source ON page_board_artifact_placements(source_page_id,source_revision);
CREATE INDEX idx_board_placement_revision ON page_board_artifact_placements(source_revision);
CREATE INDEX idx_board_placement_author ON page_board_artifact_placements(author_user_id);
CREATE TRIGGER board_empty_page BEFORE INSERT ON page_boards
WHEN NOT EXISTS(SELECT 1 FROM pages WHERE id=NEW.page_id AND owner_user_id IS NOT NULL AND owner_crew_id IS NULL)
 OR EXISTS(SELECT 1 FROM page_panels WHERE page_id=NEW.page_id)
 OR EXISTS(SELECT 1 FROM page_project_drafts WHERE page_id=NEW.page_id)
 OR EXISTS(SELECT 1 FROM page_project_live WHERE page_id=NEW.page_id)
 OR EXISTS(SELECT 1 FROM page_artifacts WHERE page_id=NEW.page_id)
BEGIN SELECT RAISE(ABORT,'board requires an empty human-owned page'); END;
CREATE TRIGGER board_no_artifact BEFORE INSERT ON page_artifacts WHEN EXISTS(SELECT 1 FROM page_boards WHERE page_id=NEW.page_id)
BEGIN SELECT RAISE(ABORT,'board cannot contain artifact'); END;
CREATE TRIGGER board_no_panels BEFORE INSERT ON page_panels WHEN EXISTS(SELECT 1 FROM page_boards WHERE page_id=NEW.page_id)
BEGIN SELECT RAISE(ABORT,'board cannot contain panels'); END;
CREATE TRIGGER board_no_project BEFORE INSERT ON page_project_drafts WHEN EXISTS(SELECT 1 FROM page_boards WHERE page_id=NEW.page_id)
BEGIN SELECT RAISE(ABORT,'board cannot contain project'); END;
CREATE TRIGGER board_no_live_project BEFORE INSERT ON page_project_live WHEN EXISTS(SELECT 1 FROM page_boards WHERE page_id=NEW.page_id)
BEGIN SELECT RAISE(ABORT,'board cannot contain application'); END;
CREATE TRIGGER board_human_owner BEFORE UPDATE OF owner_user_id,owner_crew_id ON pages
WHEN EXISTS(SELECT 1 FROM page_boards WHERE page_id=NEW.id) AND (NEW.owner_user_id IS NULL OR NEW.owner_crew_id IS NOT NULL)
BEGIN SELECT RAISE(ABORT,'board owner must be human'); END;
CREATE TRIGGER board_no_panel_move BEFORE UPDATE OF page_id ON page_panels WHEN EXISTS(SELECT 1 FROM page_boards WHERE page_id=NEW.page_id)
BEGIN SELECT RAISE(ABORT,'board cannot contain panels'); END;
CREATE TRIGGER board_no_project_move BEFORE UPDATE OF page_id ON page_project_drafts WHEN EXISTS(SELECT 1 FROM page_boards WHERE page_id=NEW.page_id)
BEGIN SELECT RAISE(ABORT,'board cannot contain project'); END;
CREATE TRIGGER board_no_live_project_move BEFORE UPDATE OF page_id ON page_project_live WHEN EXISTS(SELECT 1 FROM page_boards WHERE page_id=NEW.page_id)
BEGIN SELECT RAISE(ABORT,'board cannot contain application'); END;
CREATE TRIGGER board_no_artifact_move BEFORE UPDATE OF page_id ON page_artifacts WHEN EXISTS(SELECT 1 FROM page_boards WHERE page_id=NEW.page_id)
BEGIN SELECT RAISE(ABORT,'board cannot contain artifact'); END;
CREATE TRIGGER board_placement_workspace BEFORE INSERT ON page_board_artifact_placements
WHEN NEW.source_page_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM pages b JOIN pages s ON s.workspace_id=b.workspace_id WHERE b.id=NEW.board_page_id AND s.id=NEW.source_page_id)
BEGIN SELECT RAISE(ABORT,'board source must belong to same workspace'); END;
CREATE TRIGGER board_placement_workspace_update BEFORE UPDATE OF board_page_id,source_page_id ON page_board_artifact_placements
WHEN NEW.source_page_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM pages b JOIN pages s ON s.workspace_id=b.workspace_id WHERE b.id=NEW.board_page_id AND s.id=NEW.source_page_id)
BEGIN SELECT RAISE(ABORT,'board source must belong to same workspace'); END;

CREATE TRIGGER board_empty_page_move BEFORE UPDATE OF page_id ON page_boards
WHEN NEW.page_id<>OLD.page_id AND (
 NOT EXISTS(SELECT 1 FROM pages WHERE id=NEW.page_id AND owner_user_id IS NOT NULL AND owner_crew_id IS NULL)
 OR EXISTS(SELECT 1 FROM page_panels WHERE page_id=NEW.page_id)
 OR EXISTS(SELECT 1 FROM page_project_drafts WHERE page_id=NEW.page_id)
 OR EXISTS(SELECT 1 FROM page_project_live WHERE page_id=NEW.page_id)
 OR EXISTS(SELECT 1 FROM page_artifacts WHERE page_id=NEW.page_id))
BEGIN SELECT RAISE(ABORT,'board requires an empty human-owned page'); END;
