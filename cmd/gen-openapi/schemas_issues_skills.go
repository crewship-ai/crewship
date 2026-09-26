package main

// DomainSchemaMap returns the OpenAPI component schemas for the issue,
// label, skill, and credential surfaces.  The map is deliberately kept in a
// separate file from the source scanner: these contracts are derived from
// the API handler response/request types, while route discovery remains
// source based.
//
// The returned value is a fresh map and may be changed by callers.
func issueSkillCredentialSchemaComponents() map[string]any {
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
	// issueOwner / issueDelegate (A10, invariant I5 — "delegating to an
	// agent never changes the human owner"): the typed projection of
	// missions.owner_user_id / delegate_agent_id, independent of each
	// other and of the legacy assignee_type/assignee_id pair the "issue"
	// schema below still carries for the migration window. Both omitempty
	// on issueResponse (nil when nobody occupies that slot), so neither is
	// in "issue"'s required list.
	issueOwner := obj(map[string]any{
		"id": str(), "name": str(),
	}, "id")
	issueDelegate := obj(map[string]any{
		"id": str(), "name": str(),
	}, "id")
	issue := obj(map[string]any{
		"work_mode": str(), "work_revision": integer(), "worker_user_id": nullable("string"), "worker_name": nullable("string"), "work_note": str(), "work_stopping": boolean(),
		// brief_revision / client_review_required ride every issue row
		// (no omitempty on issueResponse since #2448); the schema has to
		// admit them or every response violates the contract (#2623).
		"brief_revision": integer(), "client_review_required": boolean(),
		"id": str(), "workspace_id": str(), "crew_id": str(),
		"crew_name": str(), "crew_slug": str(), "number": nullable("integer"),
		"identifier": nullable("string"), "title": str(), "description": nullable("string"),
		"status": str(), "priority": str(), "assignee_type": nullable("string"),
		"assignee_id": nullable("string"), "assignee_name": nullable("string"),
		"owner": ref("IssueOwner"), "delegate": ref("IssueDelegate"),
		"due_date": nullable("string"), "sort_order": number(), "mission_type": str(),
		"lead_agent_id": str(), "created_at": str(), "updated_at": str(),
		"completed_at": nullable("string"), "labels": arrayOf(ref("Label")),
		"project_id": nullable("string"), "project_name": nullable("string"),
		"estimate": nullable("integer"), "parent_issue_id": nullable("string"),
		"milestone_id": nullable("string"), "sub_issues_count": integer(),
		"comment_count": integer(), "routine_id": nullable("string"),
		"routine_slug": nullable("string"), "routine_name": nullable("string"),
		"routine_inputs": map[string]any{"type": "object", "additionalProperties": true},
		"created_by":     ref("IssueCreator"), "authored_via": nullable("string"),
		// Optional projections on issueResponse: assignee_slug (agent
		// assignee's page key), code_links (agent-facing internal read
		// only — browsers read …/code-links), execution (#2448 durable
		// execution snapshot). All omitempty, none required.
		"assignee_slug": nullable("string"),
		"code_links": arrayOf(object(map[string]any{
			"url": str(), "provider": str(), "state": str(), "details": str(),
		}, "url", "provider")),
		"execution": func() map[string]any {
			s := object(map[string]any{
				"id": str(), "stage": str(), "attempt": integer(), "reviewer": str(), "note": str(),
				"routine_run_id": str(),
				"workers": arrayOf(object(map[string]any{
					"assignment_id": str(), "name": str(), "status": str(),
				}, "assignment_id", "name", "status")),
			}, "id", "stage", "attempt", "reviewer", "note", "workers")
			s["nullable"] = true
			return s
		}(),
	}, "id", "workspace_id", "crew_id", "title", "status", "priority", "sort_order", "mission_type", "lead_agent_id", "created_at", "updated_at", "labels",
		"number", "identifier", "description", "assignee_type", "assignee_id", "due_date", "completed_at",
		"project_id", "estimate", "parent_issue_id", "milestone_id", "sub_issues_count", "comment_count", "work_mode", "work_revision", "work_note", "work_stopping", "client_review_required")

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
	}, "id", "name", "slug", "display_name", "version", "category", "source", "verification", "downloads", "rating_count", "featured", "pricing_tier", "runtime", "maturity", "scan_status", "created_at", "updated_at",
		"description", "author", "icon", "rating_avg", "tags", "tool_count", "vendor", "homepage", "spdx_license", "description_quality",
		"content", "credential_requirements", "mcp_server_command", "mcp_server_image", "mcp_transport", "dependencies", "license", "agent_count", "security_score", "allowed_domains", "changelog")

	credential := obj(map[string]any{
		"id": str(), "name": str(), "description": nullable("string"), "type": str(), "provider": str(), "status": str(), "scope": str(),
		"crew_id": nullable("string"), "crew_ids": stringArray(), "account_label": nullable("string"), "account_email": nullable("string"),
		"username": nullable("string"), "endpoint_url": nullable("string"), "testable": boolean(), "sensitivity": str(),
		"security_level": integer(), "security_level_label": str(), "token_expires_at": nullable("string"), "last_checked_at": nullable("string"),
		"last_error": nullable("string"), "last_used_at": nullable("string"), "last_used_ips": stringArray(), "tags": stringArray(),
		"created_at": str(), "updated_at": str(), "_count_agent_credentials": integer(), "agent_names": stringArray(), "agent_ids": stringArray(), "mcp_used": boolean(),
		"created_by_actor_type": nullable("string"), "created_by_actor_id": nullable("string"), "provisioned_for_service": nullable("string"),
		"login": ref("ProviderLogin"),
	}, "id", "name", "type", "provider", "status", "scope", "crew_ids", "testable", "sensitivity", "security_level", "security_level_label", "last_used_ips", "tags", "created_at", "updated_at", "_count_agent_credentials", "agent_names", "agent_ids", "mcp_used",
		"description", "crew_id", "account_label", "account_email", "username", "token_expires_at",
		"last_checked_at", "last_error", "last_used_at", "created_by_actor_type", "created_by_actor_id", "provisioned_for_service")

	// Provider login (docs/prd/provider-logins.md §10.1): the seat a model is
	// paid with, on PROVIDER_LOGIN rows and derived on AI_CLI_TOKEN / API_KEY
	// rows of a model provider. Absent on every other credential.
	providerLoginRefresh := obj(map[string]any{
		"supported": boolean(), "status": str(), "last_at": nullable("string"), "next_at": nullable("string"), "error": nullable("string"),
	}, "supported", "status", "last_at", "next_at", "error")
	providerLoginQuota := map[string]any{"type": "object", "nullable": true, "properties": map[string]any{
		"window_5h_pct": integer(), "window_weekly_pct": integer(), "resets_at": str(),
	}}
	providerLoginDelivery := obj(map[string]any{"kind": str(), "target": str()}, "kind", "target")
	providerLoginPaysFor := obj(map[string]any{"agents": integer(), "crews": integer()}, "agents", "crews")
	providerLogin := obj(map[string]any{
		"mode": str(), "provider": str(), "plan": nullable("string"), "plan_label": nullable("string"),
		"owner_user_id": nullable("string"), "owner_email": nullable("string"), "expires_at": nullable("string"),
		"refresh": providerLoginRefresh, "quota": providerLoginQuota, "delivery": providerLoginDelivery, "pays_for": providerLoginPaysFor,
	}, "mode", "provider", "plan", "plan_label", "owner_user_id", "owner_email", "expires_at", "refresh", "quota", "delivery", "pays_for")
	providerLoginRefreshResponse := obj(map[string]any{"login": ref("ProviderLogin")}, "login")

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

	return map[string]any{
		"Issue": issue, "IssueList": arrayOf(ref("Issue")), "IssueCreator": creator,
		"IssueOwner": issueOwner, "IssueDelegate": issueDelegate,
		"Label": label, "LabelList": arrayOf(ref("Label")),
		"Skill": skill, "SkillDetail": skillDetail, "SkillList": arrayOf(ref("Skill")), "InstalledSkillAgent": installedAgent,
		"Credential": credential, "CredentialList": arrayOf(ref("Credential")),
		"ProviderLogin": providerLogin, "ProviderLoginRefreshResponse": providerLoginRefreshResponse,
		"CredentialPage":  obj(map[string]any{"credentials": arrayOf(ref("Credential")), "next_cursor": nullable("string"), "limit": integer()}, "credentials", "next_cursor", "limit"),
		"CredentialField": credentialField, "CredentialFieldList": arrayOf(ref("CredentialField")),
		"CredentialBinding": credentialBinding, "CredentialBindingList": obj(map[string]any{"bindings": arrayOf(ref("CredentialBinding"))}, "bindings"),
		"AgentCredential": agentCredential, "AgentCredentialList": arrayOf(ref("AgentCredential")),
		// The issue, label, credential and skill request bodies are owned by
		// schemas_request_core_resources_v2.go (Core…RequestV2).
	}
}
