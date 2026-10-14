ALTER TABLE page_artifact_requests ADD COLUMN result_kind TEXT NOT NULL DEFAULT 'revision' CHECK(result_kind IN ('revision','reply'));
CREATE TRIGGER artifact_request_result_kind_immutable BEFORE UPDATE OF result_kind ON page_artifact_requests WHEN NEW.result_kind IS NOT OLD.result_kind BEGIN SELECT RAISE(ABORT,'immutable request result kind'); END;
CREATE TABLE page_artifact_request_updates (
 id TEXT NOT NULL PRIMARY KEY,
 request_id TEXT NOT NULL REFERENCES page_artifact_requests(id) ON DELETE CASCADE,
 agent_id TEXT REFERENCES agents(id) ON DELETE SET NULL,
 run_id TEXT NOT NULL,
 on_behalf_of_user_id TEXT REFERENCES users(id) ON DELETE SET NULL,
 mutation_id TEXT NOT NULL,
 fingerprint TEXT NOT NULL,
 operation TEXT NOT NULL CHECK(operation IN ('progress','reply')),
 phase TEXT NOT NULL DEFAULT '',
 message TEXT NOT NULL DEFAULT '',
 body TEXT NOT NULL DEFAULT '',
 created_at TEXT NOT NULL,
 UNIQUE(request_id,run_id,mutation_id),
 CHECK((operation='progress' AND phase IN ('preparing','waiting_for_input','uploading','finalizing') AND body='') OR (operation='reply' AND phase='' AND message='' AND length(body)>0))
);
CREATE INDEX idx_artifact_request_update_agent ON page_artifact_request_updates(agent_id);
CREATE INDEX idx_artifact_request_update_human ON page_artifact_request_updates(on_behalf_of_user_id);
CREATE UNIQUE INDEX idx_artifact_request_single_reply ON page_artifact_request_updates(request_id) WHERE operation='reply';
CREATE TRIGGER artifact_request_updates_immutable BEFORE UPDATE ON page_artifact_request_updates
WHEN NEW.id IS NOT OLD.id OR NEW.request_id IS NOT OLD.request_id OR NEW.run_id IS NOT OLD.run_id OR NEW.mutation_id IS NOT OLD.mutation_id OR NEW.fingerprint IS NOT OLD.fingerprint OR NEW.operation IS NOT OLD.operation OR NEW.phase IS NOT OLD.phase OR NEW.message IS NOT OLD.message OR NEW.body IS NOT OLD.body OR NEW.created_at IS NOT OLD.created_at OR (NEW.agent_id IS NOT OLD.agent_id AND NOT(NEW.agent_id IS NULL AND NOT EXISTS(SELECT 1 FROM agents WHERE id=OLD.agent_id))) OR (NEW.on_behalf_of_user_id IS NOT OLD.on_behalf_of_user_id AND NOT(NEW.on_behalf_of_user_id IS NULL AND NOT EXISTS(SELECT 1 FROM users WHERE id=OLD.on_behalf_of_user_id)))
BEGIN SELECT RAISE(ABORT,'immutable agent request update'); END;
