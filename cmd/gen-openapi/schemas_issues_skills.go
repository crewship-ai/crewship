package main

// DomainSchemaMap returns the OpenAPI component schemas for the issue,
// label, skill, and credential surfaces.  The map is deliberately kept in a
// separate file from the source scanner: these contracts are derived from
// the API handler response/request types, while route discovery remains
// source based.
//
// The returned value is a fresh map and may be changed by callers.
func issueSkillCredentialSchemaComponents() map[string]any {
	stringMap := map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}}
	stringArray := func() map[string]any {
		return map[string]any{"type": "array", "items": map[string]any{"type": "string"}}
	}
	str := func() map[string]any { return map[string]any{"type": "string"} }
	integer := func() map[string]any { return map[string]any{"type": "integer"} }
	number := func() map[string]any { return map[string]any{"type": "number"} }
	boolean := func() map[string]any { return map[string]any{"type": "boolean"} }
	nullable := func(typ string) map[string]any {
		return map[string]any{"type": typ, "nullable": true}
	}
	arrayOf := func(item map[string]any) map[string]any {
		return map[string]any{"type": "array", "items": item}
	}
	ref := func(name string) map[string]any {
		return map[string]any{"$ref": "#/components/schemas/" + name}
	}
	obj := func(properties map[string]any, required ...string) map[string]any {
		s := map[string]any{"type": "object", "properties": properties}
		if len(required) != 0 {
			s["required"] = required
		}
		return s
	}

	label := obj(map[string]any{
		"id": str(), "name": str(), "color": str(), "label_group": nullable("string"),
	}, "id", "name", "color", "label_group")
	creator := obj(map[string]any{
		"type": str(), "id": str(), "name": str(),
	}, "type", "id")
	issue := obj(map[string]any{
		"id": str(), "workspace_id": str(), "crew_id": str(),
		"crew_name": str(), "crew_slug": str(), "number": nullable("integer"),
		"identifier": nullable("string"), "title": str(), "description": nullable("string"),
		"status": str(), "priority": str(), "assignee_type": nullable("string"),
		"assignee_id": nullable("string"), "assignee_name": nullable("string"),
		"due_date": nullable("string"), "sort_order": number(), "mission_type": str(),
		"lead_agent_id": str(), "created_at": str(), "updated_at": str(),
		"completed_at": nullable("string"), "labels": arrayOf(ref("Label")),
		"project_id": nullable("string"), "project_name": nullable("string"),
		"estimate": nullable("integer"), "parent_issue_id": nullable("string"),
		"milestone_id": nullable("string"), "sub_issues_count": integer(),
		"comment_count": integer(), "routine_id": nullable("string"),
		"routine_slug": nullable("string"), "routine_name": nullable("string"),
		"created_by": ref("IssueCreator"), "authored_via": nullable("string"),
	}, "id", "workspace_id", "crew_id", "title", "status", "priority", "sort_order", "mission_type", "lead_agent_id", "created_at", "updated_at", "labels",
		"number", "identifier", "description", "assignee_type", "assignee_id", "due_date", "completed_at",
		"project_id", "estimate", "parent_issue_id", "milestone_id", "sub_issues_count", "comment_count")

	installedAgent := obj(map[string]any{
		"agent_id": str(), "agent_slug": str(), "agent_name": str(),
		"avatar_seed": nullable("string"), "avatar_style": nullable("string"), "avatar_url": nullable("string"),
		"crew_id": nullable("string"), "crew_slug": nullable("string"), "crew_name": nullable("string"),
		"crew_color": nullable("string"), "crew_icon": nullable("string"), "crew_avatar_style": nullable("string"),
	}, "agent_id", "agent_slug", "agent_name")
	skill := obj(map[string]any{
		"id": str(), "name": str(), "slug": str(), "display_name": str(), "description": nullable("string"),
		"version": str(), "author": nullable("string"), "category": str(), "source": str(), "icon": nullable("string"),
		"verification": str(), "downloads": integer(), "rating_avg": nullable("number"), "rating_count": integer(),
		"tags": nullable("string"), "featured": boolean(), "pricing_tier": str(), "tool_count": nullable("integer"),
		"vendor": nullable("string"), "homepage": nullable("string"), "spdx_license": nullable("string"),
		"runtime": str(), "maturity": str(), "scan_status": str(), "description_quality": nullable("string"),
		"created_at": str(), "updated_at": str(), "installed_on": arrayOf(ref("InstalledSkillAgent")),
	}, "id", "name", "slug", "display_name", "version", "category", "source", "verification", "downloads", "rating_count", "featured", "pricing_tier", "runtime", "maturity", "scan_status", "created_at", "updated_at",
		"description", "author", "icon", "rating_avg", "tags", "tool_count", "vendor", "homepage", "spdx_license", "description_quality")
	skillDetail := obj(map[string]any{
		"id": str(), "name": str(), "slug": str(), "display_name": str(), "description": nullable("string"),
		"version": str(), "author": nullable("string"), "category": str(), "source": str(), "icon": nullable("string"),
		"verification": str(), "downloads": integer(), "rating_avg": nullable("number"), "rating_count": integer(),
		"tags": nullable("string"), "featured": boolean(), "pricing_tier": str(), "tool_count": nullable("integer"),
		"vendor": nullable("string"), "homepage": nullable("string"), "spdx_license": nullable("string"),
		"runtime": str(), "maturity": str(), "scan_status": str(), "description_quality": nullable("string"),
		"created_at": str(), "updated_at": str(), "installed_on": arrayOf(ref("InstalledSkillAgent")),
		"content": nullable("string"), "credential_requirements": nullable("string"), "mcp_server_command": nullable("string"),
		"mcp_server_image": nullable("string"), "mcp_transport": nullable("string"), "dependencies": nullable("string"),
		"license": nullable("string"), "agent_count": integer(), "security_score": nullable("integer"),
		"allowed_domains": nullable("string"), "changelog": nullable("string"),
	}, "id", "name", "slug", "display_name", "version", "category", "source", "verification", "downloads", "rating_count", "featured", "pricing_tier", "runtime", "maturity", "scan_status", "created_at", "updated_at", "agent_count")

	credential := obj(map[string]any{
		"id": str(), "name": str(), "description": nullable("string"), "type": str(), "provider": str(), "status": str(), "scope": str(),
		"crew_id": nullable("string"), "crew_ids": stringArray(), "account_label": nullable("string"), "account_email": nullable("string"),
		"username": nullable("string"), "endpoint_url": nullable("string"), "testable": boolean(), "sensitivity": str(),
		"security_level": integer(), "security_level_label": str(), "token_expires_at": nullable("string"), "last_checked_at": nullable("string"),
		"last_error": nullable("string"), "last_used_at": nullable("string"), "last_used_ips": stringArray(), "tags": stringArray(),
		"created_at": str(), "updated_at": str(), "_count_agent_credentials": integer(), "agent_names": stringArray(), "agent_ids": stringArray(), "mcp_used": boolean(),
		"created_by_actor_type": nullable("string"), "created_by_actor_id": nullable("string"), "provisioned_for_service": nullable("string"),
	}, "id", "name", "type", "provider", "status", "scope", "crew_ids", "testable", "sensitivity", "security_level", "security_level_label", "last_used_ips", "tags", "created_at", "updated_at", "_count_agent_credentials", "agent_names", "agent_ids", "mcp_used",
		"description", "crew_id", "account_label", "account_email", "username", "token_expires_at",
		"last_checked_at", "last_error", "last_used_at", "created_by_actor_type", "created_by_actor_id", "provisioned_for_service")

	credentialField := obj(map[string]any{
		"key": str(), "is_secret": boolean(), "ordinal": integer(), "value": nullable("string"), "created_at": str(), "updated_at": str(),
	}, "key", "is_secret", "ordinal", "value", "created_at", "updated_at")
	credentialBinding := obj(map[string]any{
		"id": str(), "credential_id": str(), "credential_name": str(), "scope": str(), "crew_id": nullable("string"),
		"agent_id": nullable("string"), "slot": str(), "created_at": str(),
	}, "id", "credential_id", "credential_name", "scope", "slot", "created_at", "crew_id", "agent_id")
	agentCredential := obj(map[string]any{
		"id": str(), "agent_id": str(), "credential_id": str(), "credential_name": str(), "credential_type": str(),
		"credential_provider": str(), "credential_status": str(), "env_var_name": str(), "priority": integer(), "created_at": str(),
		"expires_at": str(), "expired": boolean(), "lease_source": str(), "lease_issued_at": str(), "grant_source": str(),
	}, "id", "agent_id", "credential_id", "credential_name", "credential_type", "credential_provider", "credential_status", "env_var_name", "priority", "created_at", "expired", "grant_source")

	request := func(properties map[string]any, required ...string) map[string]any {
		return obj(properties, required...)
	}
	issueCreate := request(map[string]any{
		"title": str(), "description": nullable("string"), "priority": str(), "assignee_type": nullable("string"), "assignee_id": nullable("string"),
		"due_date": nullable("string"), "project_id": nullable("string"), "estimate": nullable("integer"), "parent_issue_id": nullable("string"),
		"milestone_id": nullable("string"), "labels": stringArray(), "routine_id": nullable("string"), "routine_inputs": stringMap,
	}, "title")
	issueUpdate := request(map[string]any{
		"title": nullable("string"), "description": nullable("string"), "status": nullable("string"), "priority": nullable("string"),
		"assignee_type": nullable("string"), "assignee_id": nullable("string"), "due_date": nullable("string"), "project_id": nullable("string"),
		"estimate": nullable("integer"), "parent_issue_id": nullable("string"), "milestone_id": nullable("string"), "sort_order": nullable("number"),
		"labels": arrayOf(stringArray()["items"].(map[string]any)), "routine_id": nullable("string"), "routine_inputs": stringMap,
	})
	issueBulk := request(map[string]any{
		"ids": stringArray(), "updates": obj(map[string]any{"status": nullable("string"), "priority": nullable("string"), "assignee_type": nullable("string"), "assignee_id": nullable("string"), "project_id": nullable("string"), "labels": stringArray()}, "status", "priority", "assignee_type", "assignee_id", "project_id", "labels"),
	}, "ids", "updates")
	labelCreate := request(map[string]any{"name": str(), "color": str(), "label_group": nullable("string")}, "name", "color")
	labelUpdate := request(map[string]any{"name": nullable("string"), "color": nullable("string"), "label_group": nullable("string")})
	credentialCreate := request(map[string]any{
		"name": str(), "description": nullable("string"), "value": str(), "type": str(), "provider": str(), "scope": str(), "crew_id": nullable("string"),
		"crew_ids": stringArray(), "tags": stringArray(), "account_label": nullable("string"), "account_email": nullable("string"), "refresh_token": nullable("string"),
		"token_expires_at": nullable("string"), "security_level": nullable("integer"), "created_by_actor_type": nullable("string"), "created_by_actor_id": nullable("string"),
		"provisioned_for_service": nullable("string"), "username": nullable("string"), "oauth_client_id": nullable("string"), "oauth_client_secret": nullable("string"),
		"oauth_auth_url": nullable("string"), "oauth_token_url": nullable("string"), "oauth_scopes": nullable("string"), "pending": boolean(),
	}, "name", "value")
	credentialFieldRequest := request(map[string]any{"key": str(), "value": str(), "is_secret": boolean(), "ordinal": integer()}, "value")
	credentialBindingRequest := request(map[string]any{"credential_id": str(), "scope": str(), "crew_id": str(), "agent_id": str(), "slot": str()}, "credential_id", "scope", "slot")
	rotationRequest := request(map[string]any{"value": str(), "grace_seconds": integer(), "endpoint_base_url": str(), "endpoint_auth_token": str(), "endpoint_headers": map[string]any{"type": "object", "additionalProperties": str()}}, "value")
	skillImport := request(map[string]any{"url": str(), "content": str(), "allow_unsafe_license": boolean()})

	return map[string]any{
		"Issue": issue, "IssueList": arrayOf(ref("Issue")), "IssueCreator": creator,
		"Label": label, "LabelList": arrayOf(ref("Label")),
		"Skill": skill, "SkillDetail": skillDetail, "SkillList": arrayOf(ref("Skill")), "InstalledSkillAgent": installedAgent,
		"Credential": credential, "CredentialList": arrayOf(ref("Credential")),
		"CredentialPage":  obj(map[string]any{"credentials": arrayOf(ref("Credential")), "next_cursor": nullable("string"), "limit": integer()}, "credentials", "limit"),
		"CredentialField": credentialField, "CredentialFieldList": arrayOf(ref("CredentialField")),
		"CredentialBinding": credentialBinding, "CredentialBindingList": obj(map[string]any{"bindings": arrayOf(ref("CredentialBinding"))}, "bindings"),
		"AgentCredential": agentCredential, "AgentCredentialList": arrayOf(ref("AgentCredential")),
		"IssueCreateRequest": issueCreate, "IssueUpdateRequest": issueUpdate, "IssueBulkUpdateRequest": issueBulk,
		"LabelCreateRequest": labelCreate, "LabelUpdateRequest": labelUpdate,
		"CredentialCreateRequest": credentialCreate, "CredentialFieldRequest": credentialFieldRequest,
		"CredentialBindingRequest": credentialBindingRequest, "CredentialRotationRequest": rotationRequest,
		"SkillImportRequest": skillImport,
	}
}
