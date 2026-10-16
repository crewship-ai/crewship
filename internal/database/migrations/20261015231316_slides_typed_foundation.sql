CREATE TABLE slides_writer_fence (id INTEGER PRIMARY KEY CHECK(id=1),seq INTEGER NOT NULL);
INSERT INTO slides_writer_fence VALUES(1,0);
CREATE TABLE slides_heads (
 workspace_id TEXT NOT NULL, page_id TEXT NOT NULL,
 schema_version INTEGER NOT NULL, head_rev INTEGER NOT NULL CHECK(head_rev>=1),
 order_version INTEGER NOT NULL CHECK(order_version>=1),
 canonical_json BLOB NOT NULL, content_hash TEXT NOT NULL,
 PRIMARY KEY(workspace_id,page_id),
 FOREIGN KEY(workspace_id,page_id) REFERENCES pages(workspace_id,id) ON DELETE CASCADE
);
CREATE UNIQUE INDEX idx_slides_heads_page ON slides_heads(page_id);
CREATE TABLE slides_revisions (
 workspace_id TEXT NOT NULL, page_id TEXT NOT NULL, rev INTEGER NOT NULL CHECK(rev>=1),
 parent_rev INTEGER NOT NULL CHECK(parent_rev>=0), order_version INTEGER NOT NULL CHECK(order_version>=1),
 schema_version INTEGER NOT NULL, canonical_json BLOB NOT NULL, content_hash TEXT NOT NULL,
 actor TEXT NOT NULL, kind TEXT NOT NULL, mutation_id TEXT NOT NULL,
 created_at TEXT NOT NULL DEFAULT(strftime('%Y-%m-%dT%H:%M:%fZ','now')),
 PRIMARY KEY(workspace_id,page_id,rev), UNIQUE(workspace_id,page_id,mutation_id),
 FOREIGN KEY(workspace_id,page_id) REFERENCES slides_heads(workspace_id,page_id) ON DELETE CASCADE
);
CREATE TABLE slides_receipts (
 workspace_id TEXT NOT NULL, page_id TEXT NOT NULL, mutation_id TEXT NOT NULL,
 request_hash TEXT NOT NULL, actor TEXT NOT NULL, rev INTEGER NOT NULL,
 order_version INTEGER NOT NULL, kind TEXT NOT NULL, content_hash TEXT NOT NULL,
 PRIMARY KEY(workspace_id,page_id,mutation_id),
 FOREIGN KEY(workspace_id,page_id,rev) REFERENCES slides_revisions(workspace_id,page_id,rev) ON DELETE CASCADE
);
CREATE INDEX idx_slides_receipts_revision ON slides_receipts(rev,workspace_id,page_id);
CREATE TABLE slides_object_ids (
 workspace_id TEXT NOT NULL, page_id TEXT NOT NULL, object_id TEXT NOT NULL,
 object_kind TEXT NOT NULL CHECK(object_kind IN ('slide','element')),
 PRIMARY KEY(workspace_id,page_id,object_id),
 FOREIGN KEY(workspace_id,page_id) REFERENCES slides_heads(workspace_id,page_id) ON DELETE CASCADE
);

CREATE TRIGGER slides_exclusive_type_insert BEFORE INSERT ON slides_heads
WHEN EXISTS(SELECT 1 FROM page_artifacts WHERE page_id=NEW.page_id) OR EXISTS(SELECT 1 FROM page_boards WHERE page_id=NEW.page_id) OR EXISTS(SELECT 1 FROM documents WHERE page_id=NEW.page_id) OR EXISTS(SELECT 1 FROM page_project_drafts WHERE page_id=NEW.page_id) OR EXISTS(SELECT 1 FROM page_project_live WHERE page_id=NEW.page_id) OR EXISTS(SELECT 1 FROM page_panels WHERE page_id=NEW.page_id)
BEGIN SELECT RAISE(ABORT,'Slides require an empty Page'); END;

CREATE TRIGGER slides_exclusive_type_update BEFORE UPDATE OF page_id,workspace_id ON slides_heads
WHEN EXISTS(SELECT 1 FROM page_artifacts WHERE page_id=NEW.page_id) OR EXISTS(SELECT 1 FROM page_boards WHERE page_id=NEW.page_id) OR EXISTS(SELECT 1 FROM documents WHERE page_id=NEW.page_id) OR EXISTS(SELECT 1 FROM page_project_drafts WHERE page_id=NEW.page_id) OR EXISTS(SELECT 1 FROM page_project_live WHERE page_id=NEW.page_id) OR EXISTS(SELECT 1 FROM page_panels WHERE page_id=NEW.page_id)
BEGIN SELECT RAISE(ABORT,'Slides require an empty Page'); END;

