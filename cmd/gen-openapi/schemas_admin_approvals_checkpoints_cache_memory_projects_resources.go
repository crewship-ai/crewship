package main

// schemaCatalogAdminApprovalsCheckpointsCacheMemoryProjectsResources contains
// the API surfaces audited in this worktree. It is intentionally a separate
// catalog so another domain can be added without editing a shared schema file.
// The schemas mirror the JSON assembled by the handlers, not their private Go
// implementation types.
func schemaCatalogAdminApprovalsCheckpointsCacheMemoryProjectsResources() map[string]DomainSchema {
	str := func() map[string]any { return map[string]any{"type": "string"} }
	integer := func() map[string]any { return map[string]any{"type": "integer"} }
	number := func() map[string]any { return map[string]any{"type": "number"} }
	boolean := func() map[string]any { return map[string]any{"type": "boolean"} }
	anyObject := func() map[string]any {
		return map[string]any{"type": "object", "additionalProperties": true}
	}
	// Variadic `required`, matching schemas_core.go. A response schema that
	// names its properties but not its required ones certifies a body that
	// shares no field name with what the server sends — which is how an
	// all-PascalCase approvals response passed every check we own.
	object := func(properties map[string]any, required ...string) map[string]any {
		s := map[string]any{"type": "object", "properties": properties}
		if len(required) > 0 {
			s["required"] = required
		}
		return s
	}
	array := func(items map[string]any) map[string]any {
		return map[string]any{"type": "array", "items": items}
	}
	stringArray := func() map[string]any { return array(str()) }
	stringMap := func() map[string]any {
		return map[string]any{"type": "object", "additionalProperties": str()}
	}
	booleanMap := func() map[string]any {
		return map[string]any{"type": "object", "additionalProperties": boolean()}
	}
	integerMap := func() map[string]any {
		return map[string]any{"type": "object", "additionalProperties": integer()}
	}
	nullable := func(schema map[string]any) map[string]any {
		copy := map[string]any{}
		for k, v := range schema {
			copy[k] = v
		}
		copy["nullable"] = true
		return copy
	}

	stats := object(map[string]any{
		"workspaces": integer(), "users": integer(), "crews": integer(),
		"agents": integer(), "running": integer(),
	})
	adminUser := object(map[string]any{
		"id": str(), "email": str(), "full_name": nullable(str()),
		"avatar_url": nullable(str()), "created_at": str(),
		"workspace": object(map[string]any{"id": str(), "name": str(), "slug": str()}),
		"role":      nullable(str()), "workspaces": array(object(map[string]any{"id": str(), "name": str(), "slug": str()})),
	})
	adminWorkspace := object(map[string]any{
		"id": str(), "name": str(), "slug": str(), "created_at": str(), "updated_at": str(),
		"_count_members": integer(), "_count_agents": integer(), "_count_crews": integer(),
	})

	approval := object(map[string]any{
		"id": str(), "workspace_id": str(), "crew_id": str(), "agent_id": str(), "mission_id": str(),
		"requested_by": str(), "kind": str(), "reason": str(), "payload": anyObject(), "status": str(),
		"decided_by": nullable(str()), "decided_at": nullable(str()), "decision_comment": str(),
		"timeout_at": nullable(str()), "created_at": str(),
	},
		// harbormaster.Request has no omitempty on any of these, so the server
		// emits all fifteen on every row. Naming them is what lets the contract
		// gate see a renamed field at all.
		"id", "workspace_id", "crew_id", "agent_id", "mission_id", "requested_by",
		"kind", "reason", "payload", "status", "decided_by", "decided_at",
		"decision_comment", "timeout_at", "created_at")
	checkpointState := object(map[string]any{
		"agent_memory":  stringMap(),
		"pending_tasks": stringArray(), "open_assignments": stringArray(),
		"crew_container_id": nullable(str()), "meta": anyObject(),
	})
	checkpoint := object(map[string]any{
		"id": str(), "workspace_id": str(), "crew_id": nullable(str()), "mission_id": str(),
		"label": nullable(str()), "journal_cursor": str(), "state": checkpointState,
		"fork_of": nullable(str()), "created_by": nullable(str()), "created_at": str(),
	})

	cacheImage := object(map[string]any{
		"tag": str(), "size": integer(), "created_at": integer(), "referenced_by": stringArray(),
	})
	memoryVersion := object(map[string]any{
		"id": str(), "path": str(), "tier": str(), "sha256": str(), "bytes": integer(),
		"written_at": str(), "written_by": str(), "parent_sha": nullable(str()),
	})
	memoryStats := object(map[string]any{
		"workspace_id": str(),
		"totals":       object(map[string]any{"versions": integer(), "bytes": integer(), "blobs": integer(), "oldest_at": str(), "newest_at": str()}),
		"by_tier":      array(object(map[string]any{"tier": str(), "versions": integer(), "bytes": integer()})),
		"by_agent":     array(object(map[string]any{"agent_slug": str(), "versions": integer(), "bytes": integer(), "newest_at": str()})),
	}, "workspace_id", "totals", "by_tier", "by_agent")
	memoryVersionList := object(map[string]any{
		"workspace_id": str(), "rows": array(memoryVersion), "next_cursor": nullable(str()),
		"limit": integer(), "filters_applied": stringMap(),
	}, "workspace_id", "rows", "next_cursor", "limit", "filters_applied")
	memoryConfig := object(map[string]any{
		"workspace_id": str(), "versions_retention_days": integer(), "is_default": boolean(), "raw_config": nullable(str()),
	}, "workspace_id", "versions_retention_days", "is_default", "raw_config")
	memoryHealth := object(map[string]any{
		"workspace_id": str(), "crew_id": nullable(str()), "computed_at": str(), "overall": number(),
		"metrics": object(map[string]any{"freshness": number(), "coverage": number(), "coherence": number(), "efficiency": number(), "reachability": number()}),
		"details": anyObject(),
	})

	skillAgent := object(map[string]any{
		"agent_id": str(), "agent_slug": str(), "agent_name": str(), "avatar_seed": nullable(str()),
		"avatar_style": nullable(str()), "avatar_url": nullable(str()), "crew_id": nullable(str()),
		"crew_slug": nullable(str()), "crew_name": nullable(str()), "crew_color": nullable(str()),
		"crew_icon": nullable(str()), "crew_avatar_style": nullable(str()),
	})
	skill := object(map[string]any{
		"id": str(), "name": str(), "slug": str(), "display_name": str(), "description": nullable(str()),
		"version": str(), "author": nullable(str()), "category": str(), "source": str(), "icon": nullable(str()),
		"verification": str(), "downloads": integer(), "rating_avg": nullable(number()), "rating_count": integer(),
		"tags": nullable(str()), "featured": boolean(), "pricing_tier": str(), "tool_count": nullable(integer()),
		"vendor": nullable(str()), "homepage": nullable(str()), "spdx_license": nullable(str()), "runtime": str(),
		"maturity": str(), "scan_status": str(), "description_quality": nullable(str()), "created_at": str(),
		"updated_at": str(), "installed_on": array(skillAgent),
	})
	skillDetail := object(map[string]any{
		"id": str(), "name": str(), "slug": str(), "display_name": str(), "description": nullable(str()),
		"version": str(), "author": nullable(str()), "category": str(), "source": str(), "icon": nullable(str()),
		"verification": str(), "downloads": integer(), "rating_avg": nullable(number()), "rating_count": integer(),
		"tags": nullable(str()), "featured": boolean(), "pricing_tier": str(), "tool_count": nullable(integer()),
		"vendor": nullable(str()), "homepage": nullable(str()), "spdx_license": nullable(str()), "runtime": str(),
		"maturity": str(), "scan_status": str(), "description_quality": nullable(str()), "created_at": str(),
		"updated_at": str(), "installed_on": array(skillAgent), "content": nullable(str()),
		"credential_requirements": nullable(str()), "mcp_server_command": nullable(str()), "mcp_server_image": nullable(str()),
		"mcp_transport": nullable(str()), "dependencies": nullable(str()), "license": nullable(str()), "agent_count": integer(),
		"security_score": nullable(integer()), "allowed_domains": nullable(str()), "changelog": nullable(str()),
	})
	recipeCredential := object(map[string]any{"env_var_name": str(), "provider": str(), "type": str(), "label": str(), "help_url": nullable(str())})
	recipeServer := object(map[string]any{
		"name": str(), "display_name": str(), "transport": str(), "command": nullable(str()), "args": stringArray(),
		"endpoint": nullable(str()), "env_mapping": stringMap(), "icon": nullable(str()),
	})
	recipe := object(map[string]any{
		"slug": str(), "name": str(), "description": str(), "icon": str(), "color": str(), "crew_slug": str(),
		"credentials": array(recipeCredential), "mcp_servers": array(recipeServer),
	})
	project := object(map[string]any{
		"id": str(), "workspace_id": str(), "name": str(), "slug": str(), "description": nullable(str()), "icon": nullable(str()),
		"color": str(), "status": str(), "priority": str(), "health": str(), "lead_type": nullable(str()), "lead_id": nullable(str()),
		"lead_name": nullable(str()), "start_date": nullable(str()), "target_date": nullable(str()), "created_at": str(), "updated_at": str(),
		"issue_count": integer(), "done_count": integer(), "progress": integer(),
	})
	resources := object(map[string]any{
		"datastores": array(object(map[string]any{"type": str(), "name": str(), "host": str(), "port": str()})),
		"tools":      array(object(map[string]any{"type": str(), "name": str()})),
	})
	capabilities := object(map[string]any{
		"crew_id": str(), "crew_slug": str(), "container": resources,
		"integrations": array(object(map[string]any{"id": str(), "name": str(), "display_name": nullable(str()), "tools": stringArray()})),
		"agents":       array(object(map[string]any{"slug": str(), "name": str()})),
		"runtimes":     anyObject(), "schema": anyObject(),
	})
	_ = skill
	_ = skillDetail

	return map[string]DomainSchema{
		"GET /api/v1/admin/stats":      {Response: stats},
		"GET /api/v1/admin/users":      {Response: array(adminUser)},
		"GET /api/v1/admin/workspaces": {Response: array(adminWorkspace)},
		"GET /api/v1/admin/health":     {Response: object(map[string]any{"uptime_seconds": integer(), "log_level": anyObject(), "encryption_key_source": str(), "db": anyObject(), "disk": anyObject()})},
		"GET /api/v1/admin/security-posture": {Response: object(map[string]any{"environment": str(), "encryption_key_configured": boolean(), "plaintext_secrets_allowed": boolean(), "private_endpoints_ceiling": boolean(), "signup_open": boolean(), "oauth_configured": boolean(), "email_configured": boolean(), "rate_limit_disabled": boolean(), "rate_limit_effectively_disabled": boolean(), "warnings": array(object(map[string]any{"key": str(), "severity": str(), "message": str()}, "key", "severity", "message"))},
			"environment", "encryption_key_configured", "plaintext_secrets_allowed", "private_endpoints_ceiling", "signup_open",
			"oauth_configured", "email_configured", "rate_limit_disabled", "rate_limit_effectively_disabled", "warnings")},
		"GET /api/v1/admin/log-level":         {Response: object(map[string]any{"level": str(), "baseline": str(), "expires_at": nullable(str())})},
		"PUT /api/v1/admin/log-level":         {Request: object(map[string]any{"level": str(), "ttl_seconds": integer()})},
		"GET /api/v1/admin/rate-limits":       {Response: object(map[string]any{"limiters": array(anyObject())})},
		"PUT /api/v1/admin/rate-limits/{key}": {Request: object(map[string]any{"value": integer()})},
		"GET /api/v1/approvals": {Response: object(map[string]any{"rows": array(approval), "status": str(), "count": integer(), "has_more": map[string]any{"type": "boolean"}},
			// The ENVELOPE needs its own required list, not just the row. Without
			// it `{}` validates: an empty object is a valid instance of a schema
			// whose every property is optional, so a handler that returned
			// nothing at all would satisfy the contract. ApprovalsHandler.List
			// writes this envelope as a map literal, so there is no struct to
			// derive it from — see the DTO note in
			// docs/prd/response-shape-contract.md.
			"rows", "status", "count", "has_more")},
		"GET /api/v1/approvals/{id}":                               {Response: approval},
		"POST /api/v1/approvals/{id}/decide":                       {Request: object(map[string]any{"status": str(), "comment": str()}), Response: object(map[string]any{"status": str(), "decided_by": str()})},
		"POST /api/v1/approvals/{id}/cancel":                       {Request: object(map[string]any{"reason": str()}), Response: object(map[string]any{"status": str(), "cancelled_by": str()})},
		"POST /api/v1/approvals/reset-auto-tuning":                 {Request: object(map[string]any{"tool": str()}), Response: object(map[string]any{"tool": str(), "rows_deleted": integer(), "workspace_id": str()})},
		"GET /api/v1/missions/{missionId}/checkpoints":             {Response: object(map[string]any{"checkpoints": array(checkpoint), "count": integer(), "mission_id": str()})},
		"POST /api/v1/missions/{missionId}/checkpoints":            {Request: object(map[string]any{"label": str()}), Response: checkpoint},
		"GET /api/v1/checkpoints/{id}":                             {Response: checkpoint},
		"POST /api/v1/checkpoints/{id}/restore":                    {Response: object(map[string]any{"checkpoint": checkpoint, "journal_cursor": str(), "warn_divergence": stringArray()})},
		"POST /api/v1/checkpoints/{id}/fork":                       {Request: object(map[string]any{"label": str()}), Response: object(map[string]any{"new_mission_id": str(), "new_checkpoint_id": str()})},
		"GET /api/v1/cache/images":                                 {Response: object(map[string]any{"images": array(cacheImage)})},
		"DELETE /api/v1/cache/images/{tag}":                        {Response: object(map[string]any{"tag": str(), "status": str()})},
		"GET /api/v1/admin/memory/stats":                           {Response: memoryStats},
		"GET /api/v1/admin/memory/versions":                        {Response: memoryVersionList},
		"GET /api/v1/admin/memory/config":                          {Response: memoryConfig},
		"PATCH /api/v1/admin/memory/config":                        {Request: object(map[string]any{"versions_retention_days": integer()}), Response: memoryConfig},
		"GET /api/v1/admin/memory/versions/{id}/content":           {Response: map[string]any{"type": "string", "format": "binary"}, ResponseMedia: []string{"text/markdown", "application/octet-stream"}},
		"GET /api/v1/memory/health":                                {Response: memoryHealth},
		"GET /api/v1/memory/versions":                              {Response: memoryVersionList},
		"GET /api/v1/memory/versions/{sha}":                        {Response: map[string]any{"type": "string", "format": "binary"}, ResponseMedia: []string{"application/octet-stream"}},
		"POST /api/v1/memory/versions/{sha}/restore":               {Request: object(map[string]any{"path": str(), "canonical_path": str(), "tier": str()})},
		"POST /api/v1/workspaces/{workspaceId}/skills/import":      {Request: object(map[string]any{"url": str(), "content": str(), "allow_unsafe_license": boolean()})},
		"POST /api/v1/workspaces/{workspaceId}/skills/generate":    {Request: object(map[string]any{"slug": str(), "prompt": str(), "model": str()}), Response: object(map[string]any{"skill_id": str(), "slug": str(), "content": str(), "scan_status": str(), "scan_reason": nullable(str()), "description_quality": nullable(str())})},
		"POST /api/v1/workspaces/{workspaceId}/skills/bulk-import": {Request: object(map[string]any{"git_url": str(), "git_ref": str(), "paths": stringArray(), "vendor": str(), "allow_unsafe_license": boolean(), "dry_run": boolean()})},
		"GET /api/v1/recipes":                                      {Response: array(recipe)},
		"GET /api/v1/recipes/{slug}":                               {Response: recipe},
		"GET /api/v1/recipes/{slug}/preview":                       {Response: object(map[string]any{"recipe": recipe, "needed_credentials": stringArray(), "existing_credentials": booleanMap(), "crew_slug_available": boolean(), "resolved_crew_slug": str()})},
		"POST /api/v1/recipes/{slug}/install":                      {Request: object(map[string]any{"credential_values": stringMap(), "account_labels": stringMap()}), Response: object(map[string]any{"crew_id": str(), "crew_slug": str(), "credentials_added": stringArray(), "credentials_reused": stringArray(), "mcp_servers_added": stringArray()})},
		"POST /api/v1/projects":                                    {Request: object(map[string]any{"name": str(), "description": nullable(str()), "icon": nullable(str()), "color": str(), "status": str(), "priority": str(), "lead_type": nullable(str()), "lead_id": nullable(str()), "start_date": nullable(str()), "target_date": nullable(str())}), Response: project},
		"PATCH /api/v1/projects/{projectId}":                       {Request: object(map[string]any{"name": nullable(str()), "description": nullable(str()), "icon": nullable(str()), "color": nullable(str()), "status": nullable(str()), "priority": nullable(str()), "health": nullable(str()), "lead_type": nullable(str()), "lead_id": nullable(str()), "start_date": nullable(str()), "target_date": nullable(str())}), Response: project},
		"GET /api/v1/projects/{projectId}/stats":                   {Response: object(map[string]any{"total_issues": integer(), "completed_issues": integer(), "by_status": integerMap(), "by_assignee": array(anyObject()), "by_label": array(anyObject()), "crews": stringArray()})},
		"GET /api/v1/crews/{crewId}/capabilities":                  {Response: capabilities},
	}
}
