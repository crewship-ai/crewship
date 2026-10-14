-- Final durable Board foundations, including source pins, comments, jobs and audit.
-- Present in the Artifact delivery layer for shared storage/restore compatibility;
-- API/editor availability is controlled by the subsequent Board product delivery.

CREATE TABLE page_boards (
  page_id TEXT PRIMARY KEY REFERENCES pages(id) ON DELETE CASCADE,
  seq INTEGER NOT NULL CHECK(seq>=0),
  format TEXT NOT NULL CHECK(format='crewship-board-excalidraw-0.18.1-v1')
);

CREATE TABLE page_board_elements (
  board_page_id TEXT NOT NULL REFERENCES page_boards(page_id) ON DELETE CASCADE,
  element_id TEXT NOT NULL,
  element_rev INTEGER NOT NULL CHECK(element_rev>0),
  element_json TEXT,
  author_user_id TEXT REFERENCES users(id) ON DELETE SET NULL,
  updated_seq INTEGER NOT NULL,
  author_agent_id TEXT REFERENCES agents(id) ON DELETE SET NULL,
  author_run_id TEXT,
  origin_request_id TEXT REFERENCES page_board_requests(id) ON DELETE SET NULL,
  deleted_at TEXT,
  PRIMARY KEY(board_page_id,element_id)
);

CREATE TABLE page_board_mutations (
  board_page_id TEXT NOT NULL REFERENCES page_boards(page_id) ON DELETE CASCADE,
  mutation_id TEXT NOT NULL,
  author_user_id TEXT REFERENCES users(id) ON DELETE SET NULL,
  fingerprint TEXT NOT NULL,
  result_seq INTEGER NOT NULL,
  placement_id TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  author_agent_id TEXT REFERENCES agents(id) ON DELETE SET NULL,
  author_run_id TEXT,
  result_file_id TEXT REFERENCES page_board_files(id) ON DELETE SET NULL,
  placement_revs_json TEXT NOT NULL DEFAULT '{}',
  element_revs_json TEXT NOT NULL DEFAULT '{}',
  PRIMARY KEY(board_page_id,mutation_id)
);

CREATE TABLE page_board_artifact_placements (
  id TEXT PRIMARY KEY,
  board_page_id TEXT NOT NULL REFERENCES page_boards(page_id) ON DELETE CASCADE,
  source_page_id TEXT,
  source_revision INTEGER,
  x REAL NOT NULL,
  y REAL NOT NULL,
  width REAL NOT NULL,
  height REAL NOT NULL,
  updated_seq INTEGER NOT NULL,
  author_user_id TEXT REFERENCES users(id) ON DELETE SET NULL,
  placement_rev INTEGER NOT NULL DEFAULT 1 CHECK(placement_rev>0),
  deleted_at TEXT,
  mode TEXT NOT NULL DEFAULT 'fixed' CHECK(mode IN ('fixed','follow_latest')),
  CHECK((source_page_id IS NULL AND source_revision IS NULL) OR (source_page_id IS NOT NULL AND source_revision>0)),
  FOREIGN KEY(source_page_id,source_revision) REFERENCES page_artifact_revisions(page_id,revision) ON DELETE SET NULL
);

CREATE TABLE page_board_run_delegations (
  board_page_id TEXT NOT NULL REFERENCES page_boards(page_id) ON DELETE CASCADE,
  run_id TEXT NOT NULL REFERENCES artifact_run_principals(run_id) ON DELETE CASCADE,
  granted_by_user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  granted_at TEXT NOT NULL,
  expires_at TEXT NOT NULL,
  revoked_at TEXT,
  revoked_by_user_id TEXT REFERENCES users(id) ON DELETE SET NULL,
  PRIMARY KEY(board_page_id,run_id)
);

