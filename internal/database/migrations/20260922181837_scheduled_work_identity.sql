-- Automatic cron acceptance has one logical work per agent and due instant.
-- Explicit replay remains a distinct authorization/work identity.
CREATE UNIQUE INDEX idx_work_schedule_occurrence
ON work_items(workspace_id, agent_id, source_ref)
WHERE source = 'schedule' AND domain_kind = 'agent_run'
  AND source_ref != '' AND replay_of IS NULL;
