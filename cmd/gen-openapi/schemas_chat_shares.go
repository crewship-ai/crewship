package main

// chatShareSchemaCatalog documents the four routes for a single revocable,
// read-only chat capability. The reader's dedicated bearer security scheme is
// installed in buildDocument; these schemas pin its request and response wire
// shapes without broadening ordinary session authorization.
func chatShareSchemaCatalog() (map[string]DomainSchema, map[string]any) {
	str := func() map[string]any { return map[string]any{"type": "string"} }
	dateTime := func() map[string]any { return map[string]any{"type": "string", "format": "date-time"} }
	ref := func(name string) map[string]any { return map[string]any{"$ref": "#/components/schemas/" + name} }
	array := func(item map[string]any) map[string]any { return map[string]any{"type": "array", "items": item} }
	object := func(props map[string]any, required ...string) map[string]any {
		schema := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
		if len(required) > 0 {
			schema["required"] = required
		}
		return schema
	}
	path := func(name string) map[string]any {
		return map[string]any{"name": name, "in": "path", "required": true, "schema": map[string]any{"type": "string", "minLength": 1}}
	}
	noStore := map[string]any{"Cache-Control": map[string]any{"description": "Share responses are not cacheable.", "schema": map[string]any{"type": "string", "enum": []string{"no-store"}}}}
	managementParams := []map[string]any{path("agentId"), path("chatId")}
	readerPath := "/api/v1/shared-chats/{shareId}/messages"
	managementPath := "/api/v1/agents/{agentId}/chats/{chatId}/shares"

	components := map[string]any{
		"ChatShareGrant": object(map[string]any{
			"id": str(), "workspace_id": str(), "agent_id": str(), "chat_id": str(),
			"issued_by_user_id": str(), "created_at": dateTime(), "expires_at": dateTime(),
			"revoked_at": dateTime(),
		}, "id", "workspace_id", "agent_id", "chat_id", "issued_by_user_id", "created_at", "expires_at"),
		"ChatShareCreateRequest": object(map[string]any{
			"ttl_seconds": map[string]any{"type": "integer", "format": "int64", "minimum": 0, "maximum": 604800,
				"default": 86400, "description": "Omit or set to 0 for the 24-hour default. Maximum: seven days."},
		}),
		"ChatShareCreateResponse": object(map[string]any{
			"share": ref("ChatShareGrant"), "token": map[string]any{"type": "string", "pattern": "^cshr_[A-Za-z0-9_-]{43}$", "description": "Plaintext token returned once; absent from list responses."},
		}, "share", "token"),
		"ChatShareListResponse": object(map[string]any{"shares": array(ref("ChatShareGrant"))}, "shares"),
		"ChatShareTextMessage": object(map[string]any{
			"id": str(), "role": map[string]any{"type": "string", "enum": []string{"user", "assistant"}},
			"content": str(), "created_at": dateTime(),
		}, "id", "role", "content", "created_at"),
		"ChatShareMessagesResponse": object(map[string]any{"messages": array(ref("ChatShareTextMessage"))}, "messages"),
	}
	return map[string]DomainSchema{
		"POST " + managementPath: {
			Request: ref("ChatShareCreateRequest"), Response: ref("ChatShareCreateResponse"),
			Parameters: managementParams, SuccessStatuses: []string{"201"}, SuccessHeaders: noStore,
		},
		"GET " + managementPath: {
			Response: ref("ChatShareListResponse"), Parameters: managementParams,
			SuccessStatuses: []string{"200"}, SuccessHeaders: noStore,
		},
		"DELETE " + managementPath + "/{shareId}": {
			Parameters:      []map[string]any{path("agentId"), path("chatId"), path("shareId")},
			SuccessStatuses: []string{"204"}, SuccessHeaders: noStore,
		},
		"GET " + readerPath: {
			Response: ref("ChatShareMessagesResponse"), Parameters: []map[string]any{path("shareId")},
			SuccessStatuses: []string{"200"}, SuccessHeaders: noStore,
		},
	}, components
}