CREATE TABLE page_board_files (
  id TEXT PRIMARY KEY,
  board_page_id TEXT NOT NULL REFERENCES page_boards(page_id) ON DELETE CASCADE,
  sha256 TEXT NOT NULL CHECK(length(sha256)=64),
  content_type TEXT NOT NULL CHECK(content_type IN ('image/png','image/jpeg')),
  width INTEGER NOT NULL CHECK(width BETWEEN 1 AND 2048),
  height INTEGER NOT NULL CHECK(height BETWEEN 1 AND 2048),
  byte_size INTEGER NOT NULL CHECK(byte_size BETWEEN 1 AND 8388608),
  author_user_id TEXT REFERENCES users(id) ON DELETE SET NULL,
  created_at TEXT NOT NULL,
  last_used_at TEXT NOT NULL DEFAULT ''
);

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

CREATE TABLE page_board_comments (
  id TEXT PRIMARY KEY,
  thread_id TEXT NOT NULL REFERENCES page_board_threads(id) ON DELETE CASCADE,
  thread_version INTEGER NOT NULL CHECK(thread_version > 0),
  body TEXT NOT NULL CHECK(length(body)>0),
  author_user_id TEXT REFERENCES users(id) ON DELETE SET NULL,
  created_at TEXT NOT NULL,
  version INTEGER NOT NULL DEFAULT 1 CHECK(version>0),
  edited_at TEXT,
  deleted_at TEXT,
  edited_by_user_id TEXT REFERENCES users(id) ON DELETE SET NULL,
  deleted_by_user_id TEXT REFERENCES users(id) ON DELETE SET NULL,
  UNIQUE(thread_id,thread_version)
);

CREATE TABLE page_board_requests (
  id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
  board_page_id TEXT REFERENCES page_boards(page_id) ON DELETE SET NULL,
  original_board_id TEXT NOT NULL,
  human_user_id TEXT REFERENCES users(id) ON DELETE SET NULL,
  agent_id TEXT REFERENCES agents(id) ON DELETE SET NULL,
  chat_id TEXT REFERENCES chats(id) ON DELETE SET NULL,
  kind TEXT NOT NULL CHECK(kind IN ('diagram','artifact')),
  base_seq INTEGER NOT NULL,
  submission_id TEXT NOT NULL,
  fingerprint TEXT NOT NULL,
  instruction TEXT NOT NULL,
  selected_element_ids_json TEXT NOT NULL,
  selected_placement_ids_json TEXT NOT NULL,
  snapshot_json TEXT NOT NULL,
  target_json TEXT NOT NULL,
  dialect TEXT NOT NULL,
  converter_version TEXT NOT NULL,
  receipt_json TEXT NOT NULL,
  status TEXT NOT NULL CHECK(status IN ('queued','running','ready_to_insert','inserted','succeeded','failed','cancelled')),
  next_attempt_at TEXT NOT NULL DEFAULT '',
  run_id TEXT,
  mermaid_source TEXT NOT NULL DEFAULT '',
  source_hash TEXT NOT NULL DEFAULT '',
  source_mutation_id TEXT NOT NULL DEFAULT '',
  source_fingerprint TEXT NOT NULL DEFAULT '',
  result_seq INTEGER,
  result_page_id TEXT,
  result_revision INTEGER,
  inserted_element_ids_json TEXT NOT NULL DEFAULT '[]',
  inserted_element_hashes_json TEXT NOT NULL DEFAULT '{}',
  inserted_by_user_id TEXT REFERENCES users(id) ON DELETE SET NULL,
  cancelled_by_user_id TEXT REFERENCES users(id) ON DELETE SET NULL,
  pin_status TEXT NOT NULL DEFAULT '',
  pin_reason TEXT NOT NULL DEFAULT '',
  result_placement_id TEXT,
  error TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  deadline_at TEXT NOT NULL DEFAULT '',
  retry_of_request_id TEXT REFERENCES page_board_requests(id) ON DELETE SET NULL,
  notice_pending INTEGER NOT NULL DEFAULT 0 CHECK(notice_pending IN(0,1)),
  UNIQUE(board_page_id,human_user_id,submission_id),
  FOREIGN KEY(result_page_id,result_revision) REFERENCES page_artifact_revisions(page_id,revision) ON DELETE SET NULL,
  CHECK((result_page_id IS NULL AND result_revision IS NULL) OR (result_page_id IS NOT NULL AND result_revision>0))
);

