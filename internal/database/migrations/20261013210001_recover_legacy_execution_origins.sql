-- Recover only durable positive provenance; never mint a fresh human epoch.
-- The previous upgrade sentinel remains for missing, conflicting or malformed
-- evidence. Completed parents are valid historical sources: live receipt checks
-- still happen when the child executes.
CREATE TEMP TABLE legacy_origin_candidates(table_name TEXT,row_id TEXT,workspace_id TEXT,root_run_id TEXT);
INSERT INTO legacy_origin_candidates SELECT 'assignments',id,workspace_id,COALESCE(NULLIF(chain_origin,''),NULLIF(parent_run_id,''),'') FROM assignments WHERE status IN ('PENDING','QUEUED','RUNNING') AND chat_origin_id IN (SELECT id FROM execution_origins WHERE root_run_id='legacy-unverified-delegation:20261013162000');
INSERT INTO legacy_origin_candidates SELECT 'missions',id,workspace_id,trace_id FROM missions WHERE mission_type='orchestration' AND status NOT IN ('COMPLETED','FAILED','CANCELLED') AND chat_origin_id IN (SELECT id FROM execution_origins WHERE root_run_id='legacy-unverified-delegation:20261013162000');
INSERT INTO legacy_origin_candidates SELECT 'pending_runs',id,workspace_id,COALESCE(NULLIF(chain_origin,''),NULLIF(triggered_by_id,''),'') FROM pending_runs WHERE status IN ('pending','fired') AND (chat_origin_id IS NULL OR chat_origin_id IN (SELECT id FROM execution_origins WHERE root_run_id='legacy-unverified-delegation:20261013162000'));
INSERT INTO legacy_origin_candidates SELECT 'pipeline_runs',id,workspace_id,COALESCE(NULLIF(chain_origin,''),NULLIF(triggered_by_id,''),id) FROM pipeline_runs WHERE status IN ('queued','running','waiting','interrupted') AND (chat_origin_id IS NULL OR chat_origin_id IN (SELECT id FROM execution_origins WHERE root_run_id='legacy-unverified-delegation:20261013162000'));

-- An indirect pipeline chain can be recovered only when it terminates within
-- the bound. Repeated nodes and excessive depth yield no proof, hence a fence.
CREATE TEMP TABLE legacy_origin_roots AS
WITH RECURSIVE walk(table_name,row_id,workspace_id,root_run_id,depth,path) AS (
 SELECT table_name,row_id,workspace_id,root_run_id,0,'|'||root_run_id||'|' FROM legacy_origin_candidates
 UNION ALL
 SELECT w.table_name,w.row_id,w.workspace_id,COALESCE(NULLIF(p.chain_origin,''),NULLIF(p.triggered_by_id,''),''),w.depth+1,
 w.path||COALESCE(NULLIF(p.chain_origin,''),NULLIF(p.triggered_by_id,''),'')||'|'
 FROM walk w JOIN pipeline_runs p ON p.id=w.root_run_id AND p.workspace_id=w.workspace_id
 WHERE w.depth<16 AND COALESCE(NULLIF(p.chain_origin,''),NULLIF(p.triggered_by_id,''),'')<>''
 AND instr(w.path,'|'||COALESCE(NULLIF(p.chain_origin,''),NULLIF(p.triggered_by_id,''),'')||'|')=0
 AND NOT EXISTS(SELECT 1 FROM journal_entries j WHERE j.workspace_id=w.workspace_id AND j.trace_id=w.root_run_id AND j.entry_type='run.started')
)
SELECT w.* FROM walk w
WHERE EXISTS(SELECT 1 FROM journal_entries j WHERE j.workspace_id=w.workspace_id AND j.trace_id=w.root_run_id AND j.entry_type='run.started');
CREATE TEMP TABLE legacy_origin_proofs AS
SELECT r.table_name,r.row_id,r.workspace_id,r.root_run_id,
 MAX(CASE WHEN json_valid(j.payload) THEN j.payload ELSE '{}' END) AS payload,MAX(j.actor_type) AS actor_type,MAX(j.agent_id) AS agent_id,COUNT(*) AS starts
FROM legacy_origin_roots r JOIN journal_entries j ON j.workspace_id=r.workspace_id AND j.trace_id=r.root_run_id AND j.entry_type='run.started'
GROUP BY r.table_name,r.row_id,r.workspace_id;

-- Retain the exact historical receipt, including its original membership and
-- chat epochs. A stale or forged receipt still fails ValidateOriginReceipt.
INSERT INTO execution_origins(id,workspace_id,root_run_id,chat_id,trigger_message_id,human_authority,created_at)
SELECT 'c'||lower(hex(randomblob(16))),workspace_id,root_run_id,
 json_extract(payload,'$.chat_id'),json_extract(payload,'$.trigger_message_id'),json_extract(payload,'$.human_authority'),strftime('%Y-%m-%dT%H:%M:%fZ','now')
