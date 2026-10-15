-- Keep explicit request origin on runtime authority even when historical request
-- rows clear run_id. Generic Artifact delegations continue to use NULL here.
ALTER TABLE page_artifact_run_delegations ADD COLUMN document_request_id TEXT REFERENCES document_requests(id) ON DELETE CASCADE;
UPDATE page_artifact_run_delegations
SET document_request_id=(SELECT d.id FROM document_requests d WHERE d.page_id=page_artifact_run_delegations.page_id AND d.run_id=page_artifact_run_delegations.run_id LIMIT 1)
WHERE EXISTS(SELECT 1 FROM document_requests d WHERE d.page_id=page_artifact_run_delegations.page_id AND d.run_id=page_artifact_run_delegations.run_id);
CREATE INDEX idx_document_request_run_origin ON page_artifact_run_delegations(document_request_id) WHERE document_request_id IS NOT NULL;
CREATE TRIGGER document_request_run_origin_immutable
BEFORE UPDATE OF document_request_id ON page_artifact_run_delegations
WHEN OLD.document_request_id IS NOT NULL AND NEW.document_request_id IS NOT OLD.document_request_id
BEGIN
 SELECT RAISE(ABORT,'document request run origin is immutable');
END;