CREATE TABLE page_board_request_files (
  request_id TEXT NOT NULL REFERENCES page_board_requests(id) ON DELETE CASCADE,
  file_id TEXT NOT NULL REFERENCES page_board_files(id) ON DELETE CASCADE,
  PRIMARY KEY(request_id,file_id)
);

CREATE TABLE page_board_request_sources (
  id TEXT PRIMARY KEY,
  request_id TEXT NOT NULL REFERENCES page_board_requests(id) ON DELETE CASCADE,
  placement_id TEXT NOT NULL,
  source_page_id TEXT,
  source_revision INTEGER,
  FOREIGN KEY(source_page_id,source_revision) REFERENCES page_artifact_revisions(page_id,revision) ON DELETE SET NULL,
  CHECK((source_page_id IS NULL AND source_revision IS NULL) OR (source_page_id IS NOT NULL AND source_revision>0)),
  UNIQUE(request_id,placement_id)
);

CREATE TABLE page_board_element_permissions (
  id TEXT PRIMARY KEY,
  board_page_id TEXT NOT NULL REFERENCES page_boards(page_id) ON DELETE CASCADE,
  run_id TEXT NOT NULL REFERENCES artifact_run_principals(run_id) ON DELETE CASCADE,
  agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
  granted_by_user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  mutation_id TEXT NOT NULL,
  request_digest TEXT NOT NULL,
  elements_json TEXT NOT NULL,
  methods_json TEXT NOT NULL,
  purpose TEXT NOT NULL,
  expires_at TEXT NOT NULL,
  created_at TEXT NOT NULL,
  used_mutation_id TEXT,
  revoked_at TEXT,
  revoked_by_user_id TEXT REFERENCES users(id) ON DELETE SET NULL,
  UNIQUE(board_page_id,granted_by_user_id,mutation_id)
);

CREATE TABLE page_board_write_audit (
  board_page_id TEXT NOT NULL REFERENCES page_boards(page_id) ON DELETE CASCADE,
  mutation_id TEXT NOT NULL,
  actor_user_id TEXT REFERENCES users(id) ON DELETE SET NULL,
  actor_agent_id TEXT REFERENCES agents(id) ON DELETE SET NULL,
  actor_run_id TEXT,
  result_seq INTEGER NOT NULL,
  request_digest TEXT NOT NULL,
  created_at TEXT NOT NULL,
  PRIMARY KEY(board_page_id,mutation_id)
);

CREATE TABLE page_board_source_history (
  board_page_id TEXT NOT NULL,
  mutation_id TEXT NOT NULL,
  placement_id TEXT NOT NULL REFERENCES page_board_artifact_placements(id) ON DELETE CASCADE,
  source_page_id TEXT,
  source_revision INTEGER,
  PRIMARY KEY(board_page_id,mutation_id,placement_id),
  FOREIGN KEY(board_page_id,mutation_id) REFERENCES page_board_mutations(board_page_id,mutation_id) ON DELETE CASCADE DEFERRABLE INITIALLY DEFERRED,
  FOREIGN KEY(source_page_id,source_revision) REFERENCES page_artifact_revisions(page_id,revision) ON DELETE SET NULL,
  CHECK((source_page_id IS NULL AND source_revision IS NULL) OR(source_page_id IS NOT NULL AND source_revision>0))
);

CREATE TABLE page_board_request_previews (
  request_id TEXT NOT NULL PRIMARY KEY REFERENCES page_board_requests(id) ON DELETE CASCADE,
  sha256 TEXT NOT NULL CHECK(length(sha256)=64),
  width INTEGER NOT NULL CHECK(width BETWEEN 1 AND 1024),
  height INTEGER NOT NULL CHECK(height BETWEEN 1 AND 1024),
  byte_size INTEGER NOT NULL CHECK(byte_size BETWEEN 1 AND 2097152),
  created_at TEXT NOT NULL
);

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

