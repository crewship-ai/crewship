-- Legacy requests have no selected authority and cannot be rebound for execution.
ALTER TABLE document_requests ADD COLUMN targets_json TEXT NOT NULL DEFAULT '[]';
CREATE TRIGGER document_request_targets_immutable
BEFORE UPDATE OF targets_json,base_rev,page_id ON document_requests
WHEN NEW.targets_json IS NOT OLD.targets_json OR NEW.base_rev IS NOT OLD.base_rev OR NEW.page_id IS NOT OLD.page_id
BEGIN
 SELECT RAISE(ABORT,'document request target snapshot is immutable');
END;
