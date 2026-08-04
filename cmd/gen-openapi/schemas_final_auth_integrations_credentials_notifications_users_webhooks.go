package main

// finalAuthIntegrationsCredentialsNotificationsUsersWebhooksSchemaCatalog
// contains the last twenty generic request bodies in the identity-adjacent
// API surface. The shapes below are based on the concrete handler decoders:
// bodyless actions use an explicit empty object, uploads use multipart form
// fields, and webhook dispatch remains an intentionally arbitrary JSON value.
func finalAuthIntegrationsCredentialsNotificationsUsersWebhooksSchemaCatalog() (map[string]DomainSchema, map[string]any) {
	str := func() map[string]any { return map[string]any{"type": "string"} }
	boolean := func() map[string]any { return map[string]any{"type": "boolean"} }
	nullableString := func() map[string]any { return map[string]any{"type": "string", "nullable": true} }
	object := func(properties map[string]any, required ...string) map[string]any {
		s := map[string]any{"type": "object", "properties": properties}
		if len(required) > 0 {
			s["required"] = required
		}
		return s
	}
	array := func(item map[string]any) map[string]any { return map[string]any{"type": "array", "items": item} }
	ref := func(name string) map[string]any { return map[string]any{"$ref": "#/components/schemas/" + name} }

	components := map[string]any{
		"FinalAuthEmptyRequest": object(map[string]any{}),
		"FinalAuthSignupRequest": object(map[string]any{
			"full_name": str(), "email": str(), "password": str(),
		}, "full_name", "email", "password"),
		"FinalAuthForgotRequest": object(map[string]any{"email": str()}),
		"FinalAuthResetRequest": object(map[string]any{
			"token": str(), "new_password": str(),
		}, "token", "new_password"),
		"FinalAuthRefreshToolsRequest": object(map[string]any{
			"tools": array(object(map[string]any{"name": str(), "description": nullableString()})),
		}),
		"FinalAuthCredentialTestRequest": object(map[string]any{
			"provider": str(), "type": str(), "value": str(),
		}, "value"),
		"FinalAuthRevealRequest":      object(map[string]any{"reason": str()}, "reason"),
		"FinalAuthPeerConsentRequest": object(map[string]any{"opted_out": boolean()}, "opted_out"),
		"FinalAuthAvatarUploadRequest": object(map[string]any{
			"file": map[string]any{"type": "string", "format": "binary"},
		}, "file"),
		"FinalAuthWebhookPayload": map[string]any{
			"description": "Arbitrary JSON payload forwarded to the webhook pipeline.",
			"oneOf": []map[string]any{
				{"type": "object", "additionalProperties": true},
				{"type": "array", "items": map[string]any{}},
				{"type": "string"}, {"type": "number"}, {"type": "boolean"}, {"type": "null"},
			},
		},
		"FinalAuthUserPreferenceValue": map[string]any{
			"description": "Arbitrary JSON value stored for the authenticated user and preference key.",
			"oneOf": []map[string]any{
				{"type": "object", "additionalProperties": true},
				{"type": "array", "items": map[string]any{}},
				{"type": "string"}, {"type": "number"}, {"type": "boolean"}, {"type": "null"},
			},
		},
	}

	empty := ref("FinalAuthEmptyRequest")
	routes := map[string]DomainSchema{
		"POST /api/v1/auth/signup":                                               {Request: ref("FinalAuthSignupRequest")},
		"POST /api/v1/auth/forgot":                                               {Request: ref("FinalAuthForgotRequest")},
		"POST /api/v1/auth/reset":                                                {Request: ref("FinalAuthResetRequest")},
		"POST /api/v1/onboarding/complete":                                       {Request: empty},
		"POST /api/v1/auth/sessions/{id}/revoke":                                 {Request: empty},
		"POST /api/v1/integrations/{integrationId}/test":                         {Request: empty},
		"POST /api/v1/crews/{crewId}/integrations/{integrationId}/test":          {Request: empty},
		"POST /api/v1/crews/{crewId}/integrations/{integrationId}/tools/refresh": {Request: ref("FinalAuthRefreshToolsRequest")},
		"POST /api/v1/integrations/composio/accounts/{accountId}/revoke":         {Request: empty},
		"POST /api/v1/integrations/composio/accounts/{accountId}/refresh":        {Request: empty},
		"POST /api/v1/credentials/{credentialId}/reveal":                         {Request: ref("FinalAuthRevealRequest")},
		"POST /api/v1/credentials/{credentialId}/test":                           {Request: ref("FinalAuthCredentialTestRequest")},
		"POST /api/v1/notifications/{notificationId}/read":                       {Request: empty},
		"POST /api/v1/notifications/read-all":                                    {Request: empty},
		"POST /api/v1/users/me/avatar":                                           {Request: ref("FinalAuthAvatarUploadRequest")},
		"PUT /api/v1/users/me/peer-consent":                                      {Request: ref("FinalAuthPeerConsentRequest")},
		"PUT /api/v1/me/preferences/{key}":                                       {Request: ref("FinalAuthUserPreferenceValue")},
		"POST /api/v1/webhooks/{token}":                                          {Request: ref("FinalAuthWebhookPayload")},
		"POST /api/v1/webhooks/{crewId}/{agentId}/trigger":                       {Request: ref("FinalAuthWebhookPayload")},
	}
	return routes, components
}