CREATE TABLE page_board_comment_reactions (
  comment_id TEXT NOT NULL REFERENCES page_board_comments(id) ON DELETE CASCADE,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  emoji TEXT NOT NULL CHECK(emoji IN ('👍','❤️','👀','✅')),
  created_at TEXT NOT NULL,
  PRIMARY KEY(comment_id,user_id,emoji)
);

CREATE TABLE page_board_comment_mutations (
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

CREATE INDEX idx_board_element_author ON page_board_elements(author_user_id);

CREATE INDEX idx_board_mutation_author ON page_board_mutations(author_user_id);

CREATE INDEX idx_board_placement_source ON page_board_artifact_placements(source_page_id,source_revision);

CREATE INDEX idx_board_placement_revision ON page_board_artifact_placements(source_revision);

CREATE INDEX idx_board_placement_author ON page_board_artifact_placements(author_user_id);

CREATE INDEX idx_board_delegation_run ON page_board_run_delegations(run_id);

CREATE INDEX idx_board_delegation_granter ON page_board_run_delegations(granted_by_user_id);

CREATE INDEX idx_board_delegation_revoker ON page_board_run_delegations(revoked_by_user_id);

CREATE INDEX idx_board_element_agent ON page_board_elements(author_agent_id);

CREATE INDEX idx_board_mutation_agent ON page_board_mutations(author_agent_id);

CREATE INDEX idx_board_files_digest ON page_board_files(sha256);

CREATE INDEX idx_board_files_author ON page_board_files(author_user_id);

CREATE INDEX idx_board_mutation_file ON page_board_mutations(result_file_id);

CREATE INDEX idx_board_placement_live ON page_board_artifact_placements(board_page_id,deleted_at);

CREATE INDEX idx_page_board_threads_author ON page_board_threads(created_by_user_id);

CREATE INDEX idx_page_board_threads_resolver ON page_board_threads(resolved_by_user_id);

CREATE INDEX idx_page_board_comments_author ON page_board_comments(author_user_id);

CREATE INDEX idx_page_board_threads_page ON page_board_threads(page_id);

CREATE INDEX idx_board_request_workspace ON page_board_requests(workspace_id);

CREATE INDEX idx_board_request_human ON page_board_requests(human_user_id);

CREATE INDEX idx_board_request_agent ON page_board_requests(agent_id);

CREATE INDEX idx_board_request_chat ON page_board_requests(chat_id);

CREATE INDEX idx_board_request_queue ON page_board_requests(status,next_attempt_at,created_at);

CREATE INDEX idx_board_request_run ON page_board_requests(run_id);

CREATE INDEX idx_board_request_inserted_by ON page_board_requests(inserted_by_user_id);

CREATE INDEX idx_board_request_cancelled_by ON page_board_requests(cancelled_by_user_id);

CREATE INDEX idx_board_request_result ON page_board_requests(result_page_id,result_revision);

CREATE INDEX idx_board_request_revision ON page_board_requests(result_revision);

CREATE INDEX idx_board_request_file ON page_board_request_files(file_id);

CREATE INDEX idx_board_request_source_page ON page_board_request_sources(source_page_id,source_revision);

CREATE INDEX idx_board_request_source_revision ON page_board_request_sources(source_revision);

CREATE INDEX idx_board_element_origin_request ON page_board_elements(origin_request_id);

CREATE INDEX idx_board_permission_board ON page_board_element_permissions(board_page_id,created_at);

CREATE INDEX idx_board_permission_run ON page_board_element_permissions(run_id);

CREATE INDEX idx_board_permission_agent ON page_board_element_permissions(agent_id);

CREATE INDEX idx_board_permission_granter ON page_board_element_permissions(granted_by_user_id);

CREATE INDEX idx_board_permission_revoker ON page_board_element_permissions(revoked_by_user_id);

CREATE INDEX idx_board_element_deleted ON page_board_elements(board_page_id,deleted_at);

CREATE INDEX idx_board_audit_user ON page_board_write_audit(actor_user_id);

CREATE INDEX idx_board_audit_agent ON page_board_write_audit(actor_agent_id);

CREATE INDEX idx_board_mutation_created ON page_board_mutations(board_page_id,created_at);

CREATE INDEX idx_board_history_placement ON page_board_source_history(placement_id);

CREATE INDEX idx_board_history_source ON page_board_source_history(source_page_id,source_revision);

CREATE INDEX idx_board_history_revision ON page_board_source_history(source_revision);

CREATE INDEX idx_board_history_mutation ON page_board_source_history(mutation_id,board_page_id);

CREATE INDEX idx_board_request_preview_digest ON page_board_request_previews(sha256);

CREATE INDEX idx_board_request_retry ON page_board_requests(retry_of_request_id);

CREATE INDEX idx_board_comment_editor ON page_board_comments(edited_by_user_id);

CREATE INDEX idx_board_comment_deleter ON page_board_comments(deleted_by_user_id);

CREATE INDEX idx_board_comment_history_page ON page_board_comment_history(page_id);

CREATE INDEX idx_board_comment_history_thread ON page_board_comment_history(thread_id);

CREATE INDEX idx_board_comment_history_actor ON page_board_comment_history(actor_user_id);

CREATE INDEX idx_board_comment_reaction_user ON page_board_comment_reactions(user_id);

CREATE INDEX idx_page_board_comment_mutations_thread ON page_board_comment_mutations(thread_id);

CREATE INDEX idx_page_board_comment_mutations_comment ON page_board_comment_mutations(comment_id);

CREATE INDEX idx_page_board_comment_mutations_actor ON page_board_comment_mutations(actor_user_id);

CREATE INDEX idx_board_file_retention ON page_board_files(board_page_id,last_used_at);

CREATE TRIGGER board_empty_page BEFORE INSERT ON page_boards
WHEN NOT EXISTS(SELECT 1 FROM pages WHERE id=NEW.page_id AND owner_user_id IS NOT NULL AND owner_crew_id IS NULL)
 OR EXISTS(SELECT 1 FROM page_panels WHERE page_id=NEW.page_id)
 OR EXISTS(SELECT 1 FROM page_project_drafts WHERE page_id=NEW.page_id)
 OR EXISTS(SELECT 1 FROM page_project_live WHERE page_id=NEW.page_id)
 OR EXISTS(SELECT 1 FROM page_artifacts WHERE page_id=NEW.page_id)
BEGIN SELECT RAISE(ABORT,'board requires an empty human-owned page'); END;

CREATE TRIGGER board_no_artifact BEFORE INSERT ON page_artifacts WHEN EXISTS(SELECT 1 FROM page_boards WHERE page_id=NEW.page_id)
BEGIN SELECT RAISE(ABORT,'board cannot contain artifact'); END;

CREATE TRIGGER board_no_panels BEFORE INSERT ON page_panels WHEN EXISTS(SELECT 1 FROM page_boards WHERE page_id=NEW.page_id)
BEGIN SELECT RAISE(ABORT,'board cannot contain panels'); END;

CREATE TRIGGER board_no_project BEFORE INSERT ON page_project_drafts WHEN EXISTS(SELECT 1 FROM page_boards WHERE page_id=NEW.page_id)
BEGIN SELECT RAISE(ABORT,'board cannot contain project'); END;

CREATE TRIGGER board_no_live_project BEFORE INSERT ON page_project_live WHEN EXISTS(SELECT 1 FROM page_boards WHERE page_id=NEW.page_id)
BEGIN SELECT RAISE(ABORT,'board cannot contain application'); END;

CREATE TRIGGER board_human_owner BEFORE UPDATE OF owner_user_id,owner_crew_id ON pages
WHEN EXISTS(SELECT 1 FROM page_boards WHERE page_id=NEW.id) AND (NEW.owner_user_id IS NULL OR NEW.owner_crew_id IS NOT NULL)
BEGIN SELECT RAISE(ABORT,'board owner must be human'); END;

CREATE TRIGGER board_no_panel_move BEFORE UPDATE OF page_id ON page_panels WHEN EXISTS(SELECT 1 FROM page_boards WHERE page_id=NEW.page_id)
BEGIN SELECT RAISE(ABORT,'board cannot contain panels'); END;

CREATE TRIGGER board_no_project_move BEFORE UPDATE OF page_id ON page_project_drafts WHEN EXISTS(SELECT 1 FROM page_boards WHERE page_id=NEW.page_id)
BEGIN SELECT RAISE(ABORT,'board cannot contain project'); END;

CREATE TRIGGER board_no_live_project_move BEFORE UPDATE OF page_id ON page_project_live WHEN EXISTS(SELECT 1 FROM page_boards WHERE page_id=NEW.page_id)
BEGIN SELECT RAISE(ABORT,'board cannot contain application'); END;

CREATE TRIGGER board_no_artifact_move BEFORE UPDATE OF page_id ON page_artifacts WHEN EXISTS(SELECT 1 FROM page_boards WHERE page_id=NEW.page_id)
BEGIN SELECT RAISE(ABORT,'board cannot contain artifact'); END;

CREATE TRIGGER board_placement_workspace BEFORE INSERT ON page_board_artifact_placements
WHEN NEW.source_page_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM pages b JOIN pages s ON s.workspace_id=b.workspace_id WHERE b.id=NEW.board_page_id AND s.id=NEW.source_page_id)
BEGIN SELECT RAISE(ABORT,'board source must belong to same workspace'); END;

