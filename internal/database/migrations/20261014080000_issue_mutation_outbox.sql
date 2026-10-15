-- Source mutation, activity and delivery intent commit together. A stable
-- assignment ID and stable journal replay ID fence recovery after a lost ACK.
CREATE TABLE issue_mutation_outbox (
 id TEXT PRIMARY KEY,
 workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 -- Historical for journal jobs: deleting an issue must not delete an audit
 -- intent that has not yet reached the hash chain during a writer outage.
 mission_id TEXT NOT NULL,
 comment_id TEXT REFERENCES mission_comments(id) ON DELETE CASCADE,
 kind TEXT NOT NULL CHECK(kind IN ('journal','mention')),
 payload TEXT NOT NULL CHECK(json_valid(payload)),
 state TEXT NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','processing','done','failed')),
 lease_token TEXT,
 lease_expires_at TEXT,
 attempts INTEGER NOT NULL DEFAULT 0,
 max_attempts INTEGER NOT NULL DEFAULT 8 CHECK(max_attempts>0),
 failed_at TEXT,
 failure_code TEXT NOT NULL DEFAULT '',
 next_attempt_at TEXT NOT NULL,
 last_error TEXT NOT NULL DEFAULT '',
 created_at TEXT NOT NULL,
 completed_at TEXT,
 CHECK((kind='journal' AND comment_id IS NULL) OR (kind='mention' AND comment_id IS NOT NULL))
);
CREATE INDEX issue_mutation_outbox_pending ON issue_mutation_outbox(state,next_attempt_at,lease_expires_at);
CREATE INDEX issue_mutation_outbox_workspace ON issue_mutation_outbox(workspace_id);
CREATE INDEX issue_mutation_outbox_comment ON issue_mutation_outbox(comment_id) WHERE comment_id IS NOT NULL;
CREATE TRIGGER issue_mutation_outbox_mention_scope_insert BEFORE INSERT ON issue_mutation_outbox
WHEN NEW.kind='mention' AND NOT EXISTS(
 SELECT 1 FROM mission_comments c JOIN missions m ON m.id=c.mission_id
 WHERE c.id=NEW.comment_id AND c.mission_id=NEW.mission_id AND m.workspace_id=NEW.workspace_id
)
BEGIN SELECT RAISE(ABORT,'mention outbox scope mismatch'); END;
CREATE TRIGGER issue_mutation_outbox_mention_scope_update BEFORE UPDATE OF workspace_id,mission_id,comment_id,kind ON issue_mutation_outbox
WHEN NEW.kind='mention' AND NOT EXISTS(
 SELECT 1 FROM mission_comments c JOIN missions m ON m.id=c.mission_id
 WHERE c.id=NEW.comment_id AND c.mission_id=NEW.mission_id AND m.workspace_id=NEW.workspace_id
)
BEGIN SELECT RAISE(ABORT,'mention outbox scope mismatch'); END;
