package main

// remainingExecutionDomainSchemaCatalog contains the handler-audited
// contracts which were still falling through to the generic object schema.
// It is intentionally a new, domain-specific catalog: keep additions to
// other audits out of this file and out of shared schema helpers.
func remainingExecutionDomainSchemaCatalog() map[string]DomainSchema {
	str := func() map[string]any { return map[string]any{"type": "string"} }
	integer := func() map[string]any { return map[string]any{"type": "integer"} }
	number := func() map[string]any { return map[string]any{"type": "number"} }
	boolean := func() map[string]any { return map[string]any{"type": "boolean"} }
	anyValue := func() map[string]any { return map[string]any{} }
	obj := func(p map[string]any) map[string]any { return map[string]any{"type": "object", "properties": p} }
	arr := func(i map[string]any) map[string]any { return map[string]any{"type": "array", "items": i} }
	nullable := func(s map[string]any) map[string]any {
		out := map[string]any{}
		for k, v := range s {
			out[k] = v
		}
		out["nullable"] = true
		return out
	}
	anyObject := func() map[string]any { return map[string]any{"type": "object", "additionalProperties": true} }

	template := obj(map[string]any{
		"id": str(), "workspace_id": str(), "name": str(), "description": nullable(str()),
		"template_json": anyValue(), "icon": nullable(str()), "color": nullable(str()),
		"is_builtin": boolean(), "created_at": str(), "updated_at": str(),
	})
	workflowTemplate := obj(map[string]any{
		"id": str(), "name": str(), "description": nullable(str()), "template_json": str(),
		"icon": nullable(str()), "color": nullable(str()), "is_builtin": boolean(),
		"created_at": str(), "updated_at": str(),
	})
	recipe := obj(map[string]any{"slug": str(), "name": str(), "description": str(), "icon": str(), "color": str(), "crew_slug": str(), "credentials": arr(anyObject()), "mcp_servers": arr(anyObject())})
	project := obj(map[string]any{"id": str(), "workspace_id": str(), "name": str(), "slug": str(), "description": nullable(str()), "icon": nullable(str()), "color": str(), "status": str(), "priority": str(), "health": str(), "lead_type": nullable(str()), "lead_id": nullable(str()), "lead_name": nullable(str()), "start_date": nullable(str()), "target_date": nullable(str()), "created_at": str(), "updated_at": str(), "issue_count": integer(), "done_count": integer(), "progress": integer()})
	checkpoint := obj(map[string]any{"id": str(), "workspace_id": str(), "crew_id": nullable(str()), "mission_id": str(), "label": nullable(str()), "journal_cursor": str(), "state": anyObject(), "fork_of": nullable(str()), "created_by": nullable(str()), "created_at": str()})
	cacheImage := obj(map[string]any{"tag": str(), "size": integer(), "created_at": integer(), "referenced_by": arr(str())})
	missionTask := obj(map[string]any{
		"id": str(), "mission_id": str(), "assigned_agent_id": nullable(str()),
		"agent_name": nullable(str()), "agent_slug": nullable(str()), "title": str(),
		"description": nullable(str()), "status": str(), "task_order": integer(),
		"depends_on": str(), "iteration": nullable(integer()), "max_iterations": nullable(integer()),
		"result_summary": nullable(str()), "output_path": nullable(str()), "error_message": nullable(str()),
		"assignment_id": nullable(str()), "token_count": nullable(integer()), "estimated_cost": nullable(number()),
		"started_at": nullable(str()), "completed_at": nullable(str()), "duration_ms": nullable(integer()),
		"created_at": str(), "updated_at": str(), "confidence": nullable(number()), "needs_review": boolean(),
		"handoff_context": nullable(str()), "evaluation_status": nullable(str()), "evaluation_notes": nullable(str()),
		"approval_required": boolean(), "approval_status": nullable(str()), "approved_by": nullable(str()), "approved_at": nullable(str()),
	})
	taskStats := obj(map[string]any{
		"total": integer(), "pending": integer(), "blocked": integer(), "in_progress": integer(),
		"completed": integer(), "failed": integer(), "skipped": integer(), "awaiting_approval": integer(),
	})
	mission := obj(map[string]any{
		"id": str(), "workspace_id": str(), "crew_id": str(), "lead_agent_id": str(),
		"lead_agent_name": str(), "lead_agent_slug": str(), "trace_id": str(), "title": str(),
		"description": nullable(str()), "status": str(), "plan": nullable(str()), "workflow_template": nullable(str()),
		"total_token_count": nullable(integer()), "total_estimated_cost": nullable(number()), "created_at": str(),
		"updated_at": str(), "completed_at": nullable(str()), "task_stats": nullable(taskStats), "tasks": arr(missionTask),
	})
	memoryDoc := obj(map[string]any{"path": str(), "tier": str(), "scope": str(), "title": str(), "tags": arr(str()), "sources": arr(str()), "body": str()})
	memoryEntry := anyObject()

	return map[string]DomainSchema{
		"GET /api/v1/recipes":                                              {Response: arr(recipe)},
		"GET /api/v1/recipes/{slug}":                                       {Response: recipe},
		"GET /api/v1/recipes/{slug}/preview":                               {Response: obj(map[string]any{"recipe": recipe, "needed_credentials": arr(str()), "existing_credentials": anyObject(), "crew_slug_available": boolean(), "resolved_crew_slug": str()})},
		"POST /api/v1/recipes/{slug}/install":                              {Request: obj(map[string]any{"credential_values": anyObject(), "account_labels": anyObject()}), Response: obj(map[string]any{"crew_id": str(), "crew_slug": str(), "credentials_added": arr(str()), "credentials_reused": arr(str()), "mcp_servers_added": arr(str())})},
		"POST /api/v1/projects":                                            {Request: obj(map[string]any{"name": str(), "description": nullable(str()), "icon": nullable(str()), "color": str(), "status": str(), "priority": str(), "lead_type": nullable(str()), "lead_id": nullable(str()), "start_date": nullable(str()), "target_date": nullable(str())}), Response: project},
		"PATCH /api/v1/projects/{projectId}":                               {Request: obj(map[string]any{"name": nullable(str()), "description": nullable(str()), "icon": nullable(str()), "color": nullable(str()), "status": nullable(str()), "priority": nullable(str()), "health": nullable(str()), "lead_type": nullable(str()), "lead_id": nullable(str()), "start_date": nullable(str()), "target_date": nullable(str())}), Response: project},
		"GET /api/v1/cache/images":                                         {Response: obj(map[string]any{"images": arr(cacheImage)})},
		"DELETE /api/v1/cache/images/{tag}":                                {Response: obj(map[string]any{"tag": str(), "status": str()})},
		"GET /api/v1/crews/{crewId}/capabilities":                          {Response: obj(map[string]any{"crew_id": str(), "crew_slug": str(), "container": anyObject(), "integrations": arr(anyObject()), "agents": arr(anyObject()), "runtimes": anyObject(), "schema": anyObject()})},
		"GET /api/v1/templates":                                            {Response: arr(template)},
		"GET /api/v1/templates/{templateId}":                               {Response: template},
		"POST /api/v1/templates":                                           {Request: obj(map[string]any{"name": str(), "description": nullable(str()), "template_json": anyValue(), "icon": nullable(str()), "color": nullable(str())}), Response: obj(map[string]any{"id": str()})},
		"PATCH /api/v1/templates/{templateId}":                             {Request: obj(map[string]any{"name": nullable(str()), "description": nullable(str()), "template_json": anyValue(), "icon": nullable(str()), "color": nullable(str())}), Response: obj(map[string]any{"id": str()})},
		"DELETE /api/v1/templates/{templateId}":                            {Response: nil},
		"GET /api/v1/workflow-templates":                                   {Response: arr(workflowTemplate)},
		"GET /api/v1/workflow-templates/{id}":                              {Response: workflowTemplate},
		"POST /api/v1/workflow-templates":                                  {Request: obj(map[string]any{"name": str(), "description": nullable(str()), "template_json": str(), "icon": nullable(str()), "color": nullable(str())}), Response: workflowTemplate},
		"PATCH /api/v1/workflow-templates/{id}":                            {Request: obj(map[string]any{"name": nullable(str()), "description": nullable(str()), "template_json": nullable(str()), "icon": nullable(str()), "color": nullable(str())}), Response: workflowTemplate},
		"DELETE /api/v1/workflow-templates/{id}":                           {Response: nil},
		"GET /api/v1/missions":                                             {Response: arr(mission)},
		"GET /api/v1/crews/{crewId}/missions":                              {Response: arr(mission)},
		"GET /api/v1/crews/{crewId}/missions/{missionId}":                  {Response: mission},
		"POST /api/v1/crews/{crewId}/missions":                             {Request: obj(map[string]any{"title": str(), "description": nullable(str()), "lead_agent_id": str(), "workflow_template": nullable(str())}), Response: mission},
		"PATCH /api/v1/crews/{crewId}/missions/{missionId}":                {Request: obj(map[string]any{"status": nullable(str()), "title": nullable(str()), "description": nullable(str()), "plan": nullable(str())}), Response: mission},
		"POST /api/v1/crews/{crewId}/missions/{missionId}/start":           {Response: obj(map[string]any{"id": str(), "status": str()})},
		"POST /api/v1/crews/{crewId}/missions/{missionId}/restart":         {Response: obj(map[string]any{"id": str(), "status": str()})},
		"POST /api/v1/crews/{crewId}/missions/{missionId}/resume":          {Response: obj(map[string]any{"id": str(), "status": str(), "reset_tasks": integer()})},
		"POST /api/v1/crews/{crewId}/missions/{missionId}/clone":           {Response: obj(map[string]any{"id": str(), "status": str()})},
		"DELETE /api/v1/crews/{crewId}/missions/{missionId}":               {Response: nil},
		"GET /api/v1/mission-metrics":                                      {Response: obj(map[string]any{"total_missions": integer(), "active_missions": integer(), "completed_24h": integer(), "failed_24h": integer(), "total_tokens_24h": integer(), "total_cost_24h": number(), "avg_completion_time_ms": integer(), "tasks_completed_24h": integer(), "tasks_failed_24h": integer()})},
		"POST /api/v1/crews/{crewId}/missions/{missionId}/tasks":           {Request: obj(map[string]any{"title": str(), "description": nullable(str()), "assigned_agent_id": nullable(str()), "task_order": integer(), "depends_on": arr(str()), "max_iterations": nullable(integer())}), Response: missionTask},
		"PATCH /api/v1/crews/{crewId}/missions/{missionId}/tasks/{taskId}": {Request: obj(map[string]any{"status": nullable(str()), "title": nullable(str()), "description": nullable(str()), "depends_on": nullable(str()), "assigned_agent_id": nullable(str()), "result_summary": nullable(str()), "error_message": nullable(str()), "output_path": nullable(str()), "token_count": nullable(integer()), "estimated_cost": nullable(number()), "max_iterations": nullable(integer())}), Response: missionTask},
		"GET /api/v1/missions/{missionId}/checkpoints":                     {Response: obj(map[string]any{"checkpoints": arr(checkpoint), "count": integer(), "mission_id": str()})},
		"GET /api/v1/checkpoints/{id}":                                     {Response: checkpoint},
		"POST /api/v1/checkpoints/{id}/restore":                            {Response: obj(map[string]any{"checkpoint": checkpoint, "journal_cursor": str(), "warn_divergence": arr(str())})},
		"POST /api/v1/checkpoints/{id}/fork":                               {Request: obj(map[string]any{"label": str()}), Response: obj(map[string]any{"new_mission_id": str(), "new_checkpoint_id": str()})},
		"DELETE /api/v1/checkpoints/{id}":                                  {Response: nil},
		"GET /api/v1/memory/export":                                        {Response: obj(map[string]any{"format": str(), "documents": arr(memoryDoc), "skipped": arr(obj(map[string]any{"source": str(), "reason": str()}))})},
		"POST /api/v1/memory/import":                                       {Request: obj(map[string]any{"crew_id": str(), "agent_slug": str(), "documents": arr(memoryDoc)}), Response: anyObject()},
		"POST /api/v1/memory/search/hybrid":                                {Request: obj(map[string]any{"query": str(), "limit": integer(), "scope": str(), "crew_id": str()}), Response: obj(map[string]any{"query": str(), "count": integer(), "hits": arr(memoryEntry)})},
		"GET /api/v1/memory/versions":                                      {Response: obj(map[string]any{"path": str(), "count": integer(), "entries": arr(memoryEntry)})},
		"POST /api/v1/memory/versions/{sha}/restore":                       {Request: obj(map[string]any{"path": str(), "canonical_path": str(), "tier": str()}), Response: obj(map[string]any{"workspace_id": str(), "path": str(), "canonical_path": str(), "restored_sha": str(), "new_version_id": str(), "bytes": integer(), "restored_by": str()})},
		"POST /api/v1/agents/{agentId}/skills":                             {Request: obj(map[string]any{"skill_id": str(), "config": nullable(str())}), Response: obj(map[string]any{"id": str()})},
		"GET /api/v1/agents/{agentId}/skills":                              {Response: arr(obj(map[string]any{"id": str(), "agent_id": str(), "skill_id": str(), "enabled": boolean(), "config": nullable(str()), "skill": anyObject()}))},
		"POST /api/v1/skills/proposed/approve":                             {Request: obj(map[string]any{"crew_id": str(), "file_name": str()}), Response: obj(map[string]any{"skill_id": str(), "slug": str(), "created": boolean(), "file_name": str()})},
		"POST /api/v1/skills/proposed/reject":                              {Request: obj(map[string]any{"crew_id": str(), "file_name": str()}), Response: obj(map[string]any{"file_name": str(), "removed": boolean()})},
		"POST /api/v1/workspaces/{workspaceId}/skills/bulk-import":         {Request: obj(map[string]any{"git_url": str(), "git_ref": str(), "paths": arr(str()), "vendor": str(), "allow_unsafe_license": boolean(), "dry_run": boolean()}), Response: obj(map[string]any{"source": str(), "total_found": integer(), "total_imported": integer(), "imported": arr(obj(map[string]any{"skill_id": str(), "slug": str(), "created": boolean()})), "skipped": arr(obj(map[string]any{"path": str(), "slug": str(), "reason": str()})), "truncated": boolean()})},
		"GET /api/v1/admin/legacy-resources":                               {Response: anyObject()},
		"POST /api/v1/admin/prune-legacy-resources":                        {Response: anyObject()},
	}
}