CREATE TRIGGER board_placement_workspace_update BEFORE UPDATE OF board_page_id,source_page_id ON page_board_artifact_placements
WHEN NEW.source_page_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM pages b JOIN pages s ON s.workspace_id=b.workspace_id WHERE b.id=NEW.board_page_id AND s.id=NEW.source_page_id)
BEGIN SELECT RAISE(ABORT,'board source must belong to same workspace'); END;

CREATE TRIGGER board_empty_page_move BEFORE UPDATE OF page_id ON page_boards
WHEN NEW.page_id<>OLD.page_id AND (
 NOT EXISTS(SELECT 1 FROM pages WHERE id=NEW.page_id AND owner_user_id IS NOT NULL AND owner_crew_id IS NULL)
 OR EXISTS(SELECT 1 FROM page_panels WHERE page_id=NEW.page_id)
 OR EXISTS(SELECT 1 FROM page_project_drafts WHERE page_id=NEW.page_id)
 OR EXISTS(SELECT 1 FROM page_project_live WHERE page_id=NEW.page_id)
 OR EXISTS(SELECT 1 FROM page_artifacts WHERE page_id=NEW.page_id))
BEGIN SELECT RAISE(ABORT,'board requires an empty human-owned page'); END;

CREATE TRIGGER board_file_immutable BEFORE UPDATE OF board_page_id,sha256,content_type,width,height,byte_size ON page_board_files
BEGIN SELECT RAISE(ABORT,'board file is immutable'); END;

