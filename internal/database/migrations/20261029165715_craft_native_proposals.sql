-- An agent's native Craft change prepared while a human holds the edit lease.
-- It never changes the document: a human accepts it as an ordinary fenced save
-- over the exact recorded base version, or rejects it. Decisions are final.
CREATE TABLE craft_native_proposals (
 id TEXT PRIMARY KEY,
 page_id TEXT NOT NULL REFERENCES craft_native_documents(page_id) ON DELETE CASCADE,
 workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 application_id TEXT NOT NULL CHECK(application_id IN ('wordcraft','gridcraft')),
 engine_pin TEXT NOT NULL CHECK(length(engine_pin)=64 AND engine_pin NOT GLOB '*[^0-9a-f]*'),
 author_actor_id TEXT NOT NULL CHECK(author_actor_id LIKE 'agent:%'),
 request_id TEXT NOT NULL CHECK(length(request_id) BETWEEN 1 AND 128),
 base_version INTEGER NOT NULL CHECK(base_version>=1),
 summary TEXT NOT NULL DEFAULT '' CHECK(length(summary)<=500),
 content_hash TEXT NOT NULL CHECK(length(content_hash)=71 AND content_hash LIKE 'sha256:%'),
 content BLOB NOT NULL CHECK(length(content) BETWEEN 1 AND 33554432),
 state TEXT NOT NULL DEFAULT 'open' CHECK(state IN ('open','accepted','rejected','stale')),
 decided_by TEXT,
 result_version INTEGER,
 created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
 decided_at TEXT,
 UNIQUE(page_id, author_actor_id, request_id),
 CHECK((state='open') = (decided_by IS NULL AND decided_at IS NULL)),
 CHECK((state='accepted') = (result_version IS NOT NULL))
);
CREATE INDEX craft_native_proposals_page ON craft_native_proposals(page_id, state, created_at);
CREATE INDEX craft_native_proposals_workspace ON craft_native_proposals(workspace_id);
CREATE TRIGGER craft_native_proposal_decided_immutable BEFORE UPDATE ON craft_native_proposals
WHEN OLD.state <> 'open'
BEGIN SELECT RAISE(ABORT,'Native Craft proposal decision is final'); END;
CREATE TRIGGER craft_native_proposal_content_immutable BEFORE UPDATE ON craft_native_proposals
WHEN NEW.id IS NOT OLD.id OR NEW.page_id IS NOT OLD.page_id OR NEW.workspace_id IS NOT OLD.workspace_id OR NEW.application_id IS NOT OLD.application_id OR NEW.engine_pin IS NOT OLD.engine_pin OR NEW.author_actor_id IS NOT OLD.author_actor_id OR NEW.request_id IS NOT OLD.request_id OR NEW.base_version IS NOT OLD.base_version OR NEW.summary IS NOT OLD.summary OR NEW.content_hash IS NOT OLD.content_hash OR NEW.content IS NOT OLD.content OR NEW.created_at IS NOT OLD.created_at
BEGIN SELECT RAISE(ABORT,'Native Craft proposal content is immutable'); END;
