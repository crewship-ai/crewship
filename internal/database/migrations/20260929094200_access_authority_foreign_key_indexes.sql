-- Keep revocation/resource deletion from scanning all authority rows while
-- holding the SQLite writer lock. The member-first unique indexes do not cover
-- agent/project FK lookups. Preserve the already-deployed authority migrations.
CREATE INDEX access_attempts_agent ON access_attempts(agent_id);
CREATE INDEX access_attempts_principal ON access_attempts(principal_id);
CREATE INDEX access_attempts_workspace ON access_attempts(workspace_id);
CREATE INDEX access_grants_agent ON access_grants(agent_id);
CREATE INDEX access_grants_project ON access_grants(project_id);
CREATE INDEX access_grants_creator ON access_grants(created_by);
