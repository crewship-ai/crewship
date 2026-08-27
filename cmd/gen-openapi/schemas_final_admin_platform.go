package main

// finalAdminPlatformSchemaCatalog contains the last handler-audited response
// contracts for the platform/bootstrap surfaces. The component prefix is
// intentional: these are wire contracts for this audit, not reusable domain
// models whose names could collide with an independently audited catalog.
func finalAdminPlatformSchemaCatalog() (map[string]DomainSchema, map[string]any) {
	str := func() map[string]any { return map[string]any{"type": "string"} }
	integer := func() map[string]any { return map[string]any{"type": "integer"} }
	boolean := func() map[string]any { return map[string]any{"type": "boolean"} }
	numberSchema := func() map[string]any { return map[string]any{"type": "number"} }
	nullable := func(s map[string]any) map[string]any {
		out := map[string]any{}
		for k, v := range s {
			out[k] = v
		}
		out["nullable"] = true
		return out
	}
	object := func(p map[string]any) map[string]any { return map[string]any{"type": "object", "properties": p} }
	array := func(i map[string]any) map[string]any { return map[string]any{"type": "array", "items": i} }
	ref := func(n string) map[string]any { return map[string]any{"$ref": "#/components/schemas/" + n} }

	model := object(map[string]any{"id": str(), "display_name": str(), "provider": str()})
	featureFlag := object(map[string]any{
		"id": str(), "key": str(), "description": nullable(str()), "enabled": boolean(),
		"percentage": integer(), "created_at": str(), "updated_at": str(), "override_enabled": nullable(boolean()),
	})
	instanceSetting := object(map[string]any{"key": str(), "value": str(), "updated_at": str()})
	governance := object(map[string]any{
		"configured": boolean(), "enabled": boolean(), "security_contact_user_id": str(), "deny_notify_min_risk": integer(),
		"watch_spec": str(), "watch_presets": array(str()), "require_second_approver": boolean(),
		"gov_model_provider": str(), "gov_model_id": str(), "auto_lease_seconds": integer(), "gov_model_credential_id": str(),
		"effective_second_approver": object(map[string]any{
			"min_security_level": integer(), "min_security_level_label": str(), "source": str(),
			"tier_floor_security_level": integer(), "tier_floor_label": str(),
		}),
		"warning": str(),
	})
	backupEntry := object(map[string]any{
		"path": str(), "file_name": str(), "size_bytes": integer(), "scope": str(), "scope_level": str(),
		"encrypted": boolean(), "created_at": str(), "format_version": integer(),
	})
	backupStatus := object(map[string]any{"held": boolean(), "workspace_id": str(), "acquired_by": str(), "acquired_at": str(), "expires_at": str()})
	backupVerify := object(map[string]any{"valid": boolean(), "size_bytes": integer(), "manifest": object(map[string]any{
		"format_version": integer(), "crewship_version_at_backup": str(), "schema_migration_versions": array(integer()),
		"scope": str(), "scope_level": str(), "compatible_targets": array(str()), "created_at": str(),
	}), "error": str()})
	backupCreate := object(map[string]any{"path": str(), "size_bytes": integer(), "payload_sha256": str(), "format_version": integer(), "scope": str(), "scope_level": str(), "created_at": str(), "encrypted": boolean()})
	backupRotate := object(map[string]any{"deleted": array(str()), "dry_run": boolean()})
	// restored_ws is the restored workspace's SLUG — it is what the CLI
	// prints as `workspace=`, and RestoreResult.RestoredWs is a string.
	// It was declared boolean here, so every client generated from this
	// spec typed the one field an operator actually reads wrong.
	backupRestore := object(map[string]any{"manifest": object(map[string]any{}), "restored_ws": str(), "restored_workspace_id": str(), "crews_count": integer(), "crews_restored": integer(), "rows_inserted": integer(), "docker_phase_skipped": boolean(), "dropped_crew_filesystems": array(str()), "security_level_clamped": integer(), "security_level_clamps": array(object(map[string]any{})),
		// Schema skew: values discarded because the bundle named a column
		// this instance's schema does not have (#2034). Spelled out rather
		// than left as a bare object — the whole point of the field is that
		// a client can act on WHICH table lost WHAT.
		"columns_dropped": integer(),
		"dropped_columns": array(object(map[string]any{"table": str(), "column": str(), "rows": integer()}))})
	backupSelfTest := object(map[string]any{"ok": boolean(), "crew_id": str(), "crew_slug": str(), "canary_path": str(), "canary_bytes": integer(), "bundle_bytes": integer(), "elapsed_ms": integer(), "error": str()})
	backupMetrics := object(map[string]any{"created_total": integer(), "created_by_scope": map[string]any{"type": "object", "additionalProperties": integer()}, "failed_total": integer(), "failed_by_reason": map[string]any{"type": "object", "additionalProperties": integer()}, "restored_total": integer(), "size_bytes_total": integer(), "duration_seconds_p50": numberSchema(), "duration_seconds_p95": numberSchema(), "duration_seconds_mean": numberSchema(), "lock_held_seconds_by_workspace": map[string]any{"type": "object", "additionalProperties": integer()}})
	setup := object(map[string]any{
		"workspace_id": str(), "crew_id": str(), "agent_id": str(), "credential_id": str(),
		"agent_ids": array(str()), "agent_count": integer(),
	})
	oauthProvider := object(map[string]any{"auth_url": str(), "token_url": str(), "default_scopes": str()})
	oauthDiscovery := object(map[string]any{
		"auth_url": str(), "token_url": str(), "registration_endpoint": str(), "scopes": str(),
		"supports_dcr": boolean(), "supports_pkce": boolean(), "source": str(),
	})

	components := map[string]any{
		"FinalAdminPlatformModel":            model,
		"FinalAdminPlatformFeatureFlag":      featureFlag,
		"FinalAdminPlatformInstanceSetting":  instanceSetting,
		"FinalAdminPlatformGovernance":       governance,
		"FinalAdminPlatformBackupEntry":      backupEntry,
		"FinalAdminPlatformBackupStatus":     backupStatus,
		"FinalAdminPlatformBackupVerify":     backupVerify,
		"FinalAdminPlatformBackupCreate":     backupCreate,
		"FinalAdminPlatformBackupRotate":     backupRotate,
		"FinalAdminPlatformBackupRestore":    backupRestore,
		"FinalAdminPlatformBackupSelfTest":   backupSelfTest,
		"FinalAdminPlatformBackupMetrics":    backupMetrics,
		"FinalAdminPlatformOnboardingSetup":  setup,
		"FinalAdminPlatformOAuthProvider":    oauthProvider,
		"FinalAdminPlatformOAuthDiscovery":   oauthDiscovery,
		"FinalAdminPlatformOAuthInitiate":    object(map[string]any{"auth_url": str(), "state": str()}),
		"FinalAdminPlatformOAuthExchange":    object(map[string]any{"status": str(), "credential_id": str()}),
		"FinalAdminPlatformOAuthLoopback":    object(map[string]any{"auth_url": str(), "loopback_port": integer(), "state": str()}),
		"FinalAdminPlatformOAuthAutoConnect": object(map[string]any{"status": str(), "auth_url": str(), "credential_id": str()}),
		"FinalAdminPlatformBootstrap":        object(map[string]any{"user_id": str(), "email": str(), "workspace_id": str(), "cli_token": str(), "session_pending": boolean()}),
		"FinalAdminPlatformMessage":          object(map[string]any{"ok": boolean(), "message": str()}),
		"FinalAdminPlatformWsToken":          object(map[string]any{"token": str()}),
		"FinalAdminPlatformFeatureOverride":  object(map[string]any{"key": str(), "enabled": boolean()}),
		"FinalAdminPlatformRateLimitState": object(map[string]any{
			"key": str(), "group": str(), "display_name": str(), "description": str(), "unit": str(),
			"min": integer(), "max": integer(), "default": integer(), "value": integer(), "overridden": boolean(),
		}),
	}

	routes := map[string]DomainSchema{
		"GET /api/v1/admin/backups":              {Response: object(map[string]any{"data": array(ref("FinalAdminPlatformBackupEntry"))})},
		"POST /api/v1/admin/backups":             {Response: ref("FinalAdminPlatformBackupCreate")},
		"GET /api/v1/admin/backups/status":       {Response: ref("FinalAdminPlatformBackupStatus")},
		"GET /api/v1/admin/backups/verify":       {Response: ref("FinalAdminPlatformBackupVerify")},
		"GET /api/v1/admin/backups/metrics":      {Response: ref("FinalAdminPlatformBackupMetrics")},
		"POST /api/v1/admin/backups/rotate":      {Response: ref("FinalAdminPlatformBackupRotate")},
		"POST /api/v1/admin/backups/restore":     {Response: ref("FinalAdminPlatformBackupRestore")},
		"POST /api/v1/admin/backups/self-test":   {Response: ref("FinalAdminPlatformBackupSelfTest")},
		"GET /api/v1/admin/keeper/governance":    {Response: ref("FinalAdminPlatformGovernance")},
		"PUT /api/v1/admin/keeper/governance":    {Response: ref("FinalAdminPlatformGovernance")},
		"GET /api/v1/admin/rate-limits":          {Response: object(map[string]any{"limiters": array(ref("FinalAdminPlatformRateLimitState"))})},
		"PUT /api/v1/admin/rate-limits/{key}":    {Response: ref("FinalAdminPlatformRateLimitState")},
		"DELETE /api/v1/admin/rate-limits/{key}": {Response: ref("FinalAdminPlatformRateLimitState")},
		"POST /api/v1/auth/forgot":               {Response: ref("FinalAdminPlatformMessage")},
		"POST /api/v1/auth/reset":                {Response: object(map[string]any{"ok": boolean()})},
		"POST /api/v1/auth/signup":               {Response: ref("FinalAdminPlatformMessage")},
		"GET /api/v1/auth/google/status":         {Response: object(map[string]any{"enabled": boolean()})},
		"POST /api/v1/bootstrap":                 {Response: ref("FinalAdminPlatformBootstrap")},
		"GET /api/v1/ws-token":                   {Response: ref("FinalAdminPlatformWsToken")},
		"GET /api/v1/onboarding/status":          {Response: object(map[string]any{"completed": boolean()})},
		"POST /api/v1/onboarding/complete":       {Response: object(map[string]any{"status": str()})},
		"POST /api/v1/onboarding/setup": {Request: object(map[string]any{
			"workspace_name": str(), "crew_name": str(), "agent_name": str(), "cli_adapter": str(), "llm_provider": str(),
			"llm_model": str(), "credential_name": str(), "credential_value": str(), "crew_template_slug": str(),
			"pairing_mode": boolean(), "preferred_language": str(), "telemetry_opt_in": nullable(boolean()),
		}), Response: ref("FinalAdminPlatformOnboardingSetup")},
		"GET /api/v1/models": {Response: object(map[string]any{"provider": str(), "source": str(), "models": array(ref("FinalAdminPlatformModel"))})},
		"GET /api/v1/features/catalog": {Response: object(map[string]any{"features": array(object(map[string]any{
			"ref": str(), "name": str(), "description": str(), "category": str(), "icon": str(), "size_hint": str(), "publisher": str(), "tier": str(),
		}))})},
		"GET /api/v1/feature-flags":                {Response: array(ref("FinalAdminPlatformFeatureFlag"))},
		"POST /api/v1/feature-flags":               {Response: ref("FinalAdminPlatformFeatureFlag")},
		"PATCH /api/v1/feature-flags/{key}":        {Response: ref("FinalAdminPlatformFeatureFlag")},
		"PUT /api/v1/feature-flags/{key}/override": {Response: ref("FinalAdminPlatformFeatureOverride")},
		"GET /api/v1/instance/settings":            {Response: array(ref("FinalAdminPlatformInstanceSetting"))},
		"GET /api/v1/instance/settings/{key}":      {Response: ref("FinalAdminPlatformInstanceSetting")},
		"PUT /api/v1/instance/settings/{key}":      {Response: ref("FinalAdminPlatformInstanceSetting")},
		"GET /api/v1/oauth/providers":              {Response: map[string]any{"type": "object", "additionalProperties": ref("FinalAdminPlatformOAuthProvider")}},
		"POST /api/v1/oauth/initiate":              {Response: ref("FinalAdminPlatformOAuthInitiate")},
		"POST /api/v1/oauth/exchange":              {Response: ref("FinalAdminPlatformOAuthExchange")},
		"POST /api/v1/oauth/loopback":              {Response: ref("FinalAdminPlatformOAuthLoopback")},
		"POST /api/v1/oauth/discover":              {Response: ref("FinalAdminPlatformOAuthDiscovery")},
		"POST /api/v1/oauth/auto-connect":          {Response: ref("FinalAdminPlatformOAuthAutoConnect")},
		"GET /api/v1/oauth/callback":               {Response: str(), ResponseMedia: []string{"text/html"}},
	}
	return routes, components
}