FROM legacy_origin_proofs
WHERE starts=1 AND json_valid(payload)
 AND (json_type(payload,'$.requester_authority_missing') IS NULL OR json_type(payload,'$.requester_authority_missing')='false')
 AND (actor_type='orchestrator' OR json_extract(payload,'$.host_admitted')=1)
 AND json_type(payload,'$.human_authority')='object'
 AND COALESCE(json_extract(payload,'$.chat_id'),'')<>''
 AND COALESCE(json_extract(payload,'$.trigger_message_id'),'')<>''
 AND json_extract(payload,'$.human_authority.chat_id')=json_extract(payload,'$.chat_id')
 AND json_extract(payload,'$.human_authority.workspace_id')=workspace_id
 AND COALESCE(json_extract(payload,'$.human_authority.user_id'),'')<>''
 AND COALESCE(json_extract(payload,'$.human_authority.agent_id'),'')<>''
 AND json_extract(payload,'$.human_authority.agent_id')=agent_id
 AND COALESCE(json_extract(payload,'$.chat_origin_id'),'')=''
 AND COALESCE(json_extract(payload,'$.human_authority.member_id'),'')<>''
 AND COALESCE(json_extract(payload,'$.human_authority.chat_generation'),'')<>''
 AND json_extract(payload,'$.human_authority.member_revision')>=1
 AND json_extract(payload,'$.human_authority.chat_revision')>=1
ON CONFLICT(workspace_id,root_run_id) DO NOTHING;

INSERT INTO execution_origins(id,workspace_id,root_run_id,chat_id,trigger_message_id,human_authority,created_at)
SELECT 'c'||lower(hex(randomblob(16))),workspace_id,'legacy-unverified-delegation:20261013162000','legacy-unverified','legacy-unverified','{}',strftime('%Y-%m-%dT%H:%M:%fZ','now')
FROM legacy_origin_candidates GROUP BY workspace_id
ON CONFLICT(workspace_id,root_run_id) DO NOTHING;

CREATE TEMP TABLE legacy_origin_results AS
SELECT c.table_name,c.row_id,
 CASE WHEN p.starts=1 AND json_valid(p.payload)
 AND (json_type(p.payload,'$.requester_authority_missing') IS NULL OR json_type(p.payload,'$.requester_authority_missing')='false')
 AND json_type(p.payload,'$.host_admitted')='true'
 AND COALESCE(json_extract(p.payload,'$.chat_origin_id'),'')=''
 AND json_type(p.payload,'$.human_authority') IS NULL
 AND json_extract(p.payload,'$.trigger_type') IN ('SCHEDULED','ASSIGNMENT','WEBHOOK','ROUTINE','CRON','PEER_QUERY')
 THEN NULL
 ELSE COALESCE(
 (SELECT o.id FROM execution_origins o WHERE p.starts=1 AND (p.actor_type='orchestrator' OR json_extract(p.payload,'$.host_admitted')=1)
 AND (json_type(p.payload,'$.requester_authority_missing') IS NULL OR json_type(p.payload,'$.requester_authority_missing')='false')
 AND o.workspace_id=c.workspace_id AND o.root_run_id=p.root_run_id
 AND o.chat_id=json_extract(p.payload,'$.chat_id') AND o.trigger_message_id=json_extract(p.payload,'$.trigger_message_id')
 AND o.human_authority=json_extract(p.payload,'$.human_authority')),
 (SELECT o.id FROM execution_origins o WHERE o.workspace_id=c.workspace_id AND o.root_run_id='legacy-unverified-delegation:20261013162000'))
 END AS origin_id
FROM legacy_origin_candidates c LEFT JOIN legacy_origin_proofs p ON p.table_name=c.table_name AND p.row_id=c.row_id;
UPDATE assignments SET chat_origin_id=(SELECT origin_id FROM legacy_origin_results r WHERE r.table_name='assignments' AND r.row_id=assignments.id) WHERE id IN (SELECT row_id FROM legacy_origin_results WHERE table_name='assignments');
UPDATE missions SET chat_origin_id=(SELECT origin_id FROM legacy_origin_results r WHERE r.table_name='missions' AND r.row_id=missions.id) WHERE id IN (SELECT row_id FROM legacy_origin_results WHERE table_name='missions');
UPDATE pending_runs SET chat_origin_id=(SELECT origin_id FROM legacy_origin_results r WHERE r.table_name='pending_runs' AND r.row_id=pending_runs.id) WHERE id IN (SELECT row_id FROM legacy_origin_results WHERE table_name='pending_runs');
UPDATE pipeline_runs SET chat_origin_id=(SELECT origin_id FROM legacy_origin_results r WHERE r.table_name='pipeline_runs' AND r.row_id=pipeline_runs.id) WHERE id IN (SELECT row_id FROM legacy_origin_results WHERE table_name='pipeline_runs');
DROP TABLE legacy_origin_results;
DROP TABLE legacy_origin_proofs;
DROP TABLE legacy_origin_roots;
DROP TABLE legacy_origin_candidates;
