package main

// workspaceConversationSchemaCatalog mirrors the agent-independent human
// conversation store and its HTTP envelopes. Private groups and workspace
// channels share schemas; agent membership/dispatch is channel-only.
func workspaceConversationSchemaCatalog() (map[string]DomainSchema, map[string]any) {
	str := func() map[string]any { return map[string]any{"type": "string"} }
	enum := func(values ...string) map[string]any { return map[string]any{"type": "string", "enum": values} }
	integer := func() map[string]any { return map[string]any{"type": "integer", "format": "int64", "minimum": 0} }
	boolean := func() map[string]any { return map[string]any{"type": "boolean"} }
	ref := func(name string) map[string]any { return map[string]any{"$ref": "#/components/schemas/" + name} }
	array := func(item map[string]any) map[string]any { return map[string]any{"type": "array", "items": item} }
	object := func(properties map[string]any, required ...string) map[string]any {
		s := map[string]any{"type": "object", "properties": properties}
		if len(required) > 0 {
			s["required"] = required
		}
		return s
	}
	request := func(properties map[string]any, required ...string) map[string]any {
		s := object(properties, required...)
		s["additionalProperties"] = false
		return s
	}
	schemas := map[string]any{
		"WorkspaceConversationActivity": request(map[string]any{"issues": boolean(), "routines": boolean()}, "issues", "routines"),
		"WorkspaceConversation": object(map[string]any{
			"id": str(), "workspace_id": str(), "kind": enum("channel", "group"), "title": str(), "created_by": str(), "created_at": str(), "updated_at": str(),
			"last_sequence": integer(), "last_read_sequence": integer(), "access_scope": enum("workspace", "participants"), "unread_count": integer(), "muted": boolean(), "is_direct": boolean(), "direct_user_id": str(), "direct_user_name": str(), "direct_avatar_url": str(),
		}, "id", "workspace_id", "kind", "title", "created_by", "created_at", "updated_at", "last_sequence", "last_read_sequence", "access_scope", "unread_count", "muted", "is_direct"),
		"WorkspaceConversationMessage": object(map[string]any{
			"source_kind": enum("activity"),
			"id":          str(), "conversation_id": str(), "sequence": integer(), "author_user_id": str(), "author_agent_id": str(), "author_name": str(), "author_avatar_url": str(), "author_avatar_style": str(), "author_avatar_seed": str(), "author_slug": str(), "client_id": str(), "content": str(), "created_at": str(),
			"kind": enum("message", "agent_joined", "agent_left"), "subject_agent_id": str(), "mentioned_agent_ids": map[string]any{"type": "array", "items": str(), "nullable": true},
		}, "id", "conversation_id", "sequence", "author_user_id", "author_name", "client_id", "content", "created_at", "kind", "mentioned_agent_ids"),
		"WorkspaceConversationParticipant":     object(map[string]any{"user_id": str(), "name": str(), "avatar_url": str(), "joined_at": str(), "role": enum("owner", "member")}, "user_id", "name", "joined_at", "role"),
		"WorkspaceConversationAgent":           object(map[string]any{"agent_id": str(), "name": str(), "slug": str(), "avatar_url": str(), "avatar_style": str(), "avatar_seed": str(), "joined_at": str()}, "agent_id", "name", "slug", "joined_at"),
		"WorkspaceConversationAgentJob":        object(map[string]any{"id": str(), "agent_id": str(), "message_id": str(), "state": enum("pending", "queued", "running", "completed", "failed"), "error": str(), "assignment_id": str()}, "id", "agent_id", "message_id", "state", "error", "assignment_id"),
		"WorkspaceConversationList":            object(map[string]any{"conversations": array(ref("WorkspaceConversation")), "next_offset": map[string]any{"type": "integer", "minimum": 0, "nullable": true}}, "conversations", "next_offset"),
		"WorkspaceConversationMessages":        object(map[string]any{"messages": array(ref("WorkspaceConversationMessage")), "has_more": boolean()}, "messages", "has_more"),
		"WorkspaceConversationParticipants":    object(map[string]any{"participants": array(ref("WorkspaceConversationParticipant"))}, "participants"),
		"WorkspaceConversationAgents":          object(map[string]any{"agents": array(ref("WorkspaceConversationAgent"))}, "agents"),
		"WorkspaceConversationAgentJobs":       object(map[string]any{"jobs": array(ref("WorkspaceConversationAgentJob"))}, "jobs"),
		"WorkspaceConversationContinueRequest": request(map[string]any{"kind": enum("group", "channel"), "title": map[string]any{"type": "string", "minLength": 1, "maxLength": 120}, "member_ids": map[string]any{"type": "array", "items": str(), "maxItems": 498}, "agent_id": str(), "client_id": map[string]any{"type": "string", "minLength": 1, "description": "Stable retry identity, at most128 UTF-8 bytes. New room has fresh history; channel is workspace-visible."}}, "kind", "title", "client_id"),
		"WorkspaceConversationDirectRequest":   request(map[string]any{"user_id": str()}, "user_id"),
		"WorkspaceConversationCreateRequest":   request(map[string]any{"title": map[string]any{"type": "string", "minLength": 1, "maxLength": 120}, "kind": enum("channel", "group"), "member_ids": map[string]any{"type": "array", "items": str(), "maxItems": 500}}, "title", "kind"),
		"WorkspaceConversationSendRequest":     request(map[string]any{"client_id": map[string]any{"type": "string", "minLength": 1, "description": "Retry identity, at most 128 UTF-8 bytes; reuse only with identical content and mentions."}, "content": map[string]any{"type": "string", "minLength": 1, "description": "Nonblank text, at most 32768 UTF-8 bytes."}, "mentioned_agent_ids": map[string]any{"type": "array", "items": str(), "maxItems": 10, "description": "Explicit channel agent mentions; agents must already be members. Private groups reject agent mentions."}}, "client_id", "content"),
		// Missing last_read_sequence decodes to zero and is accepted by the handler.
		"WorkspaceConversationReadRequest":           request(map[string]any{"last_read_sequence": integer()}),
		"WorkspaceConversationMuteRequest":           request(map[string]any{"muted": boolean()}, "muted"),
		"WorkspaceConversationAddParticipantRequest": request(map[string]any{"user_id": str()}, "user_id"),
		"WorkspaceConversationAddAgentRequest":       request(map[string]any{"agent_id": str()}, "agent_id"),
	}
	routes := map[string]DomainSchema{}
	base := "/api/v1/conversations"
	add := func(method, path, requestName, responseName string, statuses ...string) {
		s := DomainSchema{SuccessStatuses: statuses}
		if requestName != "" {
			s.Request = ref(requestName)
		}
		if responseName != "" {
			s.Response = ref(responseName)
		}
		routes[method+" "+path] = s
	}
	add("GET", base, "", "WorkspaceConversationList", "200")
	add("POST", base, "WorkspaceConversationCreateRequest", "WorkspaceConversation", "201")
	add("POST", base+"/direct", "WorkspaceConversationDirectRequest", "WorkspaceConversation", "200", "201")
	base += "/{conversationId}"
	add("GET", base, "", "WorkspaceConversation", "200")
	add("POST", base+"/continue", "WorkspaceConversationContinueRequest", "WorkspaceConversation", "200", "201")
	add("GET", base+"/activity", "", "WorkspaceConversationActivity", "200")
	add("PUT", base+"/activity", "WorkspaceConversationActivity", "WorkspaceConversationActivity", "200")
	add("GET", base+"/messages", "", "WorkspaceConversationMessages", "200")
	add("POST", base+"/messages", "WorkspaceConversationSendRequest", "WorkspaceConversationMessage", "200", "201")
	add("GET", base+"/participants", "", "WorkspaceConversationParticipants", "200")
	add("GET", base+"/agents", "", "WorkspaceConversationAgents", "200")
	add("GET", base+"/agent-jobs", "", "WorkspaceConversationAgentJobs", "200")
	add("POST", base+"/read", "WorkspaceConversationReadRequest", "", "204")
	add("POST", base+"/mute", "WorkspaceConversationMuteRequest", "", "204")
	add("POST", base+"/participants", "WorkspaceConversationAddParticipantRequest", "", "204")
	add("DELETE", base+"/participants/{userId}", "", "", "204")
	add("POST", base+"/agents", "WorkspaceConversationAddAgentRequest", "", "204")
	add("DELETE", base+"/agents/{agentId}", "", "", "204")
	return routes, schemas
}
