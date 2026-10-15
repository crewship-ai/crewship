-- Legacy requests have no selected authority and cannot be rebound for execution.
ALTER TABLE document_requests ADD COLUMN targets_json TEXT NOT NULL DEFAULT '[]';
CREATE TRIGGER document_request_targets_immutable
BEFORE UPDATE OF targets_json,base_rev,page_id ON document_requests
WHEN NEW.targets_json IS NOT OLD.targets_json OR NEW.base_rev IS NOT OLD.base_rev OR NEW.page_id IS NOT OLD.page_id
BEGIN
 SELECT RAISE(ABORT,'document request target snapshot is immutable');
END;

-- Revoke the complete runtime principal before the request FK removes its
-- origin delegation. Include the delegation lookup after a historical run_id
-- reset. The principal row remains for audit; recapture never renews its expiry.
CREATE TRIGGER document_request_delete_revoke_run
BEFORE DELETE ON document_requests
BEGIN
 UPDATE artifact_run_principals SET expires_at='1970-01-01T00:00:00Z'
 WHERE run_id=OLD.run_id OR run_id IN (
  SELECT run_id FROM page_artifact_run_delegations WHERE document_request_id=OLD.id
 );
 UPDATE page_artifact_run_delegations
 SET revoked_at=COALESCE(revoked_at,strftime('%Y-%m-%dT%H:%M:%fZ','now'))
 WHERE run_id=OLD.run_id OR run_id IN (
  SELECT run_id FROM page_artifact_run_delegations WHERE document_request_id=OLD.id
 );
END;
