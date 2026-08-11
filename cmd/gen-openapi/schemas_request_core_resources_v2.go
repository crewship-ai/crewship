package main

// coreResourceRequestSchemaCatalogV2 is the handler-audited request catalog
// for the public CRUD resources. It is intentionally isolated from the older
// response/component catalogs so request contracts can evolve independently.
func coreResourceRequestSchemaCatalogV2() (map[string]DomainSchema, map[string]any) {
	str := func() map[string]any { return map[string]any{"type": "string"} }
	integer := func() map[string]any { return map[string]any{"type": "integer"} }
	number := func() map[string]any { return map[string]any{"type": "number"} }
	boolean := func() map[string]any { return map[string]any{"type": "boolean"} }
	nullable := func(schema map[string]any) map[string]any {
		copy := map[string]any{"nullable": true}
		for key, value := range schema {
			copy[key] = value
		}
		return copy
	}
	enum := func(values ...string) map[string]any {
		schema := str()
		schema["enum"] = values
		return schema
	}
	array := func(item map[string]any) map[string]any { return map[string]any{"type": "array", "items": item} }
	object := func(properties map[string]any, required ...string) map[string]any {
		schema := map[string]any{"type": "object", "properties": properties}
		if len(required) > 0 {
			schema["required"] = required
		}
		return schema
	}
	freeObject := func() map[string]any { return map[string]any{"type": "object", "additionalProperties": true} }
	ref := func(name string) map[string]any { return map[string]any{"$ref": "#/components/schemas/" + name} }

	components := map[string]any{}
	request := func(name string, schema map[string]any) { components[name] = schema }

	request("CoreWorkspaceCreateRequestV2", object(map[string]any{
		"name": str(), "slug": str(), "preferred_language": nullable(str()),
	}, "name", "slug"))
	request("CoreWorkspaceUpdateRequestV2", object(map[string]any{
		"name": nullable(str()), "slug": nullable(str()), "preferred_language": nullable(str()),
		"allow_privileged_credentials": nullable(boolean()), "run_retention_days": nullable(integer()),
		"credential_audit_retention_days": nullable(integer()), "audit_log_retention_days": nullable(integer()),
	}))

	request("CoreCrewCreateRequestV2", object(map[string]any{
		"name": str(), "slug": str(), "description": nullable(str()), "color": nullable(str()), "icon": nullable(str()),
		"container_memory_mb": nullable(integer()), "container_cpus": nullable(number()), "container_ttl_hours": nullable(integer()),
		"network_mode": nullable(str()), "allowed_domains": array(str()), "allow_private_endpoints": boolean(),
		"runtime_image": nullable(str()), "devcontainer_config": nullable(str()), "mise_config": nullable(str()), "services_json": nullable(str()),
	}, "name", "slug"))
	request("CoreCrewUpdateRequestV2", object(map[string]any{
		"name": nullable(str()), "slug": nullable(str()), "description": nullable(str()), "color": nullable(str()), "icon": nullable(str()), "avatar_style": nullable(str()),
		"container_memory_mb": nullable(integer()), "container_cpus": nullable(number()), "container_ttl_hours": nullable(integer()), "network_mode": nullable(str()),
		"allowed_domains": nullable(array(str())), "allow_private_endpoints": nullable(boolean()), "mcp_config_json": nullable(str()), "escalation_config": nullable(str()),
		"issue_prefix": nullable(str()), "runtime_image": nullable(str()), "devcontainer_config": nullable(str()), "mise_config": nullable(str()), "services_json": nullable(str()), "max_ephemeral_agents": nullable(integer()),
	}))

	request("CoreAgentCreateRequestV2", object(map[string]any{
		"name": str(), "slug": str(), "crew_id": nullable(str()), "description": nullable(str()), "role_title": nullable(str()),
		"agent_role": enum("AGENT", "LEAD"), "lead_mode": nullable(enum("active", "passive")), "cli_adapter": enum("CLAUDE_CODE", "OPENCODE", "CODEX_CLI", "GEMINI_CLI", "CURSOR_CLI", "FACTORY_DROID"),
		"llm_provider": nullable(enum("ANTHROPIC", "OPENAI", "GOOGLE", "CURSOR", "FACTORY", "OLLAMA")), "llm_model": nullable(str()), "system_prompt": nullable(str()),
		"avatar_seed": nullable(str()), "avatar_style": nullable(str()), "timeout_seconds": integer(), "tool_profile": enum("MINIMAL", "CODING", "FULL"), "memory_enabled": boolean(),
	}, "name", "slug", "agent_role", "cli_adapter", "timeout_seconds", "tool_profile", "memory_enabled"))
	request("CoreAgentUpdateRequestV2", object(map[string]any{
		"name": str(), "slug": str(), "description": nullable(str()), "role_title": nullable(str()), "agent_role": enum("AGENT", "LEAD"), "lead_mode": enum("active", "passive"),
		"cli_adapter": enum("CLAUDE_CODE", "OPENCODE", "CODEX_CLI", "GEMINI_CLI", "CURSOR_CLI", "FACTORY_DROID"), "llm_provider": nullable(enum("ANTHROPIC", "OPENAI", "GOOGLE", "CURSOR", "FACTORY", "OLLAMA")), "llm_model": nullable(str()),
		"system_prompt": nullable(str()), "avatar_seed": nullable(str()), "avatar_style": nullable(str()), "timeout_seconds": integer(), "tool_profile": enum("MINIMAL", "CODING", "FULL"), "memory_enabled": boolean(),
		"cli_tools": nullable(str()), "crew_id": nullable(str()), "schedule_cron": nullable(str()), "schedule_prompt": nullable(str()), "schedule_enabled": boolean(), "mcp_config_json": nullable(str()), "webhook_require_timestamp": boolean(),
	}))

	request("CoreProjectCreateRequestV2", object(map[string]any{
		"name": str(), "description": nullable(str()), "icon": nullable(str()), "color": str(), "status": str(), "priority": str(),
		"lead_type": nullable(enum("user", "agent")), "lead_id": nullable(str()), "start_date": nullable(str()), "target_date": nullable(str()),
	}, "name"))
	request("CoreProjectUpdateRequestV2", object(map[string]any{
		"name": nullable(str()), "description": nullable(str()), "icon": nullable(str()), "color": nullable(str()), "status": nullable(str()), "priority": nullable(str()), "health": nullable(str()),
		"lead_type": nullable(enum("user", "agent")), "lead_id": nullable(str()), "start_date": nullable(str()), "target_date": nullable(str()),
	}))

	stringArray := array(str())
	request("CoreIssueCreateRequestV2", object(map[string]any{
		"title": str(), "description": nullable(str()), "priority": str(), "assignee_type": nullable(enum("user", "agent")), "assignee_id": nullable(str()), "due_date": nullable(str()),
		"project_id": nullable(str()), "estimate": nullable(integer()), "parent_issue_id": nullable(str()), "milestone_id": nullable(str()), "labels": stringArray,
		"routine_id": nullable(str()), "routine_inputs": freeObject(),
	}, "title"))
	request("CoreIssueUpdateRequestV2", object(map[string]any{
		"title": nullable(str()), "description": nullable(str()), "status": nullable(enum("BACKLOG", "TODO", "IN_PROGRESS", "REVIEW", "DONE", "CANCELLED", "DUPLICATE", "FAILED")), "priority": nullable(str()),
		"assignee_type": nullable(enum("user", "agent")), "assignee_id": nullable(str()), "due_date": nullable(str()), "project_id": nullable(str()), "estimate": nullable(integer()), "parent_issue_id": nullable(str()), "milestone_id": nullable(str()), "sort_order": nullable(number()), "labels": nullable(stringArray),
		"routine_id": nullable(str()), "routine_inputs": nullable(freeObject()),
	}))
	request("CoreIssueBulkUpdateRequestV2", object(map[string]any{
		"ids": stringArray, "updates": object(map[string]any{
			"status": nullable(str()), "priority": nullable(str()), "assignee_type": nullable(enum("user", "agent")), "assignee_id": nullable(str()), "project_id": nullable(str()), "labels": nullable(stringArray),
		}),
	}, "ids", "updates"))
	request("CoreLabelCreateRequestV2", object(map[string]any{"name": str(), "color": str(), "label_group": nullable(str())}, "name", "color"))
	request("CoreLabelUpdateRequestV2", object(map[string]any{"name": nullable(str()), "color": nullable(str()), "label_group": nullable(str())}))

	request("CoreSkillImportRequestV2", object(map[string]any{"url": str(), "content": str(), "allow_unsafe_license": boolean()}))
	request("CoreSkillGenerateRequestV2", object(map[string]any{"slug": str(), "prompt": str(), "model": str()}, "slug", "prompt"))
	request("CoreSkillBulkImportRequestV2", object(map[string]any{"git_url": str(), "git_ref": str(), "paths": stringArray, "vendor": str(), "allow_unsafe_license": boolean(), "dry_run": boolean()}, "git_url"))
	request("CoreAgentSkillRequestV2", object(map[string]any{"skill_id": str(), "config": nullable(str())}, "skill_id"))

	request("CoreCredentialCreateRequestV2", object(map[string]any{
		"name": str(), "description": nullable(str()), "value": str(), "type": str(), "provider": str(), "scope": str(), "crew_id": nullable(str()), "crew_ids": stringArray, "tags": stringArray,
		"account_label": nullable(str()), "account_email": nullable(str()), "refresh_token": nullable(str()), "token_expires_at": nullable(str()), "security_level": nullable(integer()),
		"created_by_actor_type": nullable(enum("user", "agent", "system")), "created_by_actor_id": nullable(str()), "provisioned_for_service": nullable(str()), "username": nullable(str()),
		"oauth_client_id": nullable(str()), "oauth_client_secret": nullable(str()), "oauth_auth_url": nullable(str()), "oauth_token_url": nullable(str()), "oauth_scopes": nullable(str()), "pending": boolean(),
	}, "name", "value"))
	request("CoreCredentialUpdateRequestV2", object(map[string]any{
		"name": nullable(str()), "description": nullable(str()), "type": nullable(str()), "provider": nullable(str()), "scope": nullable(str()), "crew_id": nullable(str()), "crew_ids": nullable(stringArray), "account_label": nullable(str()), "account_email": nullable(str()), "token_expires_at": nullable(str()), "security_level": nullable(integer()), "username": nullable(str()),
	}))
	request("CoreCredentialFieldRequestV2", object(map[string]any{"key": str(), "value": str(), "is_secret": nullable(boolean()), "ordinal": nullable(integer())}, "value"))
	request("CoreCredentialBindingRequestV2", object(map[string]any{"credential_id": str(), "scope": str(), "crew_id": nullable(str()), "agent_id": nullable(str()), "slot": str()}, "credential_id", "scope", "slot"))
	request("CoreCredentialRotationRequestV2", object(map[string]any{"value": str(), "grace_seconds": nullable(integer()), "endpoint_base_url": nullable(str()), "endpoint_auth_token": nullable(str()), "endpoint_headers": nullable(map[string]any{"type": "object", "additionalProperties": str()})}, "value"))
	request("CoreAgentCredentialRequestV2", object(map[string]any{"credential_id": str(), "env_var_name": str(), "priority": integer(), "ttl_seconds": integer()}, "credential_id", "env_var_name"))

	routes := map[string]DomainSchema{}
	add := func(method, path, component string) { routes[method+" "+path] = DomainSchema{Request: ref(component)} }
	add("POST", "/api/v1/workspaces", "CoreWorkspaceCreateRequestV2")
	add("PATCH", "/api/v1/workspaces/{workspaceId}", "CoreWorkspaceUpdateRequestV2")
	add("POST", "/api/v1/crews", "CoreCrewCreateRequestV2")
	add("PATCH", "/api/v1/crews/{crewId}", "CoreCrewUpdateRequestV2")
	add("PUT", "/api/v1/crews/{crewId}", "CoreCrewUpdateRequestV2")
	add("POST", "/api/v1/agents", "CoreAgentCreateRequestV2")
	add("PATCH", "/api/v1/agents/{agentId}", "CoreAgentUpdateRequestV2")
	add("POST", "/api/v1/projects", "CoreProjectCreateRequestV2")
	add("PATCH", "/api/v1/projects/{projectId}", "CoreProjectUpdateRequestV2")
	add("POST", "/api/v1/crews/{crewId}/issues", "CoreIssueCreateRequestV2")
	add("PATCH", "/api/v1/crews/{crewId}/issues/{identifier}", "CoreIssueUpdateRequestV2")
	add("PATCH", "/api/v1/issues/bulk", "CoreIssueBulkUpdateRequestV2")
	add("POST", "/api/v1/labels", "CoreLabelCreateRequestV2")
	add("PATCH", "/api/v1/labels/{labelId}", "CoreLabelUpdateRequestV2")
	add("POST", "/api/v1/workspaces/{workspaceId}/skills/import", "CoreSkillImportRequestV2")
	add("POST", "/api/v1/workspaces/{workspaceId}/skills/generate", "CoreSkillGenerateRequestV2")
	add("POST", "/api/v1/workspaces/{workspaceId}/skills/bulk-import", "CoreSkillBulkImportRequestV2")
	add("POST", "/api/v1/agents/{agentId}/skills", "CoreAgentSkillRequestV2")
	add("POST", "/api/v1/credentials", "CoreCredentialCreateRequestV2")
	add("POST", "/api/v1/credentials/test", "CoreCredentialCreateRequestV2")
	add("PATCH", "/api/v1/credentials/{credentialId}", "CoreCredentialUpdateRequestV2")
	add("PUT", "/api/v1/credentials/{credentialId}", "CoreCredentialUpdateRequestV2")
	add("POST", "/api/v1/credentials/{credentialId}/fields", "CoreCredentialFieldRequestV2")
	add("PUT", "/api/v1/credentials/{credentialId}/fields/{fieldKey}", "CoreCredentialFieldRequestV2")
	add("POST", "/api/v1/credentials/bindings", "CoreCredentialBindingRequestV2")
	add("POST", "/api/v1/credentials/{credentialId}/rotate", "CoreCredentialRotationRequestV2")
	add("POST", "/api/v1/agents/{agentId}/credentials", "CoreAgentCredentialRequestV2")
	return routes, components
}
