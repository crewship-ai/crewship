package main

// integrationsAuthRequestBodySchemaCatalog contains request contracts audited
// against the integration, Composio, OAuth, notification, profile, token, and
// webhook handlers. It is intentionally separate from response catalogs: the
// request shapes have different validation rules and are easy to weaken when
// response work is merged into the same module.
func integrationsAuthRequestBodySchemaCatalog() (map[string]DomainSchema, map[string]any) {
	str := func() map[string]any { return map[string]any{"type": "string"} }
	integer := func() map[string]any { return map[string]any{"type": "integer"} }
	boolean := func() map[string]any { return map[string]any{"type": "boolean"} }
	nullableString := func() map[string]any { return map[string]any{"type": "string", "nullable": true} }
	anyObject := func() map[string]any { return map[string]any{"type": "object", "additionalProperties": true} }
	stringMap := func() map[string]any {
		return map[string]any{"type": "object", "additionalProperties": str()}
	}
	array := func(item map[string]any) map[string]any { return map[string]any{"type": "array", "items": item} }
	object := func(properties map[string]any, required ...string) map[string]any {
		s := map[string]any{"type": "object", "properties": properties}
		if len(required) > 0 {
			s["required"] = required
		}
		return s
	}
	ref := func(name string) map[string]any { return map[string]any{"$ref": "#/components/schemas/" + name} }

	integrationCreate := object(map[string]any{
		"name": str(), "display_name": str(), "transport": map[string]any{"type": "string", "enum": []string{"streamable-http", "stdio"}},
		"endpoint": nullableString(), "command": nullableString(), "args_json": nullableString(),
		"env_json": nullableString(), "config_json": nullableString(), "icon": nullableString(),
	}, "name")
	integrationUpdate := object(map[string]any{
		"display_name": nullableString(), "transport": nullableString(), "endpoint": nullableString(),
		"command": nullableString(), "args_json": nullableString(), "env_json": nullableString(),
		"config_json": nullableString(), "icon": nullableString(), "enabled": boolean(),
	})
	composioBindApp := object(map[string]any{
		"toolkit": str(), "mode": map[string]any{"type": "string", "enum": []string{"full", "read", "custom"}},
		"tools": array(str()),
	}, "toolkit", "mode")
	channelFields := map[string]any{
		"type": map[string]any{"type": "string", "enum": []string{"email", "webhook", "shoutrrr"}},
		"url":  str(), "to": str(), "secret": str(), "events": array(str()), "provider": str(),
		"fields": stringMap(), "shoutrrr_url": str(), "personal": boolean(), "categories": array(str()), "min_priority": str(),
	}
	prefCell := object(map[string]any{
		"category": str(), "channel_id": str(), "state": map[string]any{"type": "string", "enum": []string{"off", "immediate"}},
	}, "category", "channel_id", "state")

	components := map[string]any{
		"IntegrationsAuthWorkspaceCreateRequest": integrationCreate,
		"IntegrationsAuthCrewCreateRequest": object(map[string]any{
			"workspace_mcp_server_id": nullableString(), "name": str(), "display_name": str(),
			"transport": map[string]any{"type": "string", "enum": []string{"streamable-http", "stdio"}},
			"endpoint":  nullableString(), "command": nullableString(), "args_json": nullableString(),
			"env_json": nullableString(), "config_json": nullableString(), "icon": nullableString(),
		}, "name"),
		"IntegrationsAuthUpdateRequest":          integrationUpdate,
		"IntegrationsAuthComposioTriggerRequest": object(map[string]any{"slug": str(), "user_id": str(), "config": anyObject()}, "slug", "user_id"),
		"IntegrationsAuthComposioConnectRequest": object(map[string]any{"toolkit": str(), "user_id": str()}, "toolkit", "user_id"),
		"IntegrationsAuthComposioBindRequest": object(map[string]any{
			"user_id": str(), "apps": array(ref("IntegrationsAuthComposioAppScope")), "toolkits": array(str()),
		}, "user_id"),
		"IntegrationsAuthComposioAppScope":        composioBindApp,
		"IntegrationsAuthComposioSettingsRequest": object(map[string]any{"api_key": str(), "label": str()}, "api_key"),
		"IntegrationsAuthComposioDefaultRequest":  object(map[string]any{"user_id": str()}),
		"IntegrationsAuthOAuthInitiateRequest":    object(map[string]any{"credential_id": str(), "redirect_uri": str()}, "credential_id"),
		"IntegrationsAuthOAuthExchangeRequest": object(map[string]any{
			"credential_id": str(), "code": str(), "redirect_uri": str(), "code_verifier": str(), "state": str(),
		}, "credential_id", "code"),
		"IntegrationsAuthOAuthLoopbackRequest":  object(map[string]any{"credential_id": str()}, "credential_id"),
		"IntegrationsAuthOAuthDiscoveryRequest": object(map[string]any{"mcp_url": str()}, "mcp_url"),
		"IntegrationsAuthOAuthAutoConnectRequest": object(map[string]any{
			"mcp_url": str(), "server_name": str(), "provider_hint": str(),
			"oauth_client_id": str(), "oauth_client_secret": str(),
		}, "mcp_url"),
		"IntegrationsAuthNotificationChannelCreateRequest": object(channelFields, "type"),
		"IntegrationsAuthNotificationChannelPatchRequest": object(map[string]any{
			"enabled": boolean(), "categories": array(str()), "min_priority": str(), "events": array(str()),
		}),
		"IntegrationsAuthNotificationChannelTestRequest": object(channelFields, "type"),
		"IntegrationsAuthNotificationPairAgentRequest":   object(map[string]any{"agent_id": str()}, "agent_id"),
		"IntegrationsAuthNotificationTemplateRequest": object(map[string]any{
			"category": str(), "channel_id": str(), "title": str(), "body": str(),
		}, "category", "channel_id", "title", "body"),
		"IntegrationsAuthNotificationPreferencesRequest": object(map[string]any{"cells": array(ref("IntegrationsAuthNotificationPreferenceCell"))}),
		"IntegrationsAuthNotificationPreferenceCell":     prefCell,
		"IntegrationsAuthEmptyRequest":                   object(map[string]any{}),
		"IntegrationsAuthCLITokenRequest": object(map[string]any{
			"name": str(), "tier": map[string]any{"type": "string", "enum": []string{"STANDARD", "ADMIN"}},
			"expires_in_seconds": integer(), "scopes": array(str()),
		}),
		"PipelineWebhookCreateRequest": object(map[string]any{
			"name": str(), "target_pipeline_slug": str(), "target_pipeline_id": str(), "target_pipeline_version": map[string]any{"type": "integer", "nullable": true},
			"signing_secret": str(), "inputs_template": anyObject(), "enabled": map[string]any{"type": "boolean", "nullable": true}, "rate_limit_per_min": integer(),
		}, "name"),
	}

	routes := map[string]DomainSchema{
		"POST /api/v1/integrations":                                 {Request: ref("IntegrationsAuthWorkspaceCreateRequest")},
		"PATCH /api/v1/integrations/{integrationId}":                {Request: ref("IntegrationsAuthUpdateRequest")},
		"POST /api/v1/crews/{crewId}/integrations":                  {Request: ref("IntegrationsAuthCrewCreateRequest")},
		"PATCH /api/v1/crews/{crewId}/integrations/{integrationId}": {Request: ref("IntegrationsAuthUpdateRequest")},
		"POST /api/v1/integrations/composio/triggers":               {Request: ref("IntegrationsAuthComposioTriggerRequest")},
		"POST /api/v1/integrations/composio/connect":                {Request: ref("IntegrationsAuthComposioConnectRequest")},
		"POST /api/v1/integrations/composio/agents/{agentId}/bind":  {Request: ref("IntegrationsAuthComposioBindRequest")},
		"PUT /api/v1/integrations/composio/settings":                {Request: ref("IntegrationsAuthComposioSettingsRequest")},
		"PUT /api/v1/integrations/composio/default":                 {Request: ref("IntegrationsAuthComposioDefaultRequest")},
		"POST /api/v1/oauth/initiate":                               {Request: ref("IntegrationsAuthOAuthInitiateRequest")},
		"POST /api/v1/oauth/exchange":                               {Request: ref("IntegrationsAuthOAuthExchangeRequest")},
		"POST /api/v1/oauth/loopback":                               {Request: ref("IntegrationsAuthOAuthLoopbackRequest")},
		"POST /api/v1/oauth/discover":                               {Request: ref("IntegrationsAuthOAuthDiscoveryRequest")},
		"POST /api/v1/oauth/auto-connect":                           {Request: ref("IntegrationsAuthOAuthAutoConnectRequest")},
		"POST /api/v1/notification-channels":                        {Request: ref("IntegrationsAuthNotificationChannelCreateRequest")},
		"PATCH /api/v1/notification-channels/{id}":                  {Request: ref("IntegrationsAuthNotificationChannelPatchRequest")},
		"POST /api/v1/notification-channels/test":                   {Request: ref("IntegrationsAuthNotificationChannelTestRequest")},
		"POST /api/v1/notification-channels/{id}/test":              {Request: ref("IntegrationsAuthEmptyRequest")},
		"POST /api/v1/notification-channels/{id}/agents":            {Request: ref("IntegrationsAuthNotificationPairAgentRequest")},
		"PUT /api/v1/notification-templates":                        {Request: ref("IntegrationsAuthNotificationTemplateRequest")},
		"PUT /api/v1/me/notification-prefs":                         {Request: ref("IntegrationsAuthNotificationPreferencesRequest")},
		"POST /api/v1/auth/cli-token":                               {Request: ref("IntegrationsAuthCLITokenRequest")},
		"POST /api/v1/workspaces/{workspaceId}/pipeline-webhooks":   {Request: ref("PipelineWebhookCreateRequest")},
	}
	return routes, components
}
