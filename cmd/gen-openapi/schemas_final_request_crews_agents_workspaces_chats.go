package main

// finalRequestCrewsAgentsWorkspacesChatsSchemaCatalog contains the last
// generic request bodies on the core user-facing surfaces. The request
// contracts are kept separate from response catalogs because several of these
// operations already have response schemas owned by other audits.
func finalRequestCrewsAgentsWorkspacesChatsSchemaCatalog() (map[string]DomainSchema, map[string]any) {
	str := func() map[string]any { return map[string]any{"type": "string"} }
	integer := func() map[string]any { return map[string]any{"type": "integer"} }
	boolean := func() map[string]any { return map[string]any{"type": "boolean"} }
	nullableString := func() map[string]any { return map[string]any{"type": "string", "nullable": true} }
	array := func(item map[string]any) map[string]any { return map[string]any{"type": "array", "items": item} }
	enum := func(values ...string) map[string]any {
		return map[string]any{"type": "string", "enum": values}
	}
	object := func(properties map[string]any, required ...string) map[string]any {
		schema := map[string]any{"type": "object", "properties": properties}
		if len(required) > 0 {
			schema["required"] = required
		}
		return schema
	}
	ref := func(name string) map[string]any { return map[string]any{"$ref": "#/components/schemas/" + name} }

	empty := object(map[string]any{})
	components := map[string]any{
		"FinalCoreEmptyRequest": empty,
		"FinalCoreCrewMemberRequest": object(map[string]any{
			"user_id": str(), "role": str(),
		}, "user_id"),
		"FinalCoreCrewMemberRoleRequest":      object(map[string]any{"role": str()}),
		"FinalCoreWorkspaceMemberRoleRequest": object(map[string]any{"role": enum("OWNER", "ADMIN", "MANAGER", "MEMBER", "VIEWER")}, "role"),
		"FinalCoreWorkspaceCapabilitiesRequest": object(map[string]any{
			"set": array(str()), "grant": array(str()), "revoke": array(str()), "preset": str(),
		}),
		"FinalCoreAgentPersonaSuggestionRequest": object(map[string]any{"content": str(), "rationale": str()}, "content"),
		"FinalCoreAgentRehireRequest":            object(map[string]any{"ttl_minutes": integer(), "reason": str()}, "reason"),
		"FinalCoreBootstrapRequest": object(map[string]any{
			"full_name": str(), "email": str(), "password": str(),
		}, "full_name", "email", "password"),
		"FinalCoreChatSteerRequest":       object(map[string]any{"message": str()}, "message"),
		"FinalCoreCrewAvatarStyleRequest": object(map[string]any{"avatar_style": str(), "reset_overrides": boolean()}),
		"FinalCoreRefreshToolsRequest": object(map[string]any{
			"tools": array(object(map[string]any{"name": str(), "description": nullableString()})),
		}),
		"FinalCoreIssueCommentRequest": object(map[string]any{"body": str()}, "body"),
		"FinalCoreIssueRelationRequest": object(map[string]any{
			"target_identifier": str(), "relation_type": enum("blocks", "blocked_by", "relates_to", "duplicate_of"),
		}, "target_identifier", "relation_type"),
		"FinalCoreIssueReviewRequest": object(map[string]any{
			"action": enum("approve", "request_changes"), "comment": str(), "reassign_to": nullableString(),
		}, "action"),
		"FinalCorePortRevokeRequest": object(map[string]any{"reason": str()}),
		"FinalCoreInboxBulkRequest": object(map[string]any{
			"ids": array(str()), "state": enum("unread", "read", "resolved"), "resolved_action": str(),
		}, "ids", "state"),
		"FinalCoreInvitationRequest": object(map[string]any{"email": str(), "role": str()}, "email"),
		"FinalCoreProvisionMemberRequest": object(map[string]any{
			"email": str(), "role": str(), "full_name": str(),
		}, "email"),
		"FinalCoreAgentAvatarRequest": object(map[string]any{"svg": str()}, "svg"),
	}

	routes := map[string]DomainSchema{}
	add := func(method, path, component string) {
		routes[method+" "+path] = DomainSchema{Request: ref(component)}
	}
	add("PATCH", "/api/v1/crews/{crewId}/members/{memberId}", "FinalCoreCrewMemberRoleRequest")
	add("PATCH", "/api/v1/workspaces/{workspaceId}/members/{memberId}", "FinalCoreWorkspaceMemberRoleRequest")
	add("PATCH", "/api/v1/workspaces/{workspaceId}/members/{memberId}/capabilities", "FinalCoreWorkspaceCapabilitiesRequest")
	add("POST", "/api/v1/agents/{agentId}/approve-hire", "FinalCoreEmptyRequest")
	add("POST", "/api/v1/agents/{agentId}/persona/suggest", "FinalCoreAgentPersonaSuggestionRequest")
	add("POST", "/api/v1/agents/{agentId}/rehire", "FinalCoreAgentRehireRequest")
	add("POST", "/api/v1/agents/{agentId}/stop", "FinalCoreEmptyRequest")
	add("POST", "/api/v1/agents/{agentId}/webhook-secret/rotate", "FinalCoreEmptyRequest")
	add("POST", "/api/v1/bootstrap", "FinalCoreBootstrapRequest")
	add("POST", "/api/v1/chats/{chatId}/steer", "FinalCoreChatSteerRequest")
	add("POST", "/api/v1/crews/{crewId}/apply-avatar-style", "FinalCoreCrewAvatarStyleRequest")
	add("POST", "/api/v1/crews/{crewId}/integrations/{integrationId}/tools/refresh", "FinalCoreRefreshToolsRequest")
	add("POST", "/api/v1/crews/{crewId}/issues/{identifier}/comments", "FinalCoreIssueCommentRequest")
	add("POST", "/api/v1/crews/{crewId}/issues/{identifier}/relations", "FinalCoreIssueRelationRequest")
	add("POST", "/api/v1/crews/{crewId}/issues/{identifier}/review", "FinalCoreIssueReviewRequest")
	add("POST", "/api/v1/crews/{crewId}/issues/{identifier}/start", "FinalCoreEmptyRequest")
	add("POST", "/api/v1/crews/{crewId}/issues/{identifier}/stop", "FinalCoreEmptyRequest")
	add("POST", "/api/v1/crews/{crewId}/members", "FinalCoreCrewMemberRequest")
	add("POST", "/api/v1/crews/{crewId}/port-expose/{id}/revoke", "FinalCorePortRevokeRequest")
	add("POST", "/api/v1/crews/{crewId}/provision", "FinalCoreEmptyRequest")
	add("POST", "/api/v1/crews/{crewId}/rebuild", "FinalCoreEmptyRequest")
	add("POST", "/api/v1/crews/{crewId}/restart-agents", "FinalCoreEmptyRequest")
	// #1845: refresh-image takes no body — the crew id in the path is the
	// whole request. Declared rather than left to the generic JSON fallback so
	// a client is told there is nothing to send.
	add("POST", "/api/v1/crews/{crewId}/refresh-image", "FinalCoreEmptyRequest")
	// container-start takes no body either: the crew id in the path names
	// everything, and what the container looks like is resolved from the
	// crew row, never from the caller. Declared so a client is told there
	// is nothing to send rather than being left to guess at the fallback.
	add("POST", "/api/v1/crews/{crewId}/container-start", "FinalCoreEmptyRequest")
	add("POST", "/api/v1/crews/{crewId}/container-stop", "FinalCoreEmptyRequest")
	add("POST", "/api/v1/inbox/bulk", "FinalCoreInboxBulkRequest")
	add("POST", "/api/v1/onboarding/complete", "FinalCoreEmptyRequest")
	add("POST", "/api/v1/workspaces/{workspaceId}/invitations", "FinalCoreInvitationRequest")
	add("POST", "/api/v1/workspaces/{workspaceId}/members/provision", "FinalCoreProvisionMemberRequest")
	add("PUT", "/api/v1/agents/{agentId}/avatar", "FinalCoreAgentAvatarRequest")
	add("PUT", "/api/v1/agents/{agentId}/chats/{chatId}/read", "FinalCoreEmptyRequest")
	return routes, components
}