CREATE TRIGGER board_history_source_scope BEFORE INSERT ON page_board_source_history
WHEN NOT EXISTS(SELECT 1 FROM page_board_artifact_placements p WHERE p.id=NEW.placement_id AND p.board_page_id=NEW.board_page_id AND (NEW.source_page_id IS NULL OR p.source_page_id=NEW.source_page_id))
BEGIN SELECT RAISE(ABORT,'board source history must belong to the same placement source'); END;

CREATE TRIGGER board_history_source_immutable BEFORE UPDATE ON page_board_source_history
WHEN NEW.board_page_id IS NOT OLD.board_page_id OR NEW.mutation_id IS NOT OLD.mutation_id OR NEW.placement_id IS NOT OLD.placement_id
 OR (NEW.source_page_id IS NOT NULL AND (NEW.source_page_id IS NOT OLD.source_page_id OR NEW.source_revision IS NOT OLD.source_revision))
 OR (NEW.source_page_id IS NULL AND OLD.source_page_id IS NOT NULL AND EXISTS(SELECT 1 FROM page_artifact_revisions WHERE page_id=OLD.source_page_id AND revision=OLD.source_revision))
BEGIN SELECT RAISE(ABORT,'board source history is immutable'); END;

CREATE TRIGGER board_request_preview_immutable BEFORE UPDATE ON page_board_request_previews
BEGIN SELECT RAISE(ABORT,'immutable board request illustration'); END;

