package main

// credentialsConnectorsAuthProfileSchemaCatalog is the handler-audited
// contract for the credentials, connector, MCP integration, token, and
// profile surfaces. Keep this catalog isolated so a domain change does not
// silently alter another schema contributor's file.
func credentialsConnectorsAuthProfileSchemaCatalog() (map[string]map[string]DomainSchema, map[string]any) {
	str := func() map[string]any { return map[string]any{"type": "string"} }
	integer := func() map[string]any { return map[string]any{"type": "integer"} }
	boolean := func() map[string]any { return map[string]any{"type": "boolean"} }
	nullableString := func() map[string]any { return map[string]any{"type": "string", "nullable": true} }
	object := func(properties map[string]any) map[string]any {
		return map[string]any{"type": "object", "properties": properties}
	}
	array := func(items map[string]any) map[string]any { return map[string]any{"type": "array", "items": items} }
	stringMap := map[string]any{"type": "object", "additionalProperties": str()}
	ref := func(name string) map[string]any { return map[string]any{"$ref": "#/components/schemas/" + name} }
	request := func(properties map[string]any, required ...string) map[string]any {
		s := object(properties)
		if len(required) != 0 {
			s["required"] = required
		}
		return s
	}
	list := func(name string) map[string]any { return array(ref(name)) }

	credential := ref("Credential")
	integration := object(map[string]any{
		"id": str(), "workspace_id": str(), "crew_id": nullableString(), "workspace_mcp_server_id": nullableString(),
		"name": str(), "display_name": str(), "transport": str(), "endpoint": nullableString(), "command": nullableString(),
		"args_json": nullableString(), "env_json": nullableString(), "config_json": nullableString(), "icon": nullableString(),
		"enabled": boolean(), "created_at": str(), "updated_at": str(), "agent_binding_count": integer(), "crew_server_count": integer(),
		"auth_status": nullableString(), "crew_name": nullableString(), "crew_slug": nullableString(),
	})
	connectorField := object(map[string]any{
		"key": str(), "label": str(), "type": str(), "required": boolean(), "default": nullableString(),
		"placeholder": nullableString(), "help": nullableString(), "choices": array(str()),
	})
	oauth := object(map[string]any{"authorization_url": str(), "token_url": str(), "scopes": array(str()), "pkce": boolean()})
	mcp := object(map[string]any{"transport": str(), "command": nullableString(), "args": array(str()), "endpoint": nullableString(), "env": stringMap})
	verify := object(map[string]any{"http": nullableString(), "sql": nullableString(), "mcp_method": nullableString()})
	docs := object(map[string]any{"setup_md": str()})
	connector := object(map[string]any{
		"id": str(), "name": str(), "description": str(), "brand": object(map[string]any{"logo": str(), "color": str()}),
		"category": str(), "auth_mode": str(), "fields": array(connectorField),
		"oauth": oauth, "mcp": mcp, "derived": stringMap, "verify": verify, "docs": docs,
	})
	profile := object(map[string]any{"id": str(), "email": str(), "full_name": nullableString(), "avatar_url": nullableString()})
	cliToken := object(map[string]any{
		"id": str(), "name": str(), "tier": str(), "token": nullableString(), "expires_at": nullableString(),
		"created_at": str(), "last_used_at": nullableString(), "revoked_at": nullableString(), "scopes": array(str()),
	})

	components := map[string]any{
		"ConnectorListItem": object(map[string]any{"id": str(), "name": str(), "description": str(), "category": str(), "auth_mode": str(), "brand_logo": str(), "brand_color": str()}),
		"Connector":         connector, "ConnectorList": array(ref("ConnectorListItem")),
		"Integration": integration, "IntegrationList": list("Integration"),
		"CredentialTestResponse":   object(map[string]any{"status": str(), "message": nullableString()}),
		"CredentialProbeResponse":  object(map[string]any{"valid": boolean(), "status": integer(), "error": nullableString(), "supported": boolean()}),
		"CredentialRotation":       object(map[string]any{"id": str(), "credential_id": str(), "grace_seconds": integer(), "rotated_at": str(), "expires_at": str(), "rotated_by": str(), "status": str(), "old_value_gone": boolean(), "cancelled_at": nullableString()}),
		"ConnectorVerifyRequest":   request(map[string]any{"fields": stringMap}, "fields"),
		"ConnectorVerifyResponse":  object(map[string]any{"ok": boolean(), "message": nullableString()}),
		"ConnectorInstallRequest":  request(map[string]any{"crew_id": nullableString(), "name": nullableString(), "fields": stringMap}, "fields"),
		"ConnectorInstallResponse": object(map[string]any{"integration_id": str(), "next_step": nullableString(), "oauth_url": nullableString()}),
		"IntegrationCreateRequest": request(map[string]any{
			"workspace_mcp_server_id": nullableString(), "name": str(), "display_name": str(), "transport": str(), "endpoint": nullableString(),
			"command": nullableString(), "args_json": nullableString(), "env_json": nullableString(), "config_json": nullableString(), "icon": nullableString(),
		}, "name"),
		"IntegrationUpdateRequest": object(map[string]any{
			"display_name": nullableString(), "transport": nullableString(), "endpoint": nullableString(), "command": nullableString(),
			"args_json": nullableString(), "env_json": nullableString(), "config_json": nullableString(), "icon": nullableString(), "enabled": map[string]any{"type": "boolean", "nullable": true},
		}),
		"AgentIntegrationBinding": object(map[string]any{
			"id": str(), "agent_id": str(), "mcp_server_id": str(), "mcp_server_scope": str(), "credential_id": nullableString(),
			"cred_type": nullableString(), "cred_header": nullableString(), "enabled": boolean(), "config_override_json": nullableString(),
			"created_at": str(), "server_name": str(), "server_display_name": str(), "credential_name": nullableString(),
		}),
		"AgentIntegrationBindingRequest": request(map[string]any{
			"mcp_server_id": str(), "mcp_server_scope": str(), "credential_id": nullableString(), "cred_type": nullableString(),
			"cred_header": nullableString(), "env_var_name": nullableString(), "enabled": map[string]any{"type": "boolean", "nullable": true}, "config_override_json": nullableString(),
		}, "mcp_server_id", "mcp_server_scope"),
		"AgentIntegrationBindingUpdateRequest": object(map[string]any{
			"credential_id": nullableString(), "cred_type": nullableString(), "cred_header": nullableString(), "env_var_name": nullableString(),
			"enabled": map[string]any{"type": "boolean", "nullable": true}, "config_override_json": nullableString(),
		}),
		"IntegrationTool":              object(map[string]any{"id": str(), "tool_name": str(), "description": nullableString(), "enabled": boolean(), "created_at": str(), "updated_at": str()}),
		"IntegrationToolUpdateRequest": object(map[string]any{"enabled": map[string]any{"type": "boolean", "nullable": true}, "description": nullableString()}),
		"CLIToken":                     cliToken, "CLITokenList": object(map[string]any{"data": array(cliToken)}),
		"CLITokenCreateRequest":    request(map[string]any{"name": str(), "tier": nullableString(), "expires_in_seconds": integer(), "scopes": array(str())}, "name"),
		"CLITokenValidateResponse": object(map[string]any{"valid": boolean(), "user_id": str(), "user_email": str()}),
		"Profile":                  profile, "ProfileUpdateRequest": request(map[string]any{"full_name": str()}, "full_name"),
		"PasswordChangeRequest":  request(map[string]any{"current_password": str(), "new_password": str()}, "current_password", "new_password"),
		"PasswordChangeResponse": object(map[string]any{"success": boolean(), "sessions_revoked": integer()}),
		"StatusResponse":         object(map[string]any{"status": str()}),
	}

	credentialRoutes := map[string]DomainSchema{
		"GET /api/v1/credentials": {Response: ref("CredentialList")}, "POST /api/v1/credentials": {Request: ref("CredentialCreateRequest"), Response: credential},
		"GET /api/v1/credentials/{credentialId}": {Response: credential}, "PATCH /api/v1/credentials/{credentialId}": {Request: ref("CredentialCreateRequest"), Response: credential},
		"PUT /api/v1/credentials/{credentialId}":        {Request: ref("CredentialCreateRequest"), Response: credential},
		"DELETE /api/v1/credentials/{credentialId}":     {Response: ref("StatusResponse")},
		"POST /api/v1/credentials/test":                 {Request: ref("CredentialCreateRequest"), Response: ref("CredentialProbeResponse")},
		"POST /api/v1/credentials/{credentialId}/test":  {Response: ref("CredentialProbeResponse")},
		"GET /api/v1/credentials/{credentialId}/fields": {Response: ref("CredentialFieldList")}, "POST /api/v1/credentials/{credentialId}/fields": {Request: ref("CredentialFieldRequest"), Response: ref("CredentialField")},
		"GET /api/v1/credentials/bindings": {Response: ref("CredentialBindingList")}, "POST /api/v1/credentials/bindings": {Request: ref("CredentialBindingRequest"), Response: ref("CredentialBinding")},
		"GET /api/v1/credentials/{credentialId}/rotations": {Response: array(ref("CredentialRotation"))}, "POST /api/v1/credentials/{credentialId}/rotate": {Request: ref("CredentialRotationRequest"), Response: ref("CredentialRotation")},
	}
	integrationRoutes := map[string]DomainSchema{
		"GET /api/v1/connectors": {Response: ref("ConnectorList")}, "GET /api/v1/connectors/{connectorId}": {Response: ref("Connector")},
		"POST /api/v1/connectors/{connectorId}/verify": {Request: ref("ConnectorVerifyRequest"), Response: ref("ConnectorVerifyResponse")}, "POST /api/v1/connectors/{connectorId}/install": {Request: ref("ConnectorInstallRequest"), Response: ref("ConnectorInstallResponse")},
		"GET /api/v1/integrations": {Response: ref("IntegrationList")}, "POST /api/v1/integrations": {Request: ref("IntegrationCreateRequest"), Response: ref("Integration")},
		"GET /api/v1/integrations/{integrationId}": {Response: ref("Integration")}, "PATCH /api/v1/integrations/{integrationId}": {Request: ref("IntegrationUpdateRequest"), Response: ref("Integration")}, "DELETE /api/v1/integrations/{integrationId}": {Response: ref("StatusResponse")},
		"GET /api/v1/integrations/crews": {Response: ref("IntegrationList")}, "GET /api/v1/crews/{crewId}/integrations": {Response: ref("IntegrationList")}, "POST /api/v1/crews/{crewId}/integrations": {Request: ref("IntegrationCreateRequest"), Response: ref("Integration")},
		"PATCH /api/v1/crews/{crewId}/integrations/{integrationId}": {Request: ref("IntegrationUpdateRequest"), Response: ref("Integration")}, "DELETE /api/v1/crews/{crewId}/integrations/{integrationId}": {Response: ref("StatusResponse")},
		"GET /api/v1/agents/{agentId}/integrations": {Response: array(ref("AgentIntegrationBinding"))}, "POST /api/v1/agents/{agentId}/integrations": {Request: ref("AgentIntegrationBindingRequest"), Response: ref("AgentIntegrationBinding")},
		"PATCH /api/v1/agents/{agentId}/integrations/{integrationId}": {Request: ref("AgentIntegrationBindingUpdateRequest"), Response: ref("AgentIntegrationBinding")}, "DELETE /api/v1/agents/{agentId}/integrations/{integrationId}": {Response: ref("StatusResponse")},
		"GET /api/v1/crews/{crewId}/integrations/{integrationId}/tools": {Response: array(ref("IntegrationTool"))}, "PATCH /api/v1/crews/{crewId}/integrations/{integrationId}/tools/{toolName}": {Request: ref("IntegrationToolUpdateRequest"), Response: ref("IntegrationTool")},
		"POST /api/v1/integrations/{integrationId}/test": {Response: ref("CredentialTestResponse")}, "POST /api/v1/crews/{crewId}/integrations/{integrationId}/test": {Response: ref("CredentialTestResponse")},
	}
	authProfileRoutes := map[string]DomainSchema{
		"POST /api/v1/auth/cli-token": {Request: ref("CLITokenCreateRequest"), Response: ref("CLIToken")}, "GET /api/v1/auth/cli-token/validate": {Response: ref("CLITokenValidateResponse")},
		"GET /api/v1/auth/cli-tokens": {Response: ref("CLITokenList")}, "DELETE /api/v1/auth/cli-tokens/{tokenId}": {Response: ref("StatusResponse")},
		"PATCH /api/v1/users/me": {Request: ref("ProfileUpdateRequest"), Response: ref("Profile")}, "POST /api/v1/users/me/password": {Request: ref("PasswordChangeRequest"), Response: ref("PasswordChangeResponse")},
		"POST /api/v1/users/me/avatar": {Response: object(map[string]any{"avatar_url": nullableString()}), ResponseMedia: []string{"application/json"}}, "DELETE /api/v1/users/me/avatar": {Response: ref("StatusResponse")},
		"GET /api/v1/users/{id}/avatar": {Response: map[string]any{"type": "string", "format": "binary"}, ResponseMedia: []string{"image/svg+xml", "image/png", "image/jpeg", "image/webp"}},
	}
	return map[string]map[string]DomainSchema{"credentials": credentialRoutes, "connectors-integrations": integrationRoutes, "auth-profile": authProfileRoutes}, components
}

func credentialsConnectorsAuthProfileSchemaCatalogRoutes() map[string]map[string]DomainSchema {
	routes, _ := credentialsConnectorsAuthProfileSchemaCatalog()
	return routes
}

func credentialsConnectorsAuthProfileSchemaCatalogComponents() map[string]any {
	_, components := credentialsConnectorsAuthProfileSchemaCatalog()
	return components
}
