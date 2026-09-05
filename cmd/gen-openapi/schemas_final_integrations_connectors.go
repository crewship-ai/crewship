package main

// finalIntegrationsConnectorsSchemaCatalog contains the last response shapes
// audited against handlers in the integrations/connectors, notification,
// chat, inbox, webhook, credential-reveal, and self-service user surfaces.
// Keep this in a uniquely named file: this is a coordinator merge target and
// must not be confused with the earlier, broader domain catalogs.
func finalIntegrationsConnectorsSchemaCatalog() (map[string]DomainSchema, map[string]any) {
	str := func() map[string]any { return map[string]any{"type": "string"} }
	integer := func() map[string]any { return map[string]any{"type": "integer"} }
	boolean := func() map[string]any { return map[string]any{"type": "boolean"} }
	anyJSON := func() map[string]any { return map[string]any{} }
	anyObject := func() map[string]any { return map[string]any{"type": "object", "additionalProperties": true} }
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
	array := func(items map[string]any) map[string]any { return map[string]any{"type": "array", "items": items} }
	ref := func(name string) map[string]any { return map[string]any{"$ref": "#/components/schemas/" + name} }

	composioPage := func(item map[string]any, field string) map[string]any {
		return object(map[string]any{"enabled": boolean(), "total": integer(), field: array(item)})
	}
	composioBinding := object(map[string]any{"toolkit": str(), "mode": str(), "user_id": str(), "endpoint": str()})
	channel := object(map[string]any{
		"id": str(), "workspace_id": str(), "type": str(), "url": str(), "to": str(), "events": array(str()),
		"enabled": boolean(), "created_by": str(), "created_at": str(), "provider": str(), "scope": str(),
		"owner_user_id": str(), "categories": array(str()), "min_priority": str(), "secret": str(),
	})
	participant := object(map[string]any{"user_id": str(), "email": str(), "full_name": str(), "role": str(), "joined_at": str()})
	inboxAction := object(map[string]any{
		"id": str(), "label": str(), "effect": str(), "irreversible": boolean(),
	}, "id", "label")
	inboxItem := object(map[string]any{
		"id": str(), "workspace_id": str(), "kind": str(), "source_id": str(), "target_user_id": str(), "source_missing": map[string]any{"type": "boolean"}, "target_role": str(),
		"title": str(), "body_md": str(), "sender_type": str(), "sender_id": str(), "sender_name": str(),
		"avatar_seed": str(), "avatar_style": str(), "avatar_url": str(), "state": str(), "priority": str(), "blocking": boolean(),
		"payload": anyObject(),
		// §12 attention contract (B10, #2364) — see internal/inbox.WriteThreaded.
		"thread_key": str(), "attention_class": str(), "actions": array(inboxAction),
		"read_at": str(), "resolved_at": str(), "resolved_by_user_id": str(), "resolved_action": str(),
		"created_at": str(), "updated_at": str(), "second_approver_required": boolean(), "second_approver_by_workspace": boolean(),
		"second_approver_by_tier": boolean(), "security_level_label": str(), "evidence": anyObject(),
	},
		// Only the fields inboxItemResponse emits unconditionally. Everything
		// else on that struct carries `,omitempty` — target_user_id, body_md,
		// the avatar trio, payload, the resolved_* set, the four-eyes fields —
		// and requiring one of those would fail a row that is perfectly valid.
		"id", "workspace_id", "kind", "source_id", "title", "state", "priority",
		"blocking", "created_at", "updated_at")

	components := map[string]any{
		"FinalComposioInventory":         object(map[string]any{"enabled": boolean(), "auth_configs": array(anyObject()), "users": array(object(map[string]any{"user_id": str(), "connected_accounts": array(anyObject())}))}),
		"FinalComposioToolkits":          composioPage(anyObject(), "toolkits"),
		"FinalComposioTools":             composioPage(anyObject(), "tools"),
		"FinalComposioTriggers":          composioPage(anyObject(), "triggers"),
		"FinalComposioActiveTriggers":    object(map[string]any{"enabled": boolean(), "triggers": array(anyObject())}),
		"FinalComposioTrigger":           object(map[string]any{"enabled": boolean(), "trigger": anyObject()}),
		"FinalComposioBindings":          object(map[string]any{"agent_id": str(), "bindings": array(composioBinding)}),
		"FinalComposioBind":              object(map[string]any{"agent_id": str(), "user_id": str(), "apps": array(object(map[string]any{"toolkit": str(), "mode": str(), "endpoint": str()}))}),
		"FinalComposioSettings":          object(map[string]any{"configured": boolean(), "source": str(), "label": str()}),
		"FinalComposioDefault":           object(map[string]any{"enabled_flag": boolean(), "default_user_id": str(), "default_mcp_server_id": str(), "connected_user_count": integer()}),
		"FinalCredentialReveal":          object(map[string]any{"credential_id": str(), "name": str(), "type": str(), "sensitivity": str(), "value": str(), "revealed_at": str(), "journal_entry_id": str()}),
		"FinalNotificationChannels":      object(map[string]any{"channels": array(channel)}),
		"FinalNotificationChannel":       channel,
		"FinalNotificationTemplates":     object(map[string]any{"templates": array(object(map[string]any{"category": str(), "channel_id": str(), "title": str(), "body": str()}))}),
		"FinalNotificationChannelAgents": object(map[string]any{"agents": array(object(map[string]any{"agent_id": str(), "agent_slug": str()}))}),
		"FinalNotificationChannelAgent":  object(map[string]any{"channel_id": str(), "agent_id": str(), "allowed": boolean()}),
		"FinalChatList":                  array(object(map[string]any{"id": str(), "agent_id": str(), "workspace_id": str(), "title": str(), "mode": str(), "status": str(), "message_count": integer(), "started_at": str(), "ended_at": str(), "created_at": str(), "origin": str(), "last_activity_at": str(), "unread_count": integer()})),
		"FinalChatSteer":                 object(map[string]any{"queued": boolean(), "in_flight": boolean()}),
		"FinalChatParticipants":          object(map[string]any{"participants": array(participant)}),
		"FinalInboxList": object(map[string]any{"rows": array(inboxItem), "count": integer(), "unread_count": integer(), "has_more": map[string]any{"type": "boolean"}},
			// Named by TestOpenAPIRequired_MatchesTheStructsOwnJSONTags, which
			// reads inboxListResponse's json tags rather than anyone's memory.
			"rows", "count", "unread_count", "has_more"),
		"FinalInboxItem":         inboxItem,
		"FinalInboxBulk":         object(map[string]any{"updated": integer(), "skipped": integer(), "skipped_ids": array(str()), "state": str()}),
		"FinalWebhookFire":       object(map[string]any{"run_id": str(), "status": str(), "deduped": boolean()}),
		"FinalUserModel":         object(map[string]any{"user_id": str(), "workspace_id": str(), "exists": boolean(), "user_slug": str(), "bytes": integer(), "created_at": str(), "updated_at": str(), "content": str(), "facts": array(object(map[string]any{"key": str(), "value": str()}))}),
		"FinalUserModelMutation": object(map[string]any{"user_id": str(), "forgot": str(), "exists": boolean(), "remaining": array(object(map[string]any{"key": str(), "value": str()}))}),
		"FinalPeerConsent":       object(map[string]any{"user_id": str(), "workspace_id": str(), "opted_out": boolean(), "opted_out_at": str(), "purged": integer(), "purged_models": integer()}),
		"FinalPreferences":       map[string]any{"type": "object", "additionalProperties": anyJSON()},
		"FinalStatus":            object(map[string]any{"status": str()}),
	}

	routes := map[string]DomainSchema{
		"GET /api/v1/integrations/composio/inventory":              {Response: ref("FinalComposioInventory")},
		"GET /api/v1/integrations/composio/toolkits":               {Response: ref("FinalComposioToolkits")},
		"GET /api/v1/integrations/composio/tools":                  {Response: ref("FinalComposioTools")},
		"GET /api/v1/integrations/composio/triggers":               {Response: ref("FinalComposioTriggers")},
		"GET /api/v1/integrations/composio/triggers/active":        {Response: ref("FinalComposioActiveTriggers")},
		"POST /api/v1/integrations/composio/triggers":              {Response: ref("FinalComposioTrigger")},
		"GET /api/v1/integrations/composio/default":                {Response: ref("FinalComposioDefault")},
		"PUT /api/v1/integrations/composio/default":                {Response: ref("FinalComposioDefault")},
		"GET /api/v1/integrations/composio/settings":               {Response: ref("FinalComposioSettings")},
		"PUT /api/v1/integrations/composio/settings":               {Response: ref("FinalComposioSettings")},
		"DELETE /api/v1/integrations/composio/settings":            {Response: ref("FinalComposioSettings")},
		"GET /api/v1/integrations/composio/agents/{agentId}/bind":  {Response: ref("FinalComposioBindings")},
		"POST /api/v1/integrations/composio/agents/{agentId}/bind": {Response: ref("FinalComposioBind")},
		"POST /api/v1/credentials/{credentialId}/reveal":           {Response: ref("FinalCredentialReveal")},
		"GET /api/v1/notification-channels":                        {Response: ref("FinalNotificationChannels")},
		"POST /api/v1/notification-channels":                       {Response: ref("FinalNotificationChannel")},
		"GET /api/v1/notification-channels/{id}/agents":            {Response: ref("FinalNotificationChannelAgents")},
		"POST /api/v1/notification-channels/{id}/agents":           {Response: ref("FinalNotificationChannelAgent")},
		"GET /api/v1/notification-templates":                       {Response: ref("FinalNotificationTemplates")},
		"PUT /api/v1/notification-templates":                       {Response: object(map[string]any{"category": str(), "channel_id": str(), "title": str(), "body": str()})},
		"GET /api/v1/agents/{agentId}/chats":                       {Response: ref("FinalChatList")},
		"POST /api/v1/chats/{chatId}/steer":                        {Response: ref("FinalChatSteer")},
		"GET /api/v1/chats/{chatId}/participants":                  {Response: ref("FinalChatParticipants")},
		"GET /api/v1/inbox":                                        {Response: ref("FinalInboxList")},
		"GET /api/v1/inbox/{id}":                                   {Response: ref("FinalInboxItem")},
		"POST /api/v1/inbox/bulk":                                  {Response: ref("FinalInboxBulk")},
		"POST /api/v1/webhooks/{crewId}/{agentId}/trigger":         {Response: ref("FinalWebhookFire")},
		"GET /api/v1/users/me/user-model":                          {Response: ref("FinalUserModel")},
		"PUT /api/v1/users/me/peer-consent":                        {Response: ref("FinalPeerConsent")},
		"GET /api/v1/me/preferences":                               {Response: ref("FinalPreferences")},
		"DELETE /api/v1/me/preferences/{key}":                      {Response: ref("FinalStatus")},
	}
	return routes, components
}