CREATE TRIGGER board_request_retry_scope BEFORE INSERT ON page_board_requests
WHEN NEW.retry_of_request_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM page_board_requests p WHERE p.id=NEW.retry_of_request_id AND p.workspace_id=NEW.workspace_id AND p.board_page_id IS NEW.board_page_id AND p.original_board_id=NEW.original_board_id AND p.human_user_id IS NEW.human_user_id AND p.kind=NEW.kind AND p.status IN('failed','cancelled'))
BEGIN SELECT RAISE(ABORT,'board retry parent scope mismatch'); END;

CREATE TRIGGER board_request_retry_immutable BEFORE UPDATE OF retry_of_request_id ON page_board_requests
WHEN NEW.retry_of_request_id IS NOT OLD.retry_of_request_id AND NOT(NEW.retry_of_request_id IS NULL AND NOT EXISTS(SELECT 1 FROM page_board_requests WHERE id=OLD.retry_of_request_id))
BEGIN SELECT RAISE(ABORT,'immutable board retry lineage'); END;

CREATE TRIGGER board_comment_history_immutable BEFORE UPDATE ON page_board_comment_history
WHEN NEW.id IS NOT OLD.id OR NEW.page_id IS NOT OLD.page_id OR NEW.thread_id IS NOT OLD.thread_id OR NEW.comment_id IS NOT OLD.comment_id OR NEW.version IS NOT OLD.version OR NEW.body IS NOT OLD.body OR NEW.action IS NOT OLD.action OR NEW.created_at IS NOT OLD.created_at
 OR (NEW.actor_user_id IS NOT OLD.actor_user_id AND NOT(NEW.actor_user_id IS NULL AND NOT EXISTS(SELECT 1 FROM users WHERE id=OLD.actor_user_id)))
BEGIN SELECT RAISE(ABORT,'immutable board comment history'); END;

CREATE TRIGGER board_comment_history_source BEFORE INSERT ON page_board_comment_history
WHEN NOT EXISTS(SELECT 1 FROM page_board_comments c JOIN page_board_threads t ON t.id=c.thread_id WHERE c.id=NEW.comment_id AND c.thread_id=NEW.thread_id AND t.page_id=NEW.page_id)
BEGIN SELECT RAISE(ABORT,'board comment history source mismatch'); END;

CREATE TRIGGER board_comment_mutation_source BEFORE INSERT ON page_board_comment_mutations
WHEN NOT EXISTS(SELECT 1 FROM page_board_threads t WHERE t.id=NEW.thread_id AND t.page_id=NEW.page_id)
 OR (NEW.comment_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM page_board_comments c WHERE c.id=NEW.comment_id AND c.thread_id=NEW.thread_id))
BEGIN SELECT RAISE(ABORT,'board comment mutation source mismatch'); END;

CREATE TRIGGER board_comment_mutation_immutable BEFORE UPDATE ON page_board_comment_mutations
WHEN NEW.id IS NOT OLD.id OR NEW.page_id IS NOT OLD.page_id OR NEW.thread_id IS NOT OLD.thread_id OR NEW.mutation_id IS NOT OLD.mutation_id OR NEW.request_digest IS NOT OLD.request_digest OR NEW.operation IS NOT OLD.operation OR NEW.result_version IS NOT OLD.result_version OR NEW.created_at IS NOT OLD.created_at OR NEW.reaction_emoji IS NOT OLD.reaction_emoji OR NEW.reaction_active IS NOT OLD.reaction_active
 OR (NEW.comment_id IS NOT OLD.comment_id AND NOT(NEW.comment_id IS NULL AND NOT EXISTS(SELECT 1 FROM page_board_comments WHERE id=OLD.comment_id)))
 OR (NEW.actor_user_id IS NOT OLD.actor_user_id AND NOT(NEW.actor_user_id IS NULL AND NOT EXISTS(SELECT 1 FROM users WHERE id=OLD.actor_user_id)))
BEGIN SELECT RAISE(ABORT,'immutable board comment mutation'); END;
