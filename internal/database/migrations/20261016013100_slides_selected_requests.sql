-- Request protocol and selection schema are independent of deck versions.
CREATE TABLE slides_requests (
 id TEXT PRIMARY KEY,workspace_id TEXT NOT NULL,page_id TEXT NOT NULL,
 request_seq INTEGER NOT NULL CHECK(request_seq>=1),
 protocol_version INTEGER NOT NULL CHECK(protocol_version=1),
 origin_request_id TEXT NOT NULL,human_actor TEXT NOT NULL,agent_actor TEXT NOT NULL,
 human_user_id TEXT REFERENCES users(id) ON DELETE SET NULL,
 chat_id TEXT REFERENCES chats(id) ON DELETE SET NULL,
 agent_id TEXT REFERENCES agents(id) ON DELETE SET NULL,
 base_rev INTEGER NOT NULL CHECK(base_rev>=1),base_schema_version INTEGER NOT NULL CHECK(base_schema_version>=1),
 base_content_hash TEXT NOT NULL,input_json TEXT NOT NULL,verified_json TEXT NOT NULL,
 submission_id TEXT NOT NULL,fingerprint TEXT NOT NULL,
 audience_json TEXT NOT NULL,credential_kind TEXT NOT NULL CHECK(credential_kind IN ('cli','session')),
 credential_id TEXT NOT NULL,credential_hash TEXT NOT NULL,
 status TEXT NOT NULL CHECK(status IN ('queued','running','succeeded','failed','cancelled','interrupted')),
 version INTEGER NOT NULL CHECK(version>=1),restored INTEGER NOT NULL DEFAULT 0 CHECK(restored IN (0,1)),
 run_id TEXT UNIQUE,original_run_id TEXT,
 result_revision INTEGER,result_hash TEXT,
 error TEXT NOT NULL DEFAULT '',
 created_at TEXT NOT NULL DEFAULT(strftime('%Y-%m-%dT%H:%M:%fZ','now')),
 updated_at TEXT NOT NULL DEFAULT(strftime('%Y-%m-%dT%H:%M:%fZ','now')),
 UNIQUE(workspace_id,page_id,id),UNIQUE(workspace_id,page_id,request_seq),
 UNIQUE(workspace_id,page_id,human_actor,submission_id),
 CHECK((result_revision IS NULL AND result_hash IS NULL) OR (result_revision=base_rev+1 AND result_hash IS NOT NULL)),
 CHECK(status<>'succeeded' OR result_revision IS NOT NULL),
 FOREIGN KEY(workspace_id,page_id) REFERENCES slides_heads(workspace_id,page_id) ON DELETE CASCADE,
 FOREIGN KEY(workspace_id,page_id,base_rev) REFERENCES slides_revisions(workspace_id,page_id,rev) ON DELETE NO ACTION DEFERRABLE INITIALLY DEFERRED,
 FOREIGN KEY(workspace_id,page_id,result_revision) REFERENCES slides_revisions(workspace_id,page_id,rev) ON DELETE NO ACTION DEFERRABLE INITIALLY DEFERRED
);
CREATE INDEX idx_slides_requests_page ON slides_requests(page_id,workspace_id);
CREATE INDEX idx_slides_requests_base ON slides_requests(base_rev,workspace_id,page_id);
CREATE INDEX idx_slides_requests_result ON slides_requests(result_revision,workspace_id,page_id);
CREATE INDEX idx_slides_requests_human ON slides_requests(human_user_id);
CREATE INDEX idx_slides_requests_chat ON slides_requests(chat_id);
CREATE INDEX idx_slides_requests_agent ON slides_requests(agent_id);
CREATE INDEX idx_slides_requests_dispatch ON slides_requests(status,created_at);
CREATE TABLE slides_request_events (
 workspace_id TEXT NOT NULL,page_id TEXT NOT NULL,request_id TEXT NOT NULL,version INTEGER NOT NULL CHECK(version>=1),
 kind TEXT NOT NULL CHECK(kind IN ('queued','dispatched','bound','result','succeeded','failed','cancelled','interrupted')),
 actor TEXT NOT NULL,detail TEXT NOT NULL DEFAULT '',mutation_id TEXT,
 created_at TEXT NOT NULL DEFAULT(strftime('%Y-%m-%dT%H:%M:%fZ','now')),
 PRIMARY KEY(workspace_id,page_id,request_id,version),UNIQUE(workspace_id,page_id,request_id,kind),UNIQUE(workspace_id,page_id,mutation_id),
 FOREIGN KEY(workspace_id,page_id,request_id) REFERENCES slides_requests(workspace_id,page_id,id) ON DELETE CASCADE
);
CREATE TABLE slides_request_receipts (
 workspace_id TEXT NOT NULL,page_id TEXT NOT NULL,mutation_id TEXT NOT NULL,
 request_id TEXT NOT NULL,event_version INTEGER NOT NULL,actor TEXT NOT NULL,request_hash TEXT NOT NULL,
 PRIMARY KEY(workspace_id,page_id,request_id,event_version),UNIQUE(workspace_id,page_id,mutation_id),
 FOREIGN KEY(workspace_id,page_id,request_id,event_version) REFERENCES slides_request_events(workspace_id,page_id,request_id,version) ON DELETE CASCADE
);
ALTER TABLE page_artifact_run_delegations ADD COLUMN slides_request_id TEXT REFERENCES slides_requests(id) ON DELETE CASCADE;
CREATE INDEX idx_slides_request_run_origin ON page_artifact_run_delegations(slides_request_id);
-- Retained origin tombstone intentionally has no request FK: deleting a Page
-- must never turn its old selected run into a generic content capability.
ALTER TABLE artifact_run_principals ADD COLUMN slides_request_origin TEXT;
CREATE TRIGGER slides_principal_origin_immutable BEFORE UPDATE OF slides_request_origin ON artifact_run_principals
WHEN OLD.slides_request_origin IS NOT NULL AND NEW.slides_request_origin IS NOT OLD.slides_request_origin
BEGIN SELECT RAISE(ABORT,'Slides principal origin is immutable'); END;
CREATE TRIGGER slides_request_run_origin_immutable BEFORE UPDATE OF slides_request_id ON page_artifact_run_delegations
WHEN OLD.slides_request_id IS NOT NULL AND NEW.slides_request_id IS NOT OLD.slides_request_id
BEGIN SELECT RAISE(ABORT,'Slides request run origin is immutable'); END;
CREATE TRIGGER slides_request_origin_exclusive_insert BEFORE INSERT ON page_artifact_run_delegations
WHEN NEW.slides_request_id IS NOT NULL AND (NEW.request_id IS NOT NULL OR NEW.document_request_id IS NOT NULL)
BEGIN SELECT RAISE(ABORT,'selected run origin is mutually exclusive'); END;
CREATE TRIGGER slides_request_origin_exclusive_update BEFORE UPDATE OF slides_request_id,request_id,document_request_id ON page_artifact_run_delegations
WHEN NEW.slides_request_id IS NOT NULL AND (NEW.request_id IS NOT NULL OR NEW.document_request_id IS NOT NULL)
BEGIN SELECT RAISE(ABORT,'selected run origin is mutually exclusive'); END;
CREATE TRIGGER slides_request_original_immutable BEFORE UPDATE OF id,workspace_id,page_id,request_seq,protocol_version,origin_request_id,human_actor,agent_actor,base_rev,base_schema_version,base_content_hash,input_json,verified_json,submission_id,fingerprint,created_at ON slides_requests
WHEN NEW.id IS NOT OLD.id OR NEW.workspace_id IS NOT OLD.workspace_id OR NEW.page_id IS NOT OLD.page_id OR NEW.request_seq IS NOT OLD.request_seq OR NEW.protocol_version IS NOT OLD.protocol_version OR NEW.origin_request_id IS NOT OLD.origin_request_id OR NEW.human_actor IS NOT OLD.human_actor OR NEW.agent_actor IS NOT OLD.agent_actor OR NEW.base_rev IS NOT OLD.base_rev OR NEW.base_schema_version IS NOT OLD.base_schema_version OR NEW.base_content_hash IS NOT OLD.base_content_hash OR NEW.input_json IS NOT OLD.input_json OR NEW.verified_json IS NOT OLD.verified_json OR NEW.submission_id IS NOT OLD.submission_id OR NEW.fingerprint IS NOT OLD.fingerprint OR NEW.created_at IS NOT OLD.created_at
BEGIN SELECT RAISE(ABORT,'Slides original selected request is immutable'); END;
CREATE TRIGGER slides_request_terminal_immutable BEFORE UPDATE OF status ON slides_requests
WHEN (OLD.status IN ('succeeded','failed','cancelled','interrupted') AND NEW.status IS NOT OLD.status) OR (OLD.status='running' AND NEW.status='queued') OR (OLD.status='queued' AND NEW.status NOT IN ('queued','running','failed','cancelled','interrupted'))
BEGIN SELECT RAISE(ABORT,'Slides terminal request cannot restart'); END;
CREATE TRIGGER slides_request_result_immutable BEFORE UPDATE OF result_revision,result_hash ON slides_requests
WHEN OLD.result_revision IS NOT NULL AND (NEW.result_revision IS NOT OLD.result_revision OR NEW.result_hash IS NOT OLD.result_hash)
BEGIN SELECT RAISE(ABORT,'Slides request result is immutable'); END;
CREATE TRIGGER slides_request_events_immutable BEFORE UPDATE ON slides_request_events
BEGIN SELECT RAISE(ABORT,'Slides request history is immutable'); END;
CREATE TRIGGER slides_request_events_retained BEFORE DELETE ON slides_request_events
WHEN EXISTS(SELECT 1 FROM slides_requests WHERE workspace_id=OLD.workspace_id AND page_id=OLD.page_id AND id=OLD.request_id)
BEGIN SELECT RAISE(ABORT,'Slides request history is retained with its Page'); END;
CREATE TRIGGER slides_request_history_retained BEFORE DELETE ON slides_requests
WHEN EXISTS(SELECT 1 FROM slides_heads WHERE workspace_id=OLD.workspace_id AND page_id=OLD.page_id)
BEGIN SELECT RAISE(ABORT,'Slides request is retained with its Page'); END;
CREATE TRIGGER slides_request_live_identity_no_rebind BEFORE UPDATE OF human_user_id,chat_id,agent_id ON slides_requests
WHEN (NEW.human_user_id IS NOT OLD.human_user_id AND NEW.human_user_id IS NOT NULL) OR (NEW.chat_id IS NOT OLD.chat_id AND NEW.chat_id IS NOT NULL) OR (NEW.agent_id IS NOT OLD.agent_id AND NEW.agent_id IS NOT NULL)
BEGIN SELECT RAISE(ABORT,'Slides request dispatch identity cannot change'); END;
CREATE TRIGGER slides_request_original_run_immutable BEFORE UPDATE OF original_run_id ON slides_requests
WHEN OLD.original_run_id IS NOT NULL AND NEW.original_run_id IS NOT OLD.original_run_id
BEGIN SELECT RAISE(ABORT,'Slides original run is immutable'); END;
CREATE TRIGGER slides_request_run_no_rebind BEFORE UPDATE OF run_id ON slides_requests
WHEN (OLD.run_id IS NOT NULL AND NEW.run_id IS NOT OLD.run_id AND NOT(NEW.run_id IS NULL AND NEW.status IN ('succeeded','failed','cancelled','interrupted'))) OR (NEW.run_id IS NOT NULL AND OLD.original_run_id IS NOT NULL AND NEW.run_id IS NOT OLD.original_run_id)
BEGIN SELECT RAISE(ABORT,'Slides request run cannot change'); END;
CREATE TRIGGER slides_request_authority_immutable BEFORE UPDATE OF audience_json,credential_kind,credential_id,credential_hash ON slides_requests
WHEN (NEW.audience_json IS NOT OLD.audience_json OR NEW.credential_kind IS NOT OLD.credential_kind OR NEW.credential_id IS NOT OLD.credential_id OR NEW.credential_hash IS NOT OLD.credential_hash) AND NOT(NEW.status IN ('succeeded','failed','cancelled','interrupted') AND NEW.audience_json='{}' AND NEW.credential_kind=OLD.credential_kind AND NEW.credential_id='' AND NEW.credential_hash='')
BEGIN SELECT RAISE(ABORT,'Slides original admission cannot refresh'); END;
CREATE TRIGGER slides_request_restored_inert BEFORE UPDATE OF restored ON slides_requests
WHEN OLD.restored=1 AND NEW.restored IS NOT OLD.restored
BEGIN SELECT RAISE(ABORT,'restored Slides request cannot regain authority'); END;
CREATE TRIGGER slides_request_origin_page_insert BEFORE INSERT ON page_artifact_run_delegations
WHEN NEW.slides_request_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM slides_requests WHERE id=NEW.slides_request_id AND page_id=NEW.page_id)
BEGIN SELECT RAISE(ABORT,'Slides request origin Page mismatch'); END;
CREATE TRIGGER slides_request_origin_page_update BEFORE UPDATE OF slides_request_id,page_id ON page_artifact_run_delegations
WHEN NEW.slides_request_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM slides_requests WHERE id=NEW.slides_request_id AND page_id=NEW.page_id)
BEGIN SELECT RAISE(ABORT,'Slides request origin Page mismatch'); END;

