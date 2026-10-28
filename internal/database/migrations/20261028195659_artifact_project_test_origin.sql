-- A QA marker is explicit human creation purpose, not a model argument.
-- Existing intents remain unmarked; no retrospective inference or retagging.
ALTER TABLE artifact_project_message_intents ADD COLUMN created_by_test INTEGER NOT NULL DEFAULT 0
 CHECK (created_by_test IN (0,1) AND (created_by_test=0 OR (page_id IS NULL AND comment_id IS NULL AND base_version=0)));
CREATE TRIGGER artifact_project_message_test_origin_immutable
 BEFORE UPDATE OF created_by_test ON artifact_project_message_intents
 WHEN NEW.created_by_test IS NOT OLD.created_by_test
 BEGIN SELECT RAISE(ABORT,'artifact message test origin is immutable'); END;