CREATE TRIGGER page_artifacts_exclude_slides_insert BEFORE INSERT ON page_artifacts
WHEN EXISTS(SELECT 1 FROM slides_heads WHERE page_id=NEW.page_id)
BEGIN SELECT RAISE(ABORT,'Page already contains Slides'); END;

CREATE TRIGGER page_artifacts_exclude_slides_update BEFORE UPDATE OF page_id ON page_artifacts
WHEN EXISTS(SELECT 1 FROM slides_heads WHERE page_id=NEW.page_id)
BEGIN SELECT RAISE(ABORT,'Page already contains Slides'); END;

CREATE TRIGGER page_boards_exclude_slides_insert BEFORE INSERT ON page_boards
WHEN EXISTS(SELECT 1 FROM slides_heads WHERE page_id=NEW.page_id)
BEGIN SELECT RAISE(ABORT,'Page already contains Slides'); END;

CREATE TRIGGER page_boards_exclude_slides_update BEFORE UPDATE OF page_id ON page_boards
WHEN EXISTS(SELECT 1 FROM slides_heads WHERE page_id=NEW.page_id)
BEGIN SELECT RAISE(ABORT,'Page already contains Slides'); END;

CREATE TRIGGER documents_exclude_slides_insert BEFORE INSERT ON documents
WHEN EXISTS(SELECT 1 FROM slides_heads WHERE page_id=NEW.page_id)
BEGIN SELECT RAISE(ABORT,'Page already contains Slides'); END;

CREATE TRIGGER documents_exclude_slides_update BEFORE UPDATE OF page_id ON documents
WHEN EXISTS(SELECT 1 FROM slides_heads WHERE page_id=NEW.page_id)
BEGIN SELECT RAISE(ABORT,'Page already contains Slides'); END;

CREATE TRIGGER page_project_drafts_exclude_slides_insert BEFORE INSERT ON page_project_drafts
WHEN EXISTS(SELECT 1 FROM slides_heads WHERE page_id=NEW.page_id)
BEGIN SELECT RAISE(ABORT,'Page already contains Slides'); END;

CREATE TRIGGER page_project_drafts_exclude_slides_update BEFORE UPDATE OF page_id ON page_project_drafts
WHEN EXISTS(SELECT 1 FROM slides_heads WHERE page_id=NEW.page_id)
BEGIN SELECT RAISE(ABORT,'Page already contains Slides'); END;

CREATE TRIGGER page_project_live_exclude_slides_insert BEFORE INSERT ON page_project_live
WHEN EXISTS(SELECT 1 FROM slides_heads WHERE page_id=NEW.page_id)
BEGIN SELECT RAISE(ABORT,'Page already contains Slides'); END;

CREATE TRIGGER page_project_live_exclude_slides_update BEFORE UPDATE OF page_id ON page_project_live
WHEN EXISTS(SELECT 1 FROM slides_heads WHERE page_id=NEW.page_id)
BEGIN SELECT RAISE(ABORT,'Page already contains Slides'); END;

CREATE TRIGGER page_panels_exclude_slides_insert BEFORE INSERT ON page_panels
WHEN EXISTS(SELECT 1 FROM slides_heads WHERE page_id=NEW.page_id)
BEGIN SELECT RAISE(ABORT,'Page already contains Slides'); END;

CREATE TRIGGER page_panels_exclude_slides_update BEFORE UPDATE OF page_id ON page_panels
WHEN EXISTS(SELECT 1 FROM slides_heads WHERE page_id=NEW.page_id)
BEGIN SELECT RAISE(ABORT,'Page already contains Slides'); END;

CREATE TRIGGER slides_require_human_owner_insert BEFORE INSERT ON slides_heads
WHEN NOT EXISTS(SELECT 1 FROM pages WHERE id=NEW.page_id AND workspace_id=NEW.workspace_id AND owner_user_id IS NOT NULL AND owner_crew_id IS NULL)
BEGIN SELECT RAISE(ABORT,'Slides require a human-owned Page'); END;
CREATE TRIGGER slides_require_human_owner_update BEFORE UPDATE OF page_id,workspace_id ON slides_heads
WHEN NOT EXISTS(SELECT 1 FROM pages WHERE id=NEW.page_id AND workspace_id=NEW.workspace_id AND owner_user_id IS NOT NULL AND owner_crew_id IS NULL)
BEGIN SELECT RAISE(ABORT,'Slides require a human-owned Page'); END;
CREATE TRIGGER pages_preserve_slides_human_owner BEFORE UPDATE OF owner_user_id,owner_crew_id ON pages
WHEN EXISTS(SELECT 1 FROM slides_heads WHERE page_id=OLD.id) AND (NEW.owner_user_id IS NULL OR NEW.owner_crew_id IS NOT NULL)
BEGIN SELECT RAISE(ABORT,'Slides retain a human owner'); END;