CREATE TRIGGER slides_request_human_user_id_deleted BEFORE DELETE ON users
BEGIN
 INSERT INTO slides_request_events(workspace_id,page_id,request_id,version,kind,actor,detail)
 SELECT workspace_id,page_id,id,version+1,'failed','system:identity-deleted','Dispatch identity deleted; retained history is inert'
 FROM slides_requests WHERE human_user_id=OLD.id AND status IN ('queued','running');
 UPDATE slides_requests SET
 version=CASE WHEN status IN ('queued','running') THEN version+1 ELSE version END,
 error=CASE WHEN status IN ('queued','running') THEN 'Dispatch identity deleted; retained history is inert' ELSE error END,
 status=CASE WHEN status IN ('queued','running') THEN 'failed' ELSE status END,
 run_id=NULL,audience_json='{}',credential_id='',credential_hash='',updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
 WHERE human_user_id=OLD.id;
END;

CREATE TRIGGER slides_request_chat_id_deleted BEFORE DELETE ON chats
BEGIN
 INSERT INTO slides_request_events(workspace_id,page_id,request_id,version,kind,actor,detail)
 SELECT workspace_id,page_id,id,version+1,'failed','system:identity-deleted','Dispatch identity deleted; retained history is inert'
 FROM slides_requests WHERE chat_id=OLD.id AND status IN ('queued','running');
 UPDATE slides_requests SET
 version=CASE WHEN status IN ('queued','running') THEN version+1 ELSE version END,
 error=CASE WHEN status IN ('queued','running') THEN 'Dispatch identity deleted; retained history is inert' ELSE error END,
 status=CASE WHEN status IN ('queued','running') THEN 'failed' ELSE status END,
 run_id=NULL,audience_json='{}',credential_id='',credential_hash='',updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
 WHERE chat_id=OLD.id;
END;

CREATE TRIGGER slides_request_agent_id_deleted BEFORE DELETE ON agents
BEGIN
 INSERT INTO slides_request_events(workspace_id,page_id,request_id,version,kind,actor,detail)
 SELECT workspace_id,page_id,id,version+1,'failed','system:identity-deleted','Dispatch identity deleted; retained history is inert'
 FROM slides_requests WHERE agent_id=OLD.id AND status IN ('queued','running');
 UPDATE slides_requests SET
 version=CASE WHEN status IN ('queued','running') THEN version+1 ELSE version END,
 error=CASE WHEN status IN ('queued','running') THEN 'Dispatch identity deleted; retained history is inert' ELSE error END,
 status=CASE WHEN status IN ('queued','running') THEN 'failed' ELSE status END,
 run_id=NULL,audience_json='{}',credential_id='',credential_hash='',updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
 WHERE agent_id=OLD.id;
END;
