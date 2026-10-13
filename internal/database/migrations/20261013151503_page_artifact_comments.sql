-- Private human discussions stay bound to the exact immutable source revision.
CREATE TABLE page_artifact_threads (
 id TEXT PRIMARY KEY,
 page_id TEXT NOT NULL REFERENCES pages(id) ON DELETE CASCADE,
 revision INTEGER NOT NULL,
 anchor_json TEXT NOT NULL,
 status TEXT NOT NULL DEFAULT 'open' CHECK(status IN ('open','resolved')),
 version INTEGER NOT NULL DEFAULT 1 CHECK(version > 0),
 created_by_user_id TEXT REFERENCES users(id) ON DELETE SET NULL,
 resolved_by_user_id TEXT REFERENCES users(id) ON DELETE SET NULL,
 resolved_at TEXT,
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 FOREIGN KEY(page_id,revision) REFERENCES page_artifact_revisions(page_id,revision) ON DELETE CASCADE
);
CREATE INDEX idx_page_artifact_threads_revision ON page_artifact_threads(page_id,revision);
CREATE INDEX idx_page_artifact_threads_revision_number ON page_artifact_threads(revision,page_id);
CREATE INDEX idx_page_artifact_threads_author ON page_artifact_threads(created_by_user_id);
CREATE INDEX idx_page_artifact_threads_resolver ON page_artifact_threads(resolved_by_user_id);
CREATE TABLE page_artifact_comments (
 id TEXT PRIMARY KEY,
 thread_id TEXT NOT NULL REFERENCES page_artifact_threads(id) ON DELETE CASCADE,
 thread_version INTEGER NOT NULL CHECK(thread_version > 0),
 body TEXT NOT NULL CHECK(length(body)>0),
 author_user_id TEXT REFERENCES users(id) ON DELETE SET NULL,
 created_at TEXT NOT NULL,
 UNIQUE(thread_id,thread_version)
);
CREATE INDEX idx_page_artifact_comments_author ON page_artifact_comments(author_user_id);
-- These receipts are also the durable mutation audit: identity, operation and
-- result version, without copying private comment bodies into workspace events.
CREATE TABLE page_artifact_comment_mutations (
 id TEXT PRIMARY KEY,
 page_id TEXT NOT NULL REFERENCES pages(id) ON DELETE CASCADE,
 thread_id TEXT NOT NULL REFERENCES page_artifact_threads(id) ON DELETE CASCADE,
 comment_id TEXT REFERENCES page_artifact_comments(id) ON DELETE SET NULL,
 mutation_id TEXT NOT NULL,
 request_digest TEXT NOT NULL,
 actor_user_id TEXT REFERENCES users(id) ON DELETE SET NULL,
 operation TEXT NOT NULL CHECK(operation IN ('create','reply','resolve','reopen')),
 result_version INTEGER NOT NULL,
 created_at TEXT NOT NULL,
 UNIQUE(page_id,mutation_id)
);
CREATE INDEX idx_page_artifact_comment_mutations_thread ON page_artifact_comment_mutations(thread_id);
CREATE INDEX idx_page_artifact_comment_mutations_comment ON page_artifact_comment_mutations(comment_id);
CREATE INDEX idx_page_artifact_comment_mutations_actor ON page_artifact_comment_mutations(actor_user_id);
