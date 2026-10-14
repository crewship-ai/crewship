ALTER TABLE page_board_comments ADD COLUMN version INTEGER NOT NULL DEFAULT 1 CHECK(version>0);
ALTER TABLE page_board_comments ADD COLUMN edited_at TEXT;
ALTER TABLE page_board_comments ADD COLUMN deleted_at TEXT;
ALTER TABLE page_board_comments ADD COLUMN edited_by_user_id TEXT REFERENCES users(id) ON DELETE SET NULL;
ALTER TABLE page_board_comments ADD COLUMN deleted_by_user_id TEXT REFERENCES users(id) ON DELETE SET NULL;
CREATE INDEX idx_board_comment_editor ON page_board_comments(edited_by_user_id);
CREATE INDEX idx_board_comment_deleter ON page_board_comments(deleted_by_user_id);
CREATE TABLE page_board_comment_history (
 id TEXT NOT NULL PRIMARY KEY,
 page_id TEXT NOT NULL REFERENCES pages(id) ON DELETE CASCADE,
 thread_id TEXT NOT NULL REFERENCES page_board_threads(id) ON DELETE CASCADE,
 comment_id TEXT NOT NULL REFERENCES page_board_comments(id) ON DELETE CASCADE,
 version INTEGER NOT NULL CHECK(version>0),
 body TEXT NOT NULL,
 action TEXT NOT NULL CHECK(action IN ('create','edit','delete')),
 actor_user_id TEXT REFERENCES users(id) ON DELETE SET NULL,
 created_at TEXT NOT NULL,
 UNIQUE(comment_id,version)
);
CREATE INDEX idx_board_comment_history_page ON page_board_comment_history(page_id);
CREATE INDEX idx_board_comment_history_thread ON page_board_comment_history(thread_id);
CREATE INDEX idx_board_comment_history_actor ON page_board_comment_history(actor_user_id);
INSERT INTO page_board_comment_history(id,page_id,thread_id,comment_id,version,body,action,actor_user_id,created_at)
 SELECT 'initial-'||c.id,t.page_id,t.id,c.id,1,c.body,'create',c.author_user_id,c.created_at FROM page_board_comments c JOIN page_board_threads t ON t.id=c.thread_id;
CREATE TRIGGER board_comment_history_immutable BEFORE UPDATE ON page_board_comment_history
WHEN NEW.id IS NOT OLD.id OR NEW.page_id IS NOT OLD.page_id OR NEW.thread_id IS NOT OLD.thread_id OR NEW.comment_id IS NOT OLD.comment_id OR NEW.version IS NOT OLD.version OR NEW.body IS NOT OLD.body OR NEW.action IS NOT OLD.action OR NEW.created_at IS NOT OLD.created_at
 OR (NEW.actor_user_id IS NOT OLD.actor_user_id AND NOT(NEW.actor_user_id IS NULL AND NOT EXISTS(SELECT 1 FROM users WHERE id=OLD.actor_user_id)))
BEGIN SELECT RAISE(ABORT,'immutable board comment history'); END;
CREATE TRIGGER board_comment_history_source BEFORE INSERT ON page_board_comment_history
WHEN NOT EXISTS(SELECT 1 FROM page_board_comments c JOIN page_board_threads t ON t.id=c.thread_id WHERE c.id=NEW.comment_id AND c.thread_id=NEW.thread_id AND t.page_id=NEW.page_id)
BEGIN SELECT RAISE(ABORT,'board comment history source mismatch'); END;
CREATE TABLE page_board_comment_reactions (
 comment_id TEXT NOT NULL REFERENCES page_board_comments(id) ON DELETE CASCADE,
 user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 emoji TEXT NOT NULL CHECK(emoji IN ('👍','❤️','👀','✅')),
 created_at TEXT NOT NULL,
 PRIMARY KEY(comment_id,user_id,emoji)
);
CREATE INDEX idx_board_comment_reaction_user ON page_board_comment_reactions(user_id);
CREATE TABLE page_board_comment_mutations_v2 (
 id TEXT NOT NULL PRIMARY KEY,
 page_id TEXT NOT NULL REFERENCES pages(id) ON DELETE CASCADE,
 thread_id TEXT NOT NULL REFERENCES page_board_threads(id) ON DELETE CASCADE,
 comment_id TEXT REFERENCES page_board_comments(id) ON DELETE SET NULL,
 mutation_id TEXT NOT NULL,
 request_digest TEXT NOT NULL,
 actor_user_id TEXT REFERENCES users(id) ON DELETE SET NULL,
 operation TEXT NOT NULL CHECK(operation IN ('create','reply','resolve','reopen','edit','delete','react')),
 result_version INTEGER NOT NULL,
 created_at TEXT NOT NULL,
 reaction_emoji TEXT CHECK(reaction_emoji IS NULL OR reaction_emoji IN ('👍','❤️','👀','✅')),
 reaction_active INTEGER CHECK(reaction_active IS NULL OR reaction_active IN (0,1)),
 UNIQUE(page_id,actor_user_id,mutation_id)
);
INSERT INTO page_board_comment_mutations_v2(id,page_id,thread_id,comment_id,mutation_id,request_digest,actor_user_id,operation,result_version,created_at)
 SELECT id,page_id,thread_id,comment_id,mutation_id,request_digest,actor_user_id,operation,result_version,created_at FROM page_board_comment_mutations;
DROP TABLE page_board_comment_mutations;
ALTER TABLE page_board_comment_mutations_v2 RENAME TO page_board_comment_mutations;
CREATE INDEX idx_page_board_comment_mutations_thread ON page_board_comment_mutations(thread_id);
CREATE INDEX idx_page_board_comment_mutations_comment ON page_board_comment_mutations(comment_id);
CREATE INDEX idx_page_board_comment_mutations_actor ON page_board_comment_mutations(actor_user_id);
CREATE TRIGGER board_comment_mutation_source BEFORE INSERT ON page_board_comment_mutations
WHEN NOT EXISTS(SELECT 1 FROM page_board_threads t WHERE t.id=NEW.thread_id AND t.page_id=NEW.page_id)
 OR (NEW.comment_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM page_board_comments c WHERE c.id=NEW.comment_id AND c.thread_id=NEW.thread_id))
BEGIN SELECT RAISE(ABORT,'board comment mutation source mismatch'); END;
CREATE TRIGGER board_comment_mutation_immutable BEFORE UPDATE ON page_board_comment_mutations
WHEN NEW.id IS NOT OLD.id OR NEW.page_id IS NOT OLD.page_id OR NEW.thread_id IS NOT OLD.thread_id OR NEW.mutation_id IS NOT OLD.mutation_id OR NEW.request_digest IS NOT OLD.request_digest OR NEW.operation IS NOT OLD.operation OR NEW.result_version IS NOT OLD.result_version OR NEW.created_at IS NOT OLD.created_at OR NEW.reaction_emoji IS NOT OLD.reaction_emoji OR NEW.reaction_active IS NOT OLD.reaction_active
 OR (NEW.comment_id IS NOT OLD.comment_id AND NOT(NEW.comment_id IS NULL AND NOT EXISTS(SELECT 1 FROM page_board_comments WHERE id=OLD.comment_id)))
 OR (NEW.actor_user_id IS NOT OLD.actor_user_id AND NOT(NEW.actor_user_id IS NULL AND NOT EXISTS(SELECT 1 FROM users WHERE id=OLD.actor_user_id)))
BEGIN SELECT RAISE(ABORT,'immutable board comment mutation'); END;
