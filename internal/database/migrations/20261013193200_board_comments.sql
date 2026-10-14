-- Human Board discussions retain immutable server-captured anchors independently of scene changes.
CREATE TABLE page_board_threads (
 id TEXT PRIMARY KEY,
 page_id TEXT NOT NULL REFERENCES pages(id) ON DELETE CASCADE,
 anchor_json TEXT NOT NULL,
 status TEXT NOT NULL DEFAULT 'open' CHECK(status IN ('open','resolved')),
 version INTEGER NOT NULL DEFAULT 1 CHECK(version > 0),
 created_by_user_id TEXT REFERENCES users(id) ON DELETE SET NULL,
 resolved_by_user_id TEXT REFERENCES users(id) ON DELETE SET NULL,
 resolved_at TEXT,
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL
);
CREATE INDEX idx_page_board_threads_author ON page_board_threads(created_by_user_id);
CREATE INDEX idx_page_board_threads_resolver ON page_board_threads(resolved_by_user_id);
CREATE TABLE page_board_comments (
 id TEXT PRIMARY KEY,
 thread_id TEXT NOT NULL REFERENCES page_board_threads(id) ON DELETE CASCADE,
 thread_version INTEGER NOT NULL CHECK(thread_version > 0),
 body TEXT NOT NULL CHECK(length(body)>0),
 author_user_id TEXT REFERENCES users(id) ON DELETE SET NULL,
 created_at TEXT NOT NULL,
 UNIQUE(thread_id,thread_version)
);
CREATE INDEX idx_page_board_comments_author ON page_board_comments(author_user_id);
-- These receipts are also the durable mutation audit: identity, operation and
-- result version, without copying private comment bodies into workspace events.
CREATE TABLE page_board_comment_mutations (
 id TEXT PRIMARY KEY,
 page_id TEXT NOT NULL REFERENCES pages(id) ON DELETE CASCADE,
 thread_id TEXT NOT NULL REFERENCES page_board_threads(id) ON DELETE CASCADE,
 comment_id TEXT REFERENCES page_board_comments(id) ON DELETE SET NULL,
 mutation_id TEXT NOT NULL,
 request_digest TEXT NOT NULL,
 actor_user_id TEXT REFERENCES users(id) ON DELETE SET NULL,
 operation TEXT NOT NULL CHECK(operation IN ('create','reply','resolve','reopen')),
 result_version INTEGER NOT NULL,
 created_at TEXT NOT NULL,
 UNIQUE(page_id,actor_user_id,mutation_id)
);
CREATE INDEX idx_page_board_comment_mutations_thread ON page_board_comment_mutations(thread_id);
CREATE INDEX idx_page_board_comment_mutations_comment ON page_board_comment_mutations(comment_id);
CREATE INDEX idx_page_board_comment_mutations_actor ON page_board_comment_mutations(actor_user_id);

CREATE INDEX idx_page_board_threads_page ON page_board_threads(page_id);
