package main

// publicActivitySchemaCatalog contains the public, user-facing activity
// surfaces whose wire shapes are defined by handlers in internal/api.  Keep
// this catalog isolated: activity endpoints are frequently consumed together
// by the dashboard and CLI, and their response envelopes are not interchangeable.
func publicActivitySchemaCatalog() map[string]map[string]DomainSchema {
	str := func() map[string]any { return map[string]any{"type": "string"} }
	integer := func() map[string]any { return map[string]any{"type": "integer"} }
	boolean := func() map[string]any { return map[string]any{"type": "boolean"} }
	anyObject := func() map[string]any { return map[string]any{"type": "object", "additionalProperties": true} }
	nullable := func(schema map[string]any) map[string]any {
		out := map[string]any{}
		for key, value := range schema {
			out[key] = value
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
	request := func(properties map[string]any, required ...string) map[string]any {
		schema := object(properties)
		if len(required) > 0 {
			schema["required"] = required
		}
		return schema
	}

	chat := object(map[string]any{
		"id": str(), "agent_id": str(), "workspace_id": str(), "title": nullable(str()),
		"mode": str(), "status": str(), "message_count": integer(), "started_at": str(),
		"ended_at": nullable(str()), "created_at": str(), "origin": nullable(str()),
		"last_activity_at": str(), "unread_count": integer(),
	})
	message := object(map[string]any{
		"id": str(), "session_id": str(), "role": str(), "content": str(),
		"tool_summary": nullable(str()), "ts": str(), "author_user_id": nullable(str()),
	})
	participant := object(map[string]any{
		"user_id": str(), "email": str(), "full_name": str(), "role": str(), "joined_at": str(),
	})
	reaction := object(map[string]any{"emoji": str(), "count": integer(), "mine": boolean()})
	searchHit := object(map[string]any{
		"id": str(), "session_id": str(), "agent_id": str(), "role": str(), "content": str(),
		"tool_summary": str(), "ts": str(),
	})
	inboxItem := object(map[string]any{
		"id": str(), "workspace_id": str(), "kind": str(), "source_id": str(),
		"target_user_id": str(), "target_role": str(), "title": str(), "body_md": str(),
		"sender_type": str(), "sender_id": str(), "sender_name": str(), "avatar_seed": str(),
		"avatar_style": str(), "avatar_url": str(), "state": str(), "priority": str(),
		"blocking": boolean(), "payload": anyObject(), "read_at": str(), "resolved_at": str(),
		"resolved_by_user_id": str(), "resolved_action": str(), "created_at": str(), "updated_at": str(),
		"second_approver_required": boolean(), "second_approver_by_workspace": boolean(),
		"second_approver_by_tier": boolean(), "security_level_label": str(), "evidence": anyObject(),
	})
	auditEntry := object(map[string]any{
		"id": str(), "workspace_id": str(), "user_id": nullable(str()), "action": str(),
		"entity_type": str(), "entity_id": nullable(str()), "metadata": nullable(str()),
		"ip_address": nullable(str()), "user_agent": nullable(str()), "created_at": str(),
		"user_email": nullable(str()), "user_name": nullable(str()), "entity_name": nullable(str()),
	})
	pagination := object(map[string]any{"page": integer(), "limit": integer(), "total": integer(), "total_pages": integer()})
	webhook := object(map[string]any{
		"id": str(), "workspace_id": str(), "name": str(), "target_pipeline_id": str(),
		"target_pipeline_slug": str(), "target_pipeline_version": nullable(integer()), "token": str(),
		"signing_secret_set": boolean(), "signing_secret": str(), "inputs_template": anyObject(),
		"enabled": boolean(), "rate_limit_per_min": integer(), "last_fired_at": nullable(str()),
		"last_status": str(), "last_run_id": str(), "fire_count": integer(), "created_at": str(), "updated_at": str(),
	})

	chatCreate := request(map[string]any{"session_id": str(), "origin": str()})
	conversationSearch := request(map[string]any{"agent_id": str(), "query": str(), "limit": integer()}, "agent_id", "query")
	inboxPatch := request(map[string]any{"state": str(), "resolved_action": str()}, "state")
	inboxBulk := request(map[string]any{"ids": array(str()), "state": str(), "resolved_action": str()}, "ids", "state")
	participantAdd := request(map[string]any{"user_id": str(), "role": str()}, "user_id")
	reactionAdd := request(map[string]any{"emoji": str()}, "emoji")
	webhookRequest := request(map[string]any{
		"name": str(), "target_pipeline_slug": str(), "target_pipeline_id": str(),
		"target_pipeline_version": nullable(integer()), "signing_secret": str(), "inputs_template": anyObject(),
		"enabled": nullable(boolean()), "rate_limit_per_min": integer(),
	}, "name")

	chatDomain := map[string]DomainSchema{
		"GET /api/v1/agents/{agentId}/chats":                                   {Response: array(chat)},
		"POST /api/v1/agents/{agentId}/chats":                                  {Request: chatCreate, Response: object(map[string]any{"id": str()})},
		"PUT /api/v1/agents/{agentId}/chats/{chatId}/read":                     {Response: object(map[string]any{"chat_id": str(), "last_read_at": str()})},
		"DELETE /api/v1/agents/{agentId}/chats/{chatId}":                       {Response: object(map[string]any{"id": str(), "deleted": boolean()})},
		"GET /api/v1/chats/{chatId}/messages":                                  {Response: object(map[string]any{"messages": array(message)})},
		"GET /api/v1/chats/{chatId}/messages/{messageId}/reactions":            {Response: object(map[string]any{"reactions": array(reaction)})},
		"POST /api/v1/chats/{chatId}/messages/{messageId}/reactions":           {Request: reactionAdd, Response: object(map[string]any{"emoji": str()})},
		"DELETE /api/v1/chats/{chatId}/messages/{messageId}/reactions/{emoji}": {Response: object(map[string]any{"status": str()})},
		"GET /api/v1/chats/{chatId}/participants":                              {Response: object(map[string]any{"participants": array(participant)})},
		"POST /api/v1/chats/{chatId}/participants":                             {Request: participantAdd, Response: participant},
		"POST /api/v1/chats/{chatId}/steer":                                    {Request: anyObject(), Response: anyObject()},
	}
	conversations := map[string]DomainSchema{
		"POST /api/v1/conversations/search": {Request: conversationSearch, Response: object(map[string]any{"hits": array(searchHit), "query": str(), "count": integer()})},
	}
	inbox := map[string]DomainSchema{
		"GET /api/v1/inbox":                  {Response: object(map[string]any{"rows": array(inboxItem), "count": integer(), "unread_count": integer()})},
		"GET /api/v1/inbox/count":            {Response: object(map[string]any{"unread_count": integer()})},
		"GET /api/v1/inbox/{id}":             {Response: inboxItem},
		"PATCH /api/v1/inbox/{id}":           {Request: inboxPatch, Response: object(map[string]any{"id": str(), "state": str()})},
		"POST /api/v1/inbox/bulk":            {Request: inboxBulk, Response: anyObject()},
		"DELETE /api/v1/inbox":               {Response: object(map[string]any{"deleted": integer()})},
		"GET /api/v1/agents/{agentId}/inbox": {Response: object(map[string]any{"approvals_pending": integer(), "assignments_open": integer(), "escalations_open": integer(), "peer_messages": array(anyObject()), "cost_usd_this_month": map[string]any{"type": "number"}, "llm_calls_this_month": integer(), "tokens_used_this_month": integer()})},
	}
	audit := map[string]DomainSchema{
		"GET /api/v1/audit": {Response: object(map[string]any{"data": array(auditEntry), "pagination": pagination})},
	}
	webhooks := map[string]DomainSchema{
		"GET /api/v1/workspaces/{workspaceId}/pipeline-webhooks":  {Response: array(webhook)},
		"POST /api/v1/workspaces/{workspaceId}/pipeline-webhooks": {Request: webhookRequest, Response: webhook},
		"POST /api/v1/webhooks/{token}":                           {Response: object(map[string]any{"run_id": str(), "status": str()})},
		"POST /api/v1/webhooks/{crewId}/{agentId}/trigger":        {Response: anyObject()},
	}
	return map[string]map[string]DomainSchema{
		"public-activity-chats": chatDomain, "public-activity-conversations": conversations,
		"public-activity-inbox": inbox,
		"public-activity-audit": audit, "public-activity-webhooks": webhooks,
	}
}
