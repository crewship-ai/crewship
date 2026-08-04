package main

// final21GenericResponseSchemaCatalog contains the last small set of
// response contracts that were still relying on the generator fallback. It
// intentionally lives in its own catalog so this audit can be reviewed and
// tested as one unit without changing another domain's ownership.
func final21GenericResponseSchemaCatalog() map[string]DomainSchema {
	str := func() map[string]any { return map[string]any{"type": "string"} }
	integer := func() map[string]any { return map[string]any{"type": "integer"} }
	boolean := func() map[string]any { return map[string]any{"type": "boolean"} }
	dateTime := func() map[string]any { return map[string]any{"type": "string", "format": "date-time"} }
	nullableString := func() map[string]any { return map[string]any{"type": "string", "nullable": true} }
	obj := func(properties map[string]any) map[string]any {
		return map[string]any{"type": "object", "properties": properties}
	}
	array := func(item map[string]any) map[string]any {
		return map[string]any{"type": "array", "items": item}
	}
	mapOf := func(item map[string]any) map[string]any {
		return map[string]any{"type": "object", "additionalProperties": item}
	}
	noContent := func() DomainSchema { return DomainSchema{SuccessStatuses: []string{"204"}} }

	backupManifest := obj(map[string]any{
		"format_version": integer(), "crewship_version_at_backup": str(),
		"schema_migration_versions": array(integer()), "scope": str(), "scope_level": str(),
		"compatible_targets": array(str()), "created_at": dateTime(),
		"created_by":      obj(map[string]any{"user_id": str(), "email": str(), "role": str()}),
		"source_instance": obj(map[string]any{"hostname": str(), "platform": str(), "docker_version": str()}),
		"contents": obj(map[string]any{
			"workspace": obj(map[string]any{"id": str(), "name": str(), "slug": str()}),
			"crews": array(obj(map[string]any{
				"id": str(), "slug": str(), "name": str(), "runtime_image": str(),
				"base_image_digest": str(), "cached_image_digest": str(), "config_hash": str(),
				"devcontainer_config_included": boolean(), "mise_config_included": boolean(),
				"features":           array(obj(map[string]any{"name": str(), "digest": str()})),
				"workspace_included": boolean(), "volumes_included": array(str()), "memory_included": boolean(),
				"crew_files_included": boolean(), "output_included": boolean(), "system_included": boolean(),
				"agent_count": integer(), "payload_size_bytes": integer(),
			})),
			"credstore_included": boolean(), "auth_keys_included": boolean(),
			"instance_config_included": boolean(), "memory_blobs_included": integer(), "memory_blobs_missing": integer(),
		}),
		"encryption": obj(map[string]any{
			"enabled": boolean(), "algorithm": str(), "key_derivation": str(), "recipients": array(str()),
		}),
		"checksums": obj(map[string]any{"payload_sha256": str()}),
	})
	keeperResult := obj(map[string]any{"request_id": str(), "decision": str(), "reason": str(), "risk_score": integer()})
	logLevel := obj(map[string]any{"level": str(), "baseline": str(), "expires_at": nullableString()})
	slashField := obj(map[string]any{"name": str(), "type": str(), "required": boolean(), "default": str()})
	slashCommand := obj(map[string]any{
		"id": str(), "label": str(), "label_cs": str(), "icon": str(), "capability": str(),
		"form_schema": array(slashField),
	})

	return map[string]DomainSchema{
		"GET /api/v1/admin/backups/inspect": {Response: backupManifest},
		"POST /api/v1/admin/keeper/ask": {
			Request: obj(map[string]any{
				"requesting_agent_id": str(), "requesting_crew_id": str(), "workspace_id": str(),
				"credential_id": str(), "credential_name": str(), "task_id": str(), "intent": str(),
			}), Response: keeperResult,
		},
		"GET /api/v1/admin/log-level": {Response: logLevel},
		"PUT /api/v1/admin/log-level": {
			Request: obj(map[string]any{"level": str(), "ttl_seconds": integer()}), Response: logLevel,
		},

		"DELETE /api/v1/chats/{chatId}/participants/{userId}":        noContent(),
		"DELETE /api/v1/checkpoints/{id}":                            noContent(),
		"DELETE /api/v1/crew-connections/{connectionId}":             noContent(),
		"DELETE /api/v1/instance/settings/{key}":                     noContent(),
		"DELETE /api/v1/me/preferences/{key}":                        noContent(),
		"PUT /api/v1/me/preferences/{key}":                           noContent(),
		"DELETE /api/v1/milestones/{milestoneId}":                    noContent(),
		"DELETE /api/v1/projects/{projectId}":                        noContent(),
		"DELETE /api/v1/recurring-issues/{recurringId}":              noContent(),
		"DELETE /api/v1/triage-rules/{ruleId}":                       noContent(),
		"DELETE /api/v1/notification-templates":                      noContent(),
		"DELETE /api/v1/notification-channels/{id}/agents/{agentId}": {Response: obj(map[string]any{"channel_id": str(), "agent_id": str(), "allowed": boolean()})},

		"DELETE /api/v1/notification-channels/{id}":                       {Response: obj(map[string]any{"deleted": str()})},
		"POST /api/v1/integrations/composio/accounts/{accountId}/revoke":  noContent(),
		"POST /api/v1/integrations/composio/accounts/{accountId}/refresh": noContent(),
		"DELETE /api/v1/integrations/composio/accounts/{accountId}":       noContent(),
		"DELETE /api/v1/integrations/composio/agents/{agentId}/bind":      {Response: obj(map[string]any{"status": str()})},
		"GET /api/v1/oauth/providers":                                     {Response: mapOf(obj(map[string]any{"auth_url": str(), "token_url": str(), "default_scopes": str()}))},
		"GET /api/v1/slash-commands":                                      {Response: array(slashCommand)},
		"POST /api/v1/waitpoint-tokens/{token}": {
			Request:  obj(map[string]any{"approved": boolean(), "payload": map[string]any{}}),
			Response: obj(map[string]any{"ok": boolean(), "approved": boolean()}),
		},
	}
}
