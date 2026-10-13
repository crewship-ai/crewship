-- Prior baselines remain available for exactly the receipt/undo retention window.
CREATE TABLE page_board_source_history (
 board_page_id TEXT NOT NULL,
 mutation_id TEXT NOT NULL,
 placement_id TEXT NOT NULL REFERENCES page_board_artifact_placements(id) ON DELETE CASCADE,
 source_page_id TEXT,
 source_revision INTEGER,
 PRIMARY KEY(board_page_id,mutation_id,placement_id),
 FOREIGN KEY(board_page_id,mutation_id) REFERENCES page_board_mutations(board_page_id,mutation_id) ON DELETE CASCADE DEFERRABLE INITIALLY DEFERRED,
 FOREIGN KEY(source_page_id,source_revision) REFERENCES page_artifact_revisions(page_id,revision) ON DELETE SET NULL,
 CHECK((source_page_id IS NULL AND source_revision IS NULL) OR(source_page_id IS NOT NULL AND source_revision>0))
);
CREATE INDEX idx_board_history_placement ON page_board_source_history(placement_id);
CREATE INDEX idx_board_history_source ON page_board_source_history(source_page_id,source_revision);
CREATE INDEX idx_board_history_revision ON page_board_source_history(source_revision);
CREATE INDEX idx_board_history_mutation ON page_board_source_history(mutation_id,board_page_id);
CREATE TRIGGER board_history_source_scope BEFORE INSERT ON page_board_source_history
WHEN NOT EXISTS(SELECT 1 FROM page_board_artifact_placements p WHERE p.id=NEW.placement_id AND p.board_page_id=NEW.board_page_id AND (NEW.source_page_id IS NULL OR p.source_page_id=NEW.source_page_id))
BEGIN SELECT RAISE(ABORT,'board source history must belong to the same placement source'); END;
CREATE TRIGGER board_history_source_immutable BEFORE UPDATE ON page_board_source_history
WHEN NEW.board_page_id IS NOT OLD.board_page_id OR NEW.mutation_id IS NOT OLD.mutation_id OR NEW.placement_id IS NOT OLD.placement_id
 OR (NEW.source_page_id IS NOT NULL AND (NEW.source_page_id IS NOT OLD.source_page_id OR NEW.source_revision IS NOT OLD.source_revision))
 OR (NEW.source_page_id IS NULL AND OLD.source_page_id IS NOT NULL AND EXISTS(SELECT 1 FROM page_artifact_revisions WHERE page_id=OLD.source_page_id AND revision=OLD.source_revision))
BEGIN SELECT RAISE(ABORT,'board source history is immutable'); END;
