-- Revisions are counters, not timestamps. Aggregate-only maintenance must
-- not recursively bump the issue field revision.
ALTER TABLE missions ADD COLUMN revision INTEGER NOT NULL DEFAULT 1;
ALTER TABLE missions ADD COLUMN aggregate_revision INTEGER NOT NULL DEFAULT 1;
CREATE TRIGGER missions_revision AFTER UPDATE ON missions
WHEN NEW.revision = OLD.revision AND NEW.aggregate_revision = OLD.aggregate_revision
BEGIN
 UPDATE missions SET revision = revision + 1, aggregate_revision = aggregate_revision + 1 WHERE id = NEW.id;
END;
-- A child's visible fields affect its parent's subissue summary. Aggregate
-- maintenance itself never propagates, avoiding recursive cycles.
CREATE TRIGGER missions_parent_revision AFTER UPDATE OF revision ON missions
WHEN NEW.revision <> OLD.revision
BEGIN
 UPDATE missions SET aggregate_revision = aggregate_revision + 1 WHERE id = NEW.parent_issue_id;
END;
CREATE TRIGGER missions_parent_insert AFTER INSERT ON missions
BEGIN
 UPDATE missions SET aggregate_revision = aggregate_revision + 1 WHERE id = NEW.parent_issue_id;
END;
CREATE TRIGGER missions_parent_delete AFTER DELETE ON missions
BEGIN
 UPDATE missions SET aggregate_revision = aggregate_revision + 1 WHERE id = OLD.parent_issue_id;
END;
CREATE TRIGGER missions_parent_move AFTER UPDATE OF parent_issue_id ON missions
WHEN NEW.parent_issue_id IS NOT OLD.parent_issue_id
BEGIN
 UPDATE missions SET aggregate_revision = aggregate_revision + 1 WHERE id IN (NEW.parent_issue_id, OLD.parent_issue_id);
END;
CREATE TRIGGER mission_comments_aggregate_insert AFTER INSERT ON mission_comments
BEGIN
 UPDATE missions SET aggregate_revision = aggregate_revision + 1 WHERE id IN (NEW.mission_id);
END;
CREATE TRIGGER mission_comments_aggregate_update AFTER UPDATE ON mission_comments
BEGIN
 UPDATE missions SET aggregate_revision = aggregate_revision + 1 WHERE id IN (NEW.mission_id, OLD.mission_id);
END;
CREATE TRIGGER mission_comments_aggregate_delete AFTER DELETE ON mission_comments
BEGIN
 UPDATE missions SET aggregate_revision = aggregate_revision + 1 WHERE id IN (OLD.mission_id);
END;
CREATE TRIGGER mission_labels_aggregate_insert AFTER INSERT ON mission_labels
BEGIN
 UPDATE missions SET aggregate_revision = aggregate_revision + 1 WHERE id IN (NEW.mission_id);
END;
CREATE TRIGGER mission_labels_aggregate_update AFTER UPDATE ON mission_labels
BEGIN
 UPDATE missions SET aggregate_revision = aggregate_revision + 1 WHERE id IN (NEW.mission_id, OLD.mission_id);
END;
CREATE TRIGGER mission_labels_aggregate_delete AFTER DELETE ON mission_labels
BEGIN
 UPDATE missions SET aggregate_revision = aggregate_revision + 1 WHERE id IN (OLD.mission_id);
END;
CREATE TRIGGER mission_code_links_aggregate_insert AFTER INSERT ON mission_code_links
BEGIN
 UPDATE missions SET aggregate_revision = aggregate_revision + 1 WHERE id IN (NEW.mission_id);
END;
CREATE TRIGGER mission_code_links_aggregate_update AFTER UPDATE ON mission_code_links
BEGIN
 UPDATE missions SET aggregate_revision = aggregate_revision + 1 WHERE id IN (NEW.mission_id, OLD.mission_id);
END;
CREATE TRIGGER mission_code_links_aggregate_delete AFTER DELETE ON mission_code_links
BEGIN
 UPDATE missions SET aggregate_revision = aggregate_revision + 1 WHERE id IN (OLD.mission_id);
END;
CREATE TRIGGER mission_relations_aggregate_insert AFTER INSERT ON mission_relations
BEGIN
 UPDATE missions SET aggregate_revision = aggregate_revision + 1 WHERE id IN (NEW.source_id, NEW.target_id);
END;
CREATE TRIGGER mission_relations_aggregate_update AFTER UPDATE ON mission_relations
BEGIN
 UPDATE missions SET aggregate_revision = aggregate_revision + 1 WHERE id IN (NEW.source_id, NEW.target_id, OLD.source_id, OLD.target_id);
END;
CREATE TRIGGER mission_relations_aggregate_delete AFTER DELETE ON mission_relations
BEGIN
 UPDATE missions SET aggregate_revision = aggregate_revision + 1 WHERE id IN (OLD.source_id, OLD.target_id);
END;
CREATE TRIGGER attachments_aggregate_insert AFTER INSERT ON attachments
BEGIN
 UPDATE missions SET aggregate_revision = aggregate_revision + 1 WHERE id IN (NEW.mission_id, (SELECT mission_id FROM mission_comments WHERE id = NEW.comment_id));
END;
CREATE TRIGGER attachments_aggregate_update AFTER UPDATE ON attachments
BEGIN
 UPDATE missions SET aggregate_revision = aggregate_revision + 1 WHERE id IN (NEW.mission_id, OLD.mission_id, (SELECT mission_id FROM mission_comments WHERE id = NEW.comment_id), (SELECT mission_id FROM mission_comments WHERE id = OLD.comment_id));
END;
CREATE TRIGGER attachments_aggregate_delete AFTER DELETE ON attachments
BEGIN
 UPDATE missions SET aggregate_revision = aggregate_revision + 1 WHERE id IN (OLD.mission_id, (SELECT mission_id FROM mission_comments WHERE id = OLD.comment_id));
END;
