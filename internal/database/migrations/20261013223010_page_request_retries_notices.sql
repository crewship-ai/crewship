ALTER TABLE page_artifact_requests ADD COLUMN retry_of_request_id TEXT REFERENCES page_artifact_requests(id) ON DELETE SET NULL;
ALTER TABLE page_artifact_requests ADD COLUMN notice_pending INTEGER NOT NULL DEFAULT 0 CHECK(notice_pending IN(0,1));
CREATE INDEX idx_artifact_request_retry ON page_artifact_requests(retry_of_request_id);
ALTER TABLE page_board_requests ADD COLUMN retry_of_request_id TEXT REFERENCES page_board_requests(id) ON DELETE SET NULL;
ALTER TABLE page_board_requests ADD COLUMN notice_pending INTEGER NOT NULL DEFAULT 0 CHECK(notice_pending IN(0,1));
CREATE INDEX idx_board_request_retry ON page_board_requests(retry_of_request_id);
CREATE TRIGGER artifact_request_retry_scope BEFORE INSERT ON page_artifact_requests
WHEN NEW.retry_of_request_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM page_artifact_requests p WHERE p.id=NEW.retry_of_request_id AND p.page_id=NEW.page_id AND p.human_user_id IS NEW.human_user_id AND p.status IN('failed','cancelled'))
BEGIN SELECT RAISE(ABORT,'artifact retry parent scope mismatch'); END;
CREATE TRIGGER board_request_retry_scope BEFORE INSERT ON page_board_requests
WHEN NEW.retry_of_request_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM page_board_requests p WHERE p.id=NEW.retry_of_request_id AND p.workspace_id=NEW.workspace_id AND p.board_page_id IS NEW.board_page_id AND p.original_board_id=NEW.original_board_id AND p.human_user_id IS NEW.human_user_id AND p.kind=NEW.kind AND p.status IN('failed','cancelled'))
BEGIN SELECT RAISE(ABORT,'board retry parent scope mismatch'); END;
CREATE TRIGGER artifact_request_retry_immutable BEFORE UPDATE OF retry_of_request_id ON page_artifact_requests
WHEN NEW.retry_of_request_id IS NOT OLD.retry_of_request_id AND NOT(NEW.retry_of_request_id IS NULL AND NOT EXISTS(SELECT 1 FROM page_artifact_requests WHERE id=OLD.retry_of_request_id))
BEGIN SELECT RAISE(ABORT,'immutable artifact retry lineage'); END;
CREATE TRIGGER board_request_retry_immutable BEFORE UPDATE OF retry_of_request_id ON page_board_requests
WHEN NEW.retry_of_request_id IS NOT OLD.retry_of_request_id AND NOT(NEW.retry_of_request_id IS NULL AND NOT EXISTS(SELECT 1 FROM page_board_requests WHERE id=OLD.retry_of_request_id))
BEGIN SELECT RAISE(ABORT,'immutable board retry lineage'); END;
