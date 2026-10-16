-- Anchors are stable namespaced object identities, not live-element foreign
-- keys: deletion detaches the projection without destroying the conversation.
CREATE TABLE slides_comment_threads (
 workspace_id TEXT NOT NULL,page_id TEXT NOT NULL,id TEXT NOT NULL,
 thread_seq INTEGER NOT NULL CHECK(thread_seq>=1),
 anchor_schema_version INTEGER NOT NULL CHECK(anchor_schema_version=1),
 anchor_kind TEXT NOT NULL CHECK(anchor_kind IN ('slide','element')),
 slide_id TEXT NOT NULL,element_id TEXT,
 anchor_revision INTEGER NOT NULL,
 version INTEGER NOT NULL CHECK(version>=1),resolved INTEGER NOT NULL CHECK(resolved IN (0,1)),
 comment_count INTEGER NOT NULL CHECK(comment_count>=1),
 status TEXT NOT NULL CHECK(status IN ('attached','detached','restored')),
 projection_revision INTEGER NOT NULL,
 last_missing_revision INTEGER,restored_at_revision INTEGER,
 created_at TEXT NOT NULL DEFAULT(strftime('%Y-%m-%dT%H:%M:%fZ','now')),
 updated_at TEXT NOT NULL DEFAULT(strftime('%Y-%m-%dT%H:%M:%fZ','now')),
 PRIMARY KEY(workspace_id,page_id,id),UNIQUE(workspace_id,page_id,thread_seq),
 CHECK((anchor_kind='slide' AND element_id IS NULL) OR (anchor_kind='element' AND element_id IS NOT NULL)),
 FOREIGN KEY(workspace_id,page_id) REFERENCES slides_heads(workspace_id,page_id) ON DELETE CASCADE,
 FOREIGN KEY(workspace_id,page_id,anchor_revision) REFERENCES slides_revisions(workspace_id,page_id,rev) ON DELETE NO ACTION DEFERRABLE INITIALLY DEFERRED
);
CREATE INDEX idx_slides_comment_anchor_revision ON slides_comment_threads(anchor_revision,workspace_id,page_id);
CREATE TABLE slides_comment_events (
 workspace_id TEXT NOT NULL,page_id TEXT NOT NULL,thread_id TEXT NOT NULL,
 version INTEGER NOT NULL CHECK(version>=1),
 kind TEXT NOT NULL CHECK(kind IN ('comment','reply','resolve','reopen')),
 body TEXT NOT NULL,actor TEXT NOT NULL,mutation_id TEXT NOT NULL,
 created_at TEXT NOT NULL DEFAULT(strftime('%Y-%m-%dT%H:%M:%fZ','now')),
 PRIMARY KEY(workspace_id,page_id,thread_id,version),UNIQUE(workspace_id,page_id,mutation_id),
 FOREIGN KEY(workspace_id,page_id,thread_id) REFERENCES slides_comment_threads(workspace_id,page_id,id) ON DELETE CASCADE
);
CREATE TABLE slides_comment_receipts (
 workspace_id TEXT NOT NULL,page_id TEXT NOT NULL,thread_id TEXT NOT NULL,event_version INTEGER NOT NULL,
 mutation_id TEXT NOT NULL,request_hash TEXT NOT NULL,actor TEXT NOT NULL,kind TEXT NOT NULL,
 PRIMARY KEY(workspace_id,page_id,thread_id,event_version),UNIQUE(workspace_id,page_id,mutation_id),
 FOREIGN KEY(workspace_id,page_id,thread_id,event_version) REFERENCES slides_comment_events(workspace_id,page_id,thread_id,version) ON DELETE CASCADE
);

-- Immutable original anchors and human event history are retained while the
-- Page exists. Page/head deletion still cascades through its owned children.
CREATE TRIGGER slides_comment_anchor_immutable BEFORE UPDATE OF workspace_id,page_id,id,thread_seq,anchor_schema_version,anchor_kind,slide_id,element_id,anchor_revision,created_at ON slides_comment_threads
WHEN NEW.workspace_id IS NOT OLD.workspace_id OR NEW.page_id IS NOT OLD.page_id OR NEW.id IS NOT OLD.id OR NEW.thread_seq IS NOT OLD.thread_seq OR NEW.anchor_schema_version IS NOT OLD.anchor_schema_version OR NEW.anchor_kind IS NOT OLD.anchor_kind OR NEW.slide_id IS NOT OLD.slide_id OR NEW.element_id IS NOT OLD.element_id OR NEW.anchor_revision IS NOT OLD.anchor_revision OR NEW.created_at IS NOT OLD.created_at
BEGIN SELECT RAISE(ABORT,'Slides original comment anchor is immutable'); END;
CREATE TRIGGER slides_comment_events_no_update BEFORE UPDATE ON slides_comment_events
BEGIN SELECT RAISE(ABORT,'Slides comment history is immutable'); END;
CREATE TRIGGER slides_comment_events_no_delete BEFORE DELETE ON slides_comment_events
WHEN EXISTS(SELECT 1 FROM slides_comment_threads WHERE workspace_id=OLD.workspace_id AND page_id=OLD.page_id AND id=OLD.thread_id)
BEGIN SELECT RAISE(ABORT,'Slides comment history is retained with its Page'); END;
CREATE TRIGGER slides_comment_threads_no_delete BEFORE DELETE ON slides_comment_threads
WHEN EXISTS(SELECT 1 FROM slides_heads WHERE workspace_id=OLD.workspace_id AND page_id=OLD.page_id)
BEGIN SELECT RAISE(ABORT,'Slides comment threads are retained with their Page'); END;
