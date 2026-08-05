package main

// finalSpecialDomainSchemaCatalog is the last response-contract audit for
// handlers whose wire shapes are private structs or literal maps.  Keep this
// catalog separate from the earlier domain audits: the file name is unique so
// later merges cannot silently replace a sibling catalog.
func finalSpecialDomainSchemaCatalog() map[string]DomainSchema {
	str := func() map[string]any { return map[string]any{"type": "string"} }
	integer := func() map[string]any { return map[string]any{"type": "integer"} }
	number := func() map[string]any { return map[string]any{"type": "number"} }
	boolean := func() map[string]any { return map[string]any{"type": "boolean"} }
	anyValue := func() map[string]any { return map[string]any{} }
	nullable := func(s map[string]any) map[string]any {
		out := map[string]any{}
		for k, v := range s {
			out[k] = v
		}
		out["nullable"] = true
		return out
	}
	object := func(properties map[string]any) map[string]any {
		return map[string]any{"type": "object", "properties": properties}
	}
	array := func(items map[string]any) map[string]any {
		return map[string]any{"type": "array", "items": items}
	}
	stringArray := func() map[string]any { return array(str()) }

	connection := object(map[string]any{
		"id": str(), "workspace_id": str(), "from_crew_id": str(), "from_crew_name": nullable(str()),
		"from_crew_slug": nullable(str()), "to_crew_id": str(), "to_crew_name": nullable(str()),
		"to_crew_slug": nullable(str()), "direction": str(), "status": str(),
		"created_at": str(), "updated_at": str(),
	})
	crewTemplateAgent := object(map[string]any{
		"name": str(), "slug": str(), "role_title": str(), "agent_role": str(),
		"cli_adapter": str(), "llm_provider": str(), "llm_model": str(), "tool_profile": str(),
		"system_prompt": str(), "skills": stringArray(),
	})
	crewTemplate := object(map[string]any{
		"id": str(), "name": str(), "slug": str(), "description": nullable(str()), "icon": nullable(str()),
		"color": nullable(str()), "category": str(), "agents": array(crewTemplateAgent),
		"is_builtin": boolean(), "created_at": str(),
	})
	template := object(map[string]any{
		"id": str(), "workspace_id": str(), "name": str(), "description": nullable(str()),
		"template_json": anyValue(), "icon": nullable(str()), "color": nullable(str()),
		"is_builtin": boolean(), "created_at": str(), "updated_at": str(),
	})
	workflowTemplate := object(map[string]any{
		"id": str(), "name": str(), "description": nullable(str()), "template_json": str(),
		"icon": nullable(str()), "color": nullable(str()), "is_builtin": boolean(),
		"created_at": str(), "updated_at": str(),
	})
	mcpServer := object(map[string]any{
		"id": str(), "name": str(), "display_name": str(), "description": str(), "icon": str(),
		"transport": str(), "homepage_url": str(), "source_url": str(), "package_name": str(),
		"package_registry": str(), "command": str(), "endpoint": str(), "auth_type": str(),
		"env_vars_json": str(), "category": str(), "is_verified": boolean(), "trust_tier": str(),
		"is_featured": boolean(), "synced_at": str(),
	})
	mcpToolCall := object(map[string]any{
		"id": str(), "workspace_id": str(), "crew_id": nullable(str()), "agent_id": str(),
		"mcp_server_id": str(), "mcp_server_scope": str(), "tool_name": str(), "input_hash": nullable(str()),
		"status": str(), "duration_ms": nullable(integer()), "error_message": nullable(str()), "created_at": str(),
	})
	evalRun := object(map[string]any{
		"id": str(), "workspace_id": str(), "kind": str(), "mission_id": str(),
		"baseline_mission_id": str(), "candidate_mission_id": str(), "status": str(), "result": str(),
		"seed": integer(), "signature": str(), "total_tokens": integer(), "total_cost_usd": number(),
		"regressed": boolean(), "created_by": str(), "created_at": str(), "completed_at": nullable(str()),
	})
	evalQueued := object(map[string]any{"run_id": str(), "status": str()})
	proposal := object(map[string]any{
		"proposal_id": str(), "workspace_id": str(), "crew_id": str(), "status": str(), "proposal_path": str(),
		"rules_count": integer(), "entries_scanned": integer(), "created_at": str(), "decided_at": nullable(str()),
		"decided_by_user_id": nullable(str()), "evidence": anyValue(), "scores": anyValue(),
	})
	memoryVersion := object(map[string]any{
		"id": str(), "path": str(), "tier": str(), "sha256": str(), "bytes": integer(), "written_at": str(),
		"written_by": str(), "parent_sha": nullable(str()),
	})
	memoryHit := object(map[string]any{
		"source": str(), "score": number(),
		"fts":      nullable(object(map[string]any{"file": str(), "line_start": integer(), "line_end": integer(), "snippet": str(), "score": number()})),
		"episodic": nullable(object(map[string]any{"entry_id": str(), "score": number(), "age": integer(), "summary": str(), "entry_type": str(), "agent_id": str(), "payload": anyValue()})),
	})
	memoryDocument := object(map[string]any{"path": str(), "tier": str(), "scope": str(), "title": str(), "tags": stringArray(), "sources": stringArray(), "body": str()})
	proposedSkill := object(map[string]any{"file_name": str(), "name": str(), "description": str(), "description_quality": str(), "category": str()})
	aiAgent := object(map[string]any{"name": str(), "slug": str(), "role_title": str(), "agent_role": str(), "system_prompt": str()})
	aiSuggestion := object(map[string]any{"crew_name": str(), "crew_slug": str(), "description": str(), "agents": array(aiAgent)})

	return map[string]DomainSchema{
		"GET /api/v1/crew-connections":                   {Response: array(connection)},
		"POST /api/v1/crew-connections":                  {Request: object(map[string]any{"from_crew_id": str(), "to_crew_id": str(), "direction": str()}), Response: object(map[string]any{"id": str()})},
		"DELETE /api/v1/crew-connections/{connectionId}": {Response: nil},
		"GET /api/v1/crew-templates":                     {Response: array(crewTemplate)},
		"GET /api/v1/crew-templates/{slug}":              {Response: crewTemplate},
		"POST /api/v1/crew-templates/{slug}/deploy":      {Request: object(map[string]any{"crew_name": str(), "crew_slug": str()}), Response: object(map[string]any{"crew_id": str(), "crew_name": str(), "crew_slug": str(), "agent_count": integer(), "agent_ids": stringArray()})},
		"GET /api/v1/templates":                          {Response: array(template)},
		"GET /api/v1/templates/{templateId}":             {Response: template},
		"POST /api/v1/templates":                         {Request: object(map[string]any{"name": str(), "description": nullable(str()), "template_json": anyValue(), "icon": nullable(str()), "color": nullable(str())}), Response: object(map[string]any{"id": str()})},
		"PATCH /api/v1/templates/{templateId}":           {Request: object(map[string]any{"name": nullable(str()), "description": nullable(str()), "template_json": anyValue(), "icon": nullable(str()), "color": nullable(str())}), Response: object(map[string]any{"id": str()})},
		"DELETE /api/v1/templates/{templateId}":          {Response: object(map[string]any{"id": str()})},
		"GET /api/v1/workflow-templates":                 {Response: array(workflowTemplate)},
		"GET /api/v1/workflow-templates/{id}":            {Response: workflowTemplate},
		"POST /api/v1/workflow-templates":                {Request: object(map[string]any{"name": str(), "description": nullable(str()), "template_json": str(), "icon": nullable(str()), "color": nullable(str())}), Response: workflowTemplate},
		"PATCH /api/v1/workflow-templates/{id}":          {Request: object(map[string]any{"name": nullable(str()), "description": nullable(str()), "template_json": nullable(str()), "icon": nullable(str()), "color": nullable(str())}), Response: workflowTemplate},
		"DELETE /api/v1/workflow-templates/{id}":         {Response: object(map[string]any{"id": str()})},
		"POST /api/v1/consolidate/run":                   {Request: object(map[string]any{"crew_id": str(), "since": str()}), Response: object(map[string]any{"accepted": boolean(), "triggered": boolean(), "worker_id": str(), "note": str()})},
		"POST /api/v1/consolidate/proposed/{id}/approve": {Response: object(map[string]any{"proposal_id": str(), "canonical_path": str(), "rules_merged": integer(), "workspace_id": str(), "crew_id": str(), "decided_by": str(), "version_sha": str()})},
		"POST /api/v1/consolidate/proposed/{id}/reject":  {Request: object(map[string]any{"reason": str()}), Response: object(map[string]any{"proposal_id": str(), "status": str(), "decided_by": str(), "reason": str()})},
		"GET /api/v1/consolidate/proposed/{id}/explain":  {Response: proposal},
		"GET /api/v1/consolidate/proposed/{id}/diff":     {Response: object(map[string]any{"proposal_id": str(), "workspace_id": str(), "crew_id": str(), "status": str(), "canonical_path": str(), "canonical_exists": boolean(), "proposal_path": str(), "rules_count": integer(), "diff": str(), "stats": object(map[string]any{"additions": integer(), "deletions": integer(), "rules_appended": integer()})})},
		"POST /api/v1/eval/replay":                       {Request: object(map[string]any{"mission_id": str(), "seed": integer()}), Response: evalQueued},
		"POST /api/v1/eval/regression":                   {Request: object(map[string]any{"baseline_mission_id": str(), "candidate_mission_id": str()}), Response: evalQueued},
		"GET /api/v1/eval/runs":                          {Response: object(map[string]any{"rows": array(evalRun), "count": integer(), "limit": integer()})},
		"GET /api/v1/eval/runs/{id}":                     {Response: evalRun},
		"GET /api/v1/mcp-registry":                       {Response: object(map[string]any{"servers": array(mcpServer), "total": integer(), "limit": integer(), "offset": integer()})},
		"GET /api/v1/mcp-registry/search":                {Response: object(map[string]any{"servers": array(mcpServer), "total": integer(), "limit": integer(), "offset": integer(), "query": str()})},
		"POST /api/v1/mcp-registry/sync":                 {Response: object(map[string]any{"status": str(), "message": str()})},
		"GET /api/v1/mcp-tool-calls":                     {Response: array(mcpToolCall)},
		"GET /api/v1/skills/proposed":                    {Response: array(proposedSkill)},
		"POST /api/v1/skills/proposed/approve":           {Request: object(map[string]any{"crew_id": str(), "file_name": str()}), Response: object(map[string]any{"skill_id": str(), "slug": str(), "created": boolean(), "file_name": str()})},
		"POST /api/v1/skills/proposed/reject":            {Request: object(map[string]any{"crew_id": str(), "file_name": str()}), Response: object(map[string]any{"file_name": str(), "removed": boolean()})},
		"POST /api/v1/crew-ai-suggest":                   {Request: object(map[string]any{"description": str()}), Response: aiSuggestion},
		"GET /api/v1/memory/health":                      {Response: object(map[string]any{"workspace_id": str(), "crew_id": str(), "computed_at": str(), "overall": number(), "metrics": object(map[string]any{"freshness": number(), "coverage": number(), "coherence": number(), "efficiency": number(), "reachability": number()}), "details": anyValue()})},
		"GET /api/v1/memory/versions":                    {Response: object(map[string]any{"path": str(), "count": integer(), "entries": array(memoryVersion)})},
		"GET /api/v1/memory/versions/{sha}":              {Response: map[string]any{"type": "string", "format": "binary"}, ResponseMedia: []string{"application/octet-stream"}},
		"POST /api/v1/memory/versions/{sha}/restore":     {Request: object(map[string]any{"path": str(), "canonical_path": str(), "tier": str()}), Response: object(map[string]any{"workspace_id": str(), "path": str(), "canonical_path": str(), "restored_sha": str(), "new_version_id": str(), "bytes": integer(), "restored_by": str()})},
		"GET /api/v1/memory/export":                      {Response: object(map[string]any{"format": str(), "documents": array(memoryDocument), "skipped": array(object(map[string]any{"source": str(), "reason": str()}))})},
		"POST /api/v1/memory/import":                     {Request: object(map[string]any{"crew_id": str(), "agent_slug": str(), "documents": array(memoryDocument)}), Response: object(map[string]any{"written": integer(), "rejected": array(anyValue()), "failed": array(anyValue())})},
		"POST /api/v1/memory/search/hybrid":              {Request: object(map[string]any{"query": str(), "limit": integer(), "scope": str(), "crew_id": str()}), Response: object(map[string]any{"query": str(), "count": integer(), "hits": array(memoryHit)})},
	}
}
