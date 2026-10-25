-- Historical attribution DATA. These rows never grant or restore authority.
-- Identity columns deliberately do not cascade on deleting a user/chat/run.
CREATE TABLE mooring_collab_artifact_edits (
  page_id TEXT NOT NULL REFERENCES pages(id) ON DELETE CASCADE,
  workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
  application_id TEXT NOT NULL,
  change_id TEXT NOT NULL,
  membership_hash TEXT NOT NULL,
  first_revision TEXT NOT NULL,
  last_revision TEXT NOT NULL,
  host_session_id TEXT NOT NULL,
  actor_kind TEXT NOT NULL CHECK (actor_kind IN ('human','agent')),
  human_user_id TEXT NOT NULL CHECK (length(human_user_id)>0),
  agent_id TEXT,
  crew_id TEXT,
  chat_id TEXT,
  message_id TEXT,
  work_id TEXT,
  run_id TEXT,
  attempt_number INTEGER,
  attempt_generation INTEGER,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  PRIMARY KEY (page_id,change_id),
  UNIQUE (page_id,last_revision),
  CHECK (length(change_id)>0 AND length(membership_hash)>0 AND length(host_session_id)>0),
  CHECK (length(first_revision) BETWEEN 1 AND 20 AND first_revision NOT GLOB '*[^0-9]*' AND substr(first_revision,1,1)!='0'),
  CHECK (length(last_revision) BETWEEN 1 AND 20 AND last_revision NOT GLOB '*[^0-9]*' AND substr(last_revision,1,1)!='0'),
  CHECK (length(first_revision)<length(last_revision) OR (length(first_revision)=length(last_revision) AND first_revision<=last_revision COLLATE BINARY)),
  CHECK ((actor_kind='human' AND agent_id IS NULL AND crew_id IS NULL AND chat_id IS NULL AND message_id IS NULL AND work_id IS NULL AND run_id IS NULL AND attempt_number IS NULL AND attempt_generation IS NULL)
    OR (actor_kind='agent' AND length(COALESCE(agent_id,''))>0 AND length(COALESCE(crew_id,''))>0 AND length(COALESCE(chat_id,''))>0 AND length(COALESCE(message_id,''))>0 AND length(COALESCE(work_id,''))>0 AND length(COALESCE(run_id,''))>0 AND COALESCE(attempt_number,0)>0 AND COALESCE(attempt_generation,0)>0))
);
CREATE INDEX mooring_collab_artifact_edits_workspace ON mooring_collab_artifact_edits(workspace_id);
CREATE INDEX mooring_collab_artifact_edits_chat ON mooring_collab_artifact_edits(workspace_id,chat_id,created_at);
CREATE INDEX mooring_collab_artifact_edits_agent ON mooring_collab_artifact_edits(workspace_id,agent_id,created_at);
CREATE INDEX mooring_collab_artifact_edits_crew ON mooring_collab_artifact_edits(workspace_id,crew_id,created_at);
CREATE TRIGGER mooring_artifact_edit_scope BEFORE INSERT ON mooring_collab_artifact_edits
 WHEN NOT EXISTS (SELECT 1 FROM mooring_collab_documents d WHERE d.page_id=NEW.page_id AND d.workspace_id=NEW.workspace_id AND d.application_id=NEW.application_id)
 BEGIN SELECT RAISE(ABORT,'artifact edit scope mismatch'); END;
CREATE TRIGGER mooring_artifact_edit_immutable BEFORE UPDATE ON mooring_collab_artifact_edits
 BEGIN SELECT RAISE(ABORT,'artifact edit attribution is immutable'); END;
