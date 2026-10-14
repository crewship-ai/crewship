ALTER TABLE page_artifact_comments ADD COLUMN version INTEGER NOT NULL DEFAULT 1 CHECK(version>0);
ALTER TABLE page_artifact_comments ADD COLUMN edited_at TEXT;
ALTER TABLE page_artifact_comments ADD COLUMN deleted_at TEXT;
ALTER TABLE page_artifact_comments ADD COLUMN edited_by_user_id TEXT REFERENCES users(id) ON DELETE SET NULL;
ALTER TABLE page_artifact_comments ADD COLUMN deleted_by_user_id TEXT REFERENCES users(id) ON DELETE SET NULL;
CREATE INDEX idx_artifact_comment_editor ON page_artifact_comments(edited_by_user_id);
CREATE INDEX idx_artifact_comment_deleter ON page_artifact_comments(deleted_by_user_id);
CREATE TABLE page_artifact_comment_history (
 id TEXT NOT NULL PRIMARY KEY,
 page_id TEXT NOT NULL REFERENCES pages(id) ON DELETE CASCADE,
 thread_id TEXT NOT NULL REFERENCES page_artifact_threads(id) ON DELETE CASCADE,
 comment_id TEXT NOT NULL REFERENCES page_artifact_comments(id) ON DELETE CASCADE,
 version INTEGER NOT NULL CHECK(version>0),
 body TEXT NOT NULL,
 action TEXT NOT NULL CHECK(action IN ('create','edit','delete')),
 actor_user_id TEXT REFERENCES users(id) ON DELETE SET NULL,
 actor_agent_id TEXT REFERENCES agents(id) ON DELETE SET NULL,
 actor_run_id TEXT,
 created_at TEXT NOT NULL,
 UNIQUE(comment_id,version)
);
CREATE INDEX idx_artifact_comment_history_page ON page_artifact_comment_history(page_id);
CREATE INDEX idx_artifact_comment_history_thread ON page_artifact_comment_history(thread_id);
CREATE INDEX idx_artifact_comment_history_actor ON page_artifact_comment_history(actor_user_id);
CREATE INDEX idx_artifact_comment_history_agent ON page_artifact_comment_history(actor_agent_id);
INSERT INTO page_artifact_comment_history(id,page_id,thread_id,comment_id,version,body,action,actor_user_id,actor_agent_id,actor_run_id,created_at)
 SELECT 'initial-'||c.id,t.page_id,t.id,c.id,1,c.body,'create',c.author_user_id,c.author_agent_id,c.author_run_id,c.created_at FROM page_artifact_comments c JOIN page_artifact_threads t ON t.id=c.thread_id;
CREATE TRIGGER artifact_comment_history_immutable BEFORE UPDATE ON page_artifact_comment_history
WHEN NEW.id IS NOT OLD.id OR NEW.page_id IS NOT OLD.page_id OR NEW.thread_id IS NOT OLD.thread_id OR NEW.comment_id IS NOT OLD.comment_id OR NEW.version IS NOT OLD.version OR NEW.body IS NOT OLD.body OR NEW.action IS NOT OLD.action OR NEW.actor_run_id IS NOT OLD.actor_run_id OR NEW.created_at IS NOT OLD.created_at
 OR (NEW.actor_user_id IS NOT OLD.actor_user_id AND NOT(NEW.actor_user_id IS NULL AND NOT EXISTS(SELECT 1 FROM users WHERE id=OLD.actor_user_id)))
 OR (NEW.actor_agent_id IS NOT OLD.actor_agent_id AND NOT(NEW.actor_agent_id IS NULL AND NOT EXISTS(SELECT 1 FROM agents WHERE id=OLD.actor_agent_id)))
