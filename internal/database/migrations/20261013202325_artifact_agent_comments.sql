-- Explicit named-agent comment grants do not imply publication or dispatch.
CREATE TABLE page_grants_agent_comments (
 page_id TEXT NOT NULL REFERENCES pages(id) ON DELETE CASCADE,
 subject_type TEXT NOT NULL CHECK(subject_type IN ('user','crew','agent','workspace')),
 subject_id TEXT NOT NULL DEFAULT '',
 level TEXT NOT NULL CHECK(level IN ('read','comment','produce','write')),
 panel_ids TEXT,
 granted_by_user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 granted_at TEXT NOT NULL DEFAULT(datetime('now')),
 PRIMARY KEY(page_id,subject_type,subject_id,level),
 CHECK(panel_ids IS NULL OR level='produce'),
 CHECK(subject_type<>'workspace' OR(subject_id='' AND level IN ('read','comment','write')))
);
INSERT INTO page_grants_agent_comments SELECT page_id,subject_type,subject_id,level,panel_ids,granted_by_user_id,granted_at FROM page_grants;
DROP TABLE page_grants;
ALTER TABLE page_grants_agent_comments RENAME TO page_grants;
CREATE INDEX idx_page_grants_subject ON page_grants(subject_type,subject_id);
CREATE INDEX idx_page_grants_granted_by ON page_grants(granted_by_user_id);
ALTER TABLE page_artifact_comments ADD COLUMN author_agent_id TEXT REFERENCES agents(id) ON DELETE SET NULL;
ALTER TABLE page_artifact_comments ADD COLUMN author_run_id TEXT;
CREATE INDEX idx_artifact_comment_agent ON page_artifact_comments(author_agent_id);
ALTER TABLE page_artifact_comment_mutations ADD COLUMN actor_agent_id TEXT REFERENCES agents(id) ON DELETE SET NULL;
ALTER TABLE page_artifact_comment_mutations ADD COLUMN actor_run_id TEXT;
CREATE INDEX idx_artifact_comment_mutation_agent ON page_artifact_comment_mutations(actor_agent_id);
CREATE TRIGGER artifact_comment_agent_provenance_immutable BEFORE UPDATE OF author_agent_id,author_run_id,author_user_id ON page_artifact_comments
WHEN (OLD.author_run_id IS NOT NULL AND NEW.author_user_id IS NOT OLD.author_user_id AND NOT(NEW.author_user_id IS NULL AND NOT EXISTS(SELECT 1 FROM users WHERE id=OLD.author_user_id))) OR NEW.author_run_id IS NOT OLD.author_run_id OR (NEW.author_agent_id IS NOT OLD.author_agent_id AND NOT(NEW.author_agent_id IS NULL AND NOT EXISTS(SELECT 1 FROM agents WHERE id=OLD.author_agent_id)))
BEGIN SELECT RAISE(ABORT,'immutable artifact comment agent provenance'); END;
CREATE TRIGGER artifact_comment_actor_provenance_immutable BEFORE UPDATE OF actor_agent_id,actor_run_id,actor_user_id ON page_artifact_comment_mutations
WHEN (OLD.actor_run_id IS NOT NULL AND NEW.actor_user_id IS NOT OLD.actor_user_id AND NOT(NEW.actor_user_id IS NULL AND NOT EXISTS(SELECT 1 FROM users WHERE id=OLD.actor_user_id))) OR NEW.actor_run_id IS NOT OLD.actor_run_id OR (NEW.actor_agent_id IS NOT OLD.actor_agent_id AND NOT(NEW.actor_agent_id IS NULL AND NOT EXISTS(SELECT 1 FROM agents WHERE id=OLD.actor_agent_id)))
BEGIN SELECT RAISE(ABORT,'immutable artifact comment mutation provenance'); END;
