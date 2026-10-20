package main

// The projection extends two existing read operations. Their legacy responses
// stay intact; presence of contract_version selects the explicit source DTO.
func chatSourceSchemaComponents() map[string]any {
	str := func() map[string]any { return map[string]any{"type": "string"} }
	integer := func(min int) map[string]any {
		return map[string]any{"type": "integer", "format": "int64", "minimum": min}
	}
	object := func(properties map[string]any, required ...string) map[string]any {
		schema := map[string]any{"type": "object", "properties": properties, "additionalProperties": false}
		if len(required) > 0 {
			schema["required"] = required
		}
		return schema
	}
	nullable := func(schema map[string]any) map[string]any { schema["nullable"] = true; return schema }
	_, rooms := workspaceConversationSchemaCatalog()
	schemas := map[string]any{
		"ChatSourceConversationRef": object(map[string]any{"workspace_id": str(), "source_kind": map[string]any{"type": "string", "enum": []string{"agent_session", "workspace_room"}}, "source_id": str()}, "workspace_id", "source_kind", "source_id"),
		"ChatSourceMessageRef":      nullable(object(map[string]any{"conversation_ref": ref("ChatSourceConversationRef"), "source_message_id": str()}, "conversation_ref", "source_message_id")),
		"ChatSourceAuthor":          nullable(object(map[string]any{"user_id": str(), "agent_id": str()})),
		"ChatSourceOrder":           object(map[string]any{"source_sequence": integer(1), "source_position": integer(0)}),
		// The read projection always removes Part.Metadata, including from thinking.
		"ChatSourcePart": object(map[string]any{"type": str(), "content": str(), "tool_name": str(), "tool_id": str()}, "type"),
		"ChatSourceMessage": object(map[string]any{
			"message_ref": ref("ChatSourceMessageRef"), "role": nullable(str()), "author": ref("ChatSourceAuthor"), "occurred_at": nullable(map[string]any{"type": "string", "format": "date-time"}),
			"order": ref("ChatSourceOrder"), "reply_to": ref("ChatSourceMessageRef"), "thread_root": ref("ChatSourceMessageRef"), "alternative_parent": ref("ChatSourceMessageRef"), "content": str(), "parts": map[string]any{"type": "array", "items": ref("ChatSourcePart")},
		}, "message_ref", "role", "author", "occurred_at", "order", "reply_to", "thread_root", "alternative_parent", "content", "parts"),
		"ChatSourceMessages": object(map[string]any{
			"contract_version": map[string]any{"type": "integer", "enum": []int{1}}, "conversation_ref": ref("ChatSourceConversationRef"), "messages": map[string]any{"type": "array", "maxItems": 1000, "items": ref("ChatSourceMessage")}, "snapshot_complete": map[string]any{"type": "boolean"}, "has_more": map[string]any{"type": "boolean"},
		}, "contract_version", "conversation_ref", "messages"),
		"WorkspaceConversationMessages": chatSourceResponseVariants(rooms["WorkspaceConversationMessages"].(map[string]any)),
	}
	return schemas
}

func chatSourceResponseVariants(legacy map[string]any) map[string]any {
	// Without this exclusion, permissive legacy objects overlap source_v1 and
	// also let a future/invalid version bypass validation through that branch.
	return map[string]any{"oneOf": []any{
		map[string]any{"allOf": []any{legacy, map[string]any{"not": map[string]any{"required": []string{"contract_version"}}}}},
		ref("ChatSourceMessages"),
	}}
}

func chatSourceSchemaRoutes() map[string]DomainSchema {
	const agent = "GET /api/v1/chats/{chatId}/messages"
	var legacy map[string]any
	for _, domain := range publicActivitySchemaCatalog() {
		if value, ok := domain[agent]; ok {
			legacy = value.Response
		}
	}
	if legacy == nil {
		panic("legacy chat source response missing")
	}
	parameters := []map[string]any{{"name": "projection", "in": "query", "required": false, "schema": map[string]any{"type": "string", "enum": []string{"source_v1"}}}}
	return map[string]DomainSchema{
		agent: {Response: chatSourceResponseVariants(legacy), Parameters: parameters},
		"GET /api/v1/conversations/{conversationId}/messages": {Parameters: parameters},
	}
}