BEGIN SELECT RAISE(ABORT,'immutable artifact comment history'); END;
CREATE TABLE page_artifact_comment_reactions (
 comment_id TEXT NOT NULL REFERENCES page_artifact_comments(id) ON DELETE CASCADE,
 user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 emoji TEXT NOT NULL CHECK(emoji IN ('👍','❤️','👀','✅')),
 created_at TEXT NOT NULL,
 PRIMARY KEY(comment_id,user_id,emoji)
);
CREATE INDEX idx_artifact_comment_reaction_user ON page_artifact_comment_reactions(user_id);
-- No table references mutation receipts. Preserve all actor/run provenance.
CREATE TABLE page_artifact_comment_mutations_v2 (
 id TEXT NOT NULL PRIMARY KEY,
 page_id TEXT NOT NULL REFERENCES pages(id) ON DELETE CASCADE,
 thread_id TEXT NOT NULL REFERENCES page_artifact_threads(id) ON DELETE CASCADE,
 comment_id TEXT REFERENCES page_artifact_comments(id) ON DELETE SET NULL,
 mutation_id TEXT NOT NULL,
 request_digest TEXT NOT NULL,
 actor_user_id TEXT REFERENCES users(id) ON DELETE SET NULL,
 operation TEXT NOT NULL CHECK(operation IN ('create','reply','resolve','reopen','edit','delete','react')),
 result_version INTEGER NOT NULL,
 created_at TEXT NOT NULL,
 actor_agent_id TEXT REFERENCES agents(id) ON DELETE SET NULL,
 actor_run_id TEXT,
 reaction_emoji TEXT CHECK(reaction_emoji IS NULL OR reaction_emoji IN ('👍','❤️','👀','✅')),
 reaction_active INTEGER CHECK(reaction_active IS NULL OR reaction_active IN (0,1)),
 UNIQUE(page_id,mutation_id)
);
INSERT INTO page_artifact_comment_mutations_v2(id,page_id,thread_id,comment_id,mutation_id,request_digest,actor_user_id,operation,result_version,created_at,actor_agent_id,actor_run_id)
 SELECT id,page_id,thread_id,comment_id,mutation_id,request_digest,actor_user_id,operation,result_version,created_at,actor_agent_id,actor_run_id FROM page_artifact_comment_mutations;
DROP TABLE page_artifact_comment_mutations;
ALTER TABLE page_artifact_comment_mutations_v2 RENAME TO page_artifact_comment_mutations;
CREATE INDEX idx_page_artifact_comment_mutations_thread ON page_artifact_comment_mutations(thread_id);
CREATE INDEX idx_page_artifact_comment_mutations_comment ON page_artifact_comment_mutations(comment_id);
CREATE INDEX idx_page_artifact_comment_mutations_actor ON page_artifact_comment_mutations(actor_user_id);
CREATE INDEX idx_artifact_comment_mutation_agent ON page_artifact_comment_mutations(actor_agent_id);
CREATE TRIGGER artifact_comment_actor_provenance_immutable BEFORE UPDATE OF actor_agent_id,actor_run_id,actor_user_id ON page_artifact_comment_mutations
WHEN (OLD.actor_run_id IS NOT NULL AND NEW.actor_user_id IS NOT OLD.actor_user_id AND NOT(NEW.actor_user_id IS NULL AND NOT EXISTS(SELECT 1 FROM users WHERE id=OLD.actor_user_id))) OR NEW.actor_run_id IS NOT OLD.actor_run_id OR (NEW.actor_agent_id IS NOT OLD.actor_agent_id AND NOT(NEW.actor_agent_id IS NULL AND NOT EXISTS(SELECT 1 FROM agents WHERE id=OLD.actor_agent_id)))
BEGIN SELECT RAISE(ABORT,'immutable artifact comment mutation provenance'); END;
CREATE TRIGGER artifact_comment_history_source BEFORE INSERT ON page_artifact_comment_history
WHEN NOT EXISTS(SELECT 1 FROM page_artifact_comments c JOIN page_artifact_threads t ON t.id=c.thread_id WHERE c.id=NEW.comment_id AND c.thread_id=NEW.thread_id AND t.page_id=NEW.page_id)
BEGIN SELECT RAISE(ABORT,'artifact comment history source mismatch'); END;
CREATE TRIGGER artifact_comment_mutation_source BEFORE INSERT ON page_artifact_comment_mutations
WHEN NOT EXISTS(SELECT 1 FROM page_artifact_threads t WHERE t.id=NEW.thread_id AND t.page_id=NEW.page_id)
 OR (NEW.comment_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM page_artifact_comments c WHERE c.id=NEW.comment_id AND c.thread_id=NEW.thread_id))
BEGIN SELECT RAISE(ABORT,'artifact comment mutation source mismatch'); END;
