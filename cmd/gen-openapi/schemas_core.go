package main

// coreResourceSchemas returns the OpenAPI components for the core resource
// surface.  Keep this map separate from route discovery: these shapes are
// contracts of the handlers, not properties inferred from database columns.
// In particular, request schemas intentionally do not reuse response schemas
// because the create and patch handlers accept different fields.
func coreResourceSchemas() map[string]any {
	stringSchema := func() map[string]any { return map[string]any{"type": "string"} }
	intSchema := func() map[string]any { return map[string]any{"type": "integer", "format": "int32"} }
	numberSchema := func() map[string]any { return map[string]any{"type": "number", "format": "double"} }
	boolSchema := func() map[string]any { return map[string]any{"type": "boolean"} }
	nullableString := func() map[string]any {
		return map[string]any{"type": "string", "nullable": true}
	}
	nullableInt := func() map[string]any {
		return map[string]any{"type": "integer", "format": "int32", "nullable": true}
	}
	nullableBool := func() map[string]any {
		return map[string]any{"type": "boolean", "nullable": true}
	}
	arrayOf := func(items map[string]any) map[string]any {
		return map[string]any{"type": "array", "items": items}
	}
	ref := func(name string) map[string]any {
		return map[string]any{"$ref": "#/components/schemas/" + name}
	}
	object := func(properties map[string]any, required ...string) map[string]any {
		s := map[string]any{"type": "object", "properties": properties}
		if len(required) > 0 {
			s["required"] = required
		}
		return s
	}
	requestObject := func(properties map[string]any, required ...string) map[string]any {
		return object(properties, required...)
	}
	stringEnum := func(values ...string) map[string]any {
		s := stringSchema()
		s["enum"] = values
		return s
	}
	nullableEnum := func(values ...string) map[string]any {
		s := nullableString()
		s["enum"] = values
		return s
	}

	workspaceCounts := object(map[string]any{
		"crews": intSchema(), "agents": intSchema(), "members": intSchema(),
	}, "crews", "agents", "members")
	crewCounts := object(map[string]any{
		"agents": intSchema(), "members": intSchema(),
	}, "agents", "members")
	agentCounts := object(map[string]any{
		"skills": intSchema(), "credentials": intSchema(), "chats": intSchema(),
	}, "skills", "credentials", "chats")
	agentCrew := object(map[string]any{
		"name": stringSchema(), "slug": stringSchema(),
		"color": nullableString(), "avatar_style": nullableString(),
	}, "name", "slug")

	workspace := object(map[string]any{
		"id": stringSchema(), "name": stringSchema(), "slug": stringSchema(),
		"logo_url": nullableString(), "preferred_language": nullableString(),
		"created_at": stringSchema(), "updated_at": stringSchema(),
		"currentUserRole":              nullableString(),
		"currentUserCapabilities":      arrayOf(stringSchema()),
		"allow_privileged_credentials": boolSchema(),
		"run_retention_days":           nullableInt(), "_count": ref("WorkspaceCounts"),
		"_count_crews": intSchema(), "_count_agents": intSchema(), "_count_members": intSchema(),
	}, "id", "name", "slug", "created_at", "updated_at", "allow_privileged_credentials")

	crew := object(map[string]any{
		"id": stringSchema(), "workspace_id": stringSchema(), "name": stringSchema(), "slug": stringSchema(),
		"description": nullableString(), "color": nullableString(), "icon": nullableString(), "avatar_style": nullableString(),
		"container_memory_mb": intSchema(), "container_cpus": numberSchema(), "container_ttl_hours": nullableInt(),
		"network_mode": stringSchema(), "network_mode_enforced": boolSchema(), "network_mode_unenforced_reason": stringSchema(),
		"allowed_domains": arrayOf(stringSchema()), "allow_private_endpoints": boolSchema(),
		"mcp_config_json": nullableString(), "escalation_config": nullableString(), "runtime_image": nullableString(),
		"devcontainer_config": nullableString(), "mise_config": nullableString(), "services_json": nullableString(),
		"cached_image": nullableString(), "config_hash": nullableString(), "issue_prefix": nullableString(),
		"max_ephemeral_agents": intSchema(), "created_at": stringSchema(), "updated_at": stringSchema(),
		"_count": ref("CrewCounts"),
	}, "id", "workspace_id", "name", "slug", "container_memory_mb", "container_cpus", "network_mode",
		"network_mode_enforced", "allowed_domains", "allow_private_endpoints", "max_ephemeral_agents", "created_at", "updated_at", "_count")

	agent := object(map[string]any{
		"id": stringSchema(), "crew_id": nullableString(), "workspace_id": stringSchema(), "name": stringSchema(), "slug": stringSchema(),
		"description": nullableString(), "role_title": nullableString(), "agent_role": stringSchema(), "lead_mode": nullableString(), "status": stringSchema(),
		"cli_adapter": stringSchema(), "llm_provider": nullableString(), "llm_model": nullableString(), "system_prompt": nullableString(),
		"avatar_seed": nullableString(), "avatar_style": nullableString(), "avatar_url": nullableString(), "timeout_seconds": intSchema(),
		"tool_profile": stringSchema(), "memory_enabled": boolSchema(), "cli_tools": nullableString(), "schedule_cron": nullableString(),
		"schedule_prompt": nullableString(), "schedule_enabled": boolSchema(), "schedule_last_run": nullableString(), "schedule_next_run": nullableString(),
		"webhook_require_timestamp": boolSchema(), "webhook_secret_set": nullableBool(), "mcp_config_json": nullableString(),
		"created_at": stringSchema(), "updated_at": stringSchema(), "crew": ref("AgentCrew"), "_count": ref("AgentCounts"),
		"created_by_user_id": stringSchema(), "ephemeral": boolSchema(), "expires_at": nullableString(), "expired_at": nullableString(),
		"parent_lead_id": nullableString(), "hire_reason": nullableString(),
	}, "id", "workspace_id", "name", "slug", "agent_role", "status", "cli_adapter", "timeout_seconds", "tool_profile",
		"memory_enabled", "schedule_enabled", "webhook_require_timestamp", "created_at", "updated_at", "crew", "_count", "ephemeral")

	project := object(map[string]any{
		"id": stringSchema(), "workspace_id": stringSchema(), "name": stringSchema(), "slug": stringSchema(),
		"description": nullableString(), "icon": nullableString(), "color": stringSchema(), "status": stringSchema(),
		"priority": stringSchema(), "health": stringSchema(), "lead_type": nullableString(), "lead_id": nullableString(),
		"lead_name": nullableString(), "start_date": nullableString(), "target_date": nullableString(),
		"created_at": stringSchema(), "updated_at": stringSchema(), "issue_count": intSchema(), "done_count": intSchema(), "progress": intSchema(),
	}, "id", "workspace_id", "name", "slug", "color", "status", "priority", "health", "created_at", "updated_at", "issue_count", "done_count", "progress")

	return map[string]any{
		"Workspace": workspace, "WorkspaceList": arrayOf(ref("Workspace")), "WorkspaceCounts": workspaceCounts,
		"Crew": crew, "CrewList": arrayOf(ref("Crew")), "CrewCounts": crewCounts,
		"Agent": agent, "AgentList": arrayOf(ref("Agent")), "AgentCrew": agentCrew, "AgentCounts": agentCounts,
		"Project": project, "ProjectList": arrayOf(ref("Project")),
		// ProjectListItem is retained as an alias for the name used by the
		// existing route catalog. List and detail handlers return the same
		// projectResponse shape.
		"ProjectListItem": project,

		"WorkspaceCreateRequest": requestObject(map[string]any{
			"name": stringSchema(), "slug": stringSchema(), "preferred_language": nullableString(),
		}, "name", "slug"),
		"WorkspaceUpdateRequest": requestObject(map[string]any{
			"name": nullableString(), "slug": nullableString(), "preferred_language": nullableString(),
			"allow_privileged_credentials": nullableBool(), "run_retention_days": nullableInt(),
		}),
		"CrewCreateRequest": requestObject(map[string]any{
			"name": stringSchema(), "slug": stringSchema(), "description": nullableString(), "color": nullableString(), "icon": nullableString(),
			"container_memory_mb": nullableInt(), "container_cpus": numberSchema(), "container_ttl_hours": nullableInt(), "network_mode": nullableString(),
			"allowed_domains": arrayOf(stringSchema()), "allow_private_endpoints": boolSchema(), "runtime_image": nullableString(),
			"devcontainer_config": nullableString(), "mise_config": nullableString(), "services_json": nullableString(),
		}, "name", "slug"),
		"CrewUpdateRequest": requestObject(map[string]any{
			"name": nullableString(), "slug": nullableString(), "description": nullableString(), "color": nullableString(), "icon": nullableString(), "avatar_style": nullableString(),
			"container_memory_mb": nullableInt(), "container_cpus": numberSchema(), "container_ttl_hours": nullableInt(), "network_mode": nullableString(),
			"allowed_domains": arrayOf(stringSchema()), "allow_private_endpoints": nullableBool(), "mcp_config_json": nullableString(), "escalation_config": nullableString(),
			"issue_prefix": nullableString(), "runtime_image": nullableString(), "devcontainer_config": nullableString(), "mise_config": nullableString(), "services_json": nullableString(), "max_ephemeral_agents": nullableInt(),
		}),
		"AgentCreateRequest": requestObject(map[string]any{
			"name": stringSchema(), "slug": stringSchema(), "crew_id": nullableString(), "description": nullableString(), "role_title": nullableString(),
			"agent_role": stringEnum("AGENT", "LEAD"), "lead_mode": nullableEnum("active", "passive"), "cli_adapter": stringSchema(), "llm_provider": nullableString(), "llm_model": nullableString(),
			"system_prompt": nullableString(), "avatar_seed": nullableString(), "avatar_style": nullableString(), "timeout_seconds": intSchema(), "tool_profile": stringEnum("MINIMAL", "CODING", "FULL"), "memory_enabled": boolSchema(),
		}, "name", "slug", "agent_role", "cli_adapter", "timeout_seconds", "tool_profile", "memory_enabled"),
		"AgentUpdateRequest": requestObject(map[string]any{
			"name": stringSchema(), "slug": stringSchema(), "description": nullableString(), "role_title": nullableString(), "agent_role": stringEnum("AGENT", "LEAD"), "lead_mode": stringEnum("active", "passive"),
			"cli_adapter": stringSchema(), "llm_provider": nullableString(), "llm_model": nullableString(), "system_prompt": nullableString(), "avatar_seed": nullableString(), "avatar_style": nullableString(),
			"timeout_seconds": intSchema(), "tool_profile": stringEnum("MINIMAL", "CODING", "FULL"), "memory_enabled": boolSchema(), "cli_tools": nullableString(), "crew_id": nullableString(),
			"schedule_cron": nullableString(), "schedule_prompt": nullableString(), "schedule_enabled": boolSchema(), "mcp_config_json": nullableString(), "webhook_require_timestamp": boolSchema(),
		}),
		"ProjectCreateRequest": requestObject(map[string]any{
			"name": stringSchema(), "description": nullableString(), "icon": nullableString(), "color": stringSchema(), "status": stringSchema(), "priority": stringSchema(), "lead_type": nullableEnum("user", "agent"), "lead_id": nullableString(), "start_date": nullableString(), "target_date": nullableString(),
		}, "name"),
		"ProjectUpdateRequest": requestObject(map[string]any{
			"name": nullableString(), "description": nullableString(), "icon": nullableString(), "color": nullableString(), "status": nullableString(), "priority": nullableString(), "health": nullableString(), "lead_type": nullableEnum("user", "agent"), "lead_id": nullableString(), "start_date": nullableString(), "target_date": nullableString(),
		}),
		"HireRequest": requestObject(map[string]any{
			"crew_id": stringSchema(), "crew_slug": stringSchema(), "template_slug": stringSchema(), "model": stringSchema(), "ttl_minutes": intSchema(), "reason": stringSchema(), "parent_lead_id": stringSchema(),
		}),
		"HireResponse": object(map[string]any{
			"id": stringSchema(), "crew_id": nullableString(), "workspace_id": stringSchema(), "slug": stringSchema(), "name": stringSchema(), "status": stringSchema(), "ephemeral": boolSchema(), "expires_at": nullableString(), "expired_at": nullableString(), "parent_lead_id": nullableString(), "hire_reason": nullableString(), "pending_review": boolSchema(), "inbox_item_id": stringSchema(), "approval_id": stringSchema(), "decision": stringSchema(),
		}, "id", "workspace_id", "slug", "name", "status", "ephemeral", "pending_review", "decision"),
	}
}
