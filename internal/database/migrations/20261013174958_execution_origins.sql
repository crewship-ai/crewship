-- Durable human authority survives parent completion, queues and cron delays.
-- chat_id is historical: deleting a chat must revoke authority, not erase its
-- origin and turn dependent work into autonomous work.
CREATE TABLE execution_origins (
 id TEXT PRIMARY KEY,
 workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 root_run_id TEXT NOT NULL,
 chat_id TEXT NOT NULL,
 trigger_message_id TEXT NOT NULL,
 human_authority TEXT NOT NULL CHECK(json_valid(human_authority)),
 created_at TEXT NOT NULL,
 UNIQUE(workspace_id, root_run_id)
);
CREATE TRIGGER execution_origins_immutable BEFORE UPDATE ON execution_origins
BEGIN SELECT RAISE(ABORT, 'execution origins are immutable'); END;

-- NULL means genuinely autonomous. A present invalid origin must fail closed.
ALTER TABLE assignments ADD COLUMN chat_origin_id TEXT REFERENCES execution_origins(id);
ALTER TABLE missions ADD COLUMN chat_origin_id TEXT REFERENCES execution_origins(id);
ALTER TABLE pipelines ADD COLUMN chat_origin_id TEXT REFERENCES execution_origins(id);
ALTER TABLE pipeline_versions ADD COLUMN chat_origin_id TEXT REFERENCES execution_origins(id);
ALTER TABLE pipeline_schedules ADD COLUMN chat_origin_id TEXT REFERENCES execution_origins(id);
ALTER TABLE pipeline_runs ADD COLUMN chat_origin_id TEXT REFERENCES execution_origins(id);
ALTER TABLE pending_runs ADD COLUMN chat_origin_id TEXT REFERENCES execution_origins(id);
ALTER TABLE pipeline_drafts ADD COLUMN chat_origin_id TEXT REFERENCES execution_origins(id);
ALTER TABLE mission_comments ADD COLUMN chat_origin_id TEXT REFERENCES execution_origins(id);

-- One-time upgrade fence: legacy queued work has no trustworthy human/autonomous
-- provenance. Preserve its records and status, but require fresh authorization
-- before it may execute. An immutable invalid receipt cannot be mistaken for
-- NULL (genuine autonomy), including after recovery or parent completion.
INSERT INTO execution_origins(id,workspace_id,root_run_id,chat_id,trigger_message_id,human_authority,created_at)
SELECT 'c'||lower(hex(randomblob(16))),w.id,
       'legacy-unverified-delegation:20261013162000','legacy-unverified',
       'legacy-unverified','{}',strftime('%Y-%m-%dT%H:%M:%fZ','now')
FROM workspaces w
WHERE EXISTS(SELECT 1 FROM assignments a WHERE a.workspace_id=w.id AND a.status IN ('PENDING','QUEUED','RUNNING'))
   OR EXISTS(SELECT 1 FROM missions m WHERE m.workspace_id=w.id AND m.mission_type='orchestration' AND m.status NOT IN ('COMPLETED','FAILED','CANCELLED'))
   OR EXISTS(SELECT 1 FROM pending_runs p WHERE p.workspace_id=w.id AND p.status IN ('pending','fired') AND EXISTS(SELECT 1 FROM journal_entries j WHERE j.workspace_id=p.workspace_id
 AND j.entry_type='run.started' AND j.trace_id IN (p.chain_origin,p.triggered_by_id)
 AND CASE WHEN json_valid(j.payload) THEN COALESCE(json_extract(j.payload,'$.human_authority.chat_id'),'') ELSE '' END<>''
 AND CASE WHEN json_valid(j.payload) THEN COALESCE(json_extract(j.payload,'$.trigger_message_id'),'') ELSE '' END<>''))
   OR EXISTS(SELECT 1 FROM pipeline_runs r WHERE r.workspace_id=w.id AND r.status IN ('queued','running','waiting','interrupted') AND EXISTS(SELECT 1 FROM journal_entries j WHERE j.workspace_id=r.workspace_id
 AND j.entry_type='run.started' AND j.trace_id IN (r.chain_origin,r.triggered_by_id)
 AND CASE WHEN json_valid(j.payload) THEN COALESCE(json_extract(j.payload,'$.human_authority.chat_id'),'') ELSE '' END<>''
 AND CASE WHEN json_valid(j.payload) THEN COALESCE(json_extract(j.payload,'$.trigger_message_id'),'') ELSE '' END<>''));

UPDATE assignments SET chat_origin_id=(
 SELECT o.id FROM execution_origins o WHERE o.workspace_id=assignments.workspace_id
 AND o.root_run_id='legacy-unverified-delegation:20261013162000'
)
WHERE status IN ('PENDING','QUEUED','RUNNING');

UPDATE missions SET chat_origin_id=(
 SELECT o.id FROM execution_origins o WHERE o.workspace_id=missions.workspace_id
 AND o.root_run_id='legacy-unverified-delegation:20261013162000'
)
WHERE mission_type='orchestration' AND status NOT IN ('COMPLETED','FAILED','CANCELLED');

-- Standing recipe grants keep their existing invocation policy. Only durable
-- journal evidence identifies legacy chat-human pipeline chains; user IDs or
-- recipe authorship alone do not establish that authority.
UPDATE pending_runs SET chat_origin_id=(
 SELECT o.id FROM execution_origins o WHERE o.workspace_id=pending_runs.workspace_id
 AND o.root_run_id='legacy-unverified-delegation:20261013162000'
)
WHERE chat_origin_id IS NULL AND status IN ('pending','fired') AND EXISTS(SELECT 1 FROM journal_entries j WHERE j.workspace_id=pending_runs.workspace_id
 AND j.entry_type='run.started' AND j.trace_id IN (pending_runs.chain_origin,pending_runs.triggered_by_id)
 AND CASE WHEN json_valid(j.payload) THEN COALESCE(json_extract(j.payload,'$.human_authority.chat_id'),'') ELSE '' END<>''
 AND CASE WHEN json_valid(j.payload) THEN COALESCE(json_extract(j.payload,'$.trigger_message_id'),'') ELSE '' END<>'');

UPDATE pipeline_runs SET chat_origin_id=(
 SELECT o.id FROM execution_origins o WHERE o.workspace_id=pipeline_runs.workspace_id
 AND o.root_run_id='legacy-unverified-delegation:20261013162000'
)
WHERE chat_origin_id IS NULL AND status IN ('queued','running','waiting','interrupted') AND EXISTS(SELECT 1 FROM journal_entries j WHERE j.workspace_id=pipeline_runs.workspace_id
 AND j.entry_type='run.started' AND j.trace_id IN (pipeline_runs.chain_origin,pipeline_runs.triggered_by_id)
 AND CASE WHEN json_valid(j.payload) THEN COALESCE(json_extract(j.payload,'$.human_authority.chat_id'),'') ELSE '' END<>''
 AND CASE WHEN json_valid(j.payload) THEN COALESCE(json_extract(j.payload,'$.trigger_message_id'),'') ELSE '' END<>'');
