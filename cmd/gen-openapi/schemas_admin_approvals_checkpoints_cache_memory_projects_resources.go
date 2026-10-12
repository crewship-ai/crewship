package main

// schemaCatalogAdminApprovalsCheckpointsCacheMemoryProjectsResources contains
// the API surfaces audited in this worktree. It is intentionally a separate
// catalog so another domain can be added without editing a shared schema file.
// The schemas mirror the JSON assembled by the handlers, not their private Go
// implementation types.
func schemaCatalogAdminApprovalsCheckpointsCacheMemoryProjectsResources() map[string]DomainSchema {
	str := func() map[string]any { return map[string]any{"type": "string"} }
	integer := func() map[string]any { return map[string]any{"type": "integer"} }
	number := func() map[string]any { return map[string]any{"type": "number"} }
	boolean := func() map[string]any { return map[string]any{"type": "boolean"} }
	anyObject := func() map[string]any {
		return map[string]any{"type": "object", "additionalProperties": true}
	}
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
	array := func(items map[string]any) map[string]any {
		return map[string]any{"type": "array", "items": items}
	}
	stringArray := func() map[string]any { return array(str()) }
	stringMap := func() map[string]any {
		return map[string]any{"type": "object", "additionalProperties": str()}
	}
	booleanMap := func() map[string]any {
		return map[string]any{"type": "object", "additionalProperties": boolean()}
	}
	integerMap := func() map[string]any {
		return map[string]any{"type": "object", "additionalProperties": integer()}
	}
	nullable := func(schema map[string]any) map[string]any {
		copy := map[string]any{}
		for k, v := range schema {
			copy[k] = v
		}
		copy["nullable"] = true
		return copy
	}

	stats := object(map[string]any{
		"workspaces": integer(), "users": integer(), "crews": integer(),
		"agents": integer(), "running": integer(),
	})
	// Admin › Users / Workspaces (internal/api/admin_people.go). The scope —
	// current workspace, or every one for the instance owner — is in the
	// X-Admin-Scope response header, not the body.
	adminUser := object(map[string]any{
		"id": str(), "email": str(), "full_name": nullable(str()),
		"avatar_url": nullable(str()), "created_at": str(),
		"workspace": nullable(object(map[string]any{"id": str(), "name": str(), "slug": str()})),
		"role":      nullable(str()),
		"memberships": array(object(map[string]any{
			"member_id": str(), "workspace_id": str(), "name": str(), "slug": str(), "role": str(), "joined_at": str(),
		}, "member_id", "workspace_id", "name", "slug", "role", "joined_at")),
		"last_active_at": nullable(str()), "active_sessions": integer(), "cli_tokens": integer(),
		"locked_until": nullable(str()), "failed_login_count": integer(), "email_verified": boolean(),
		"instance_admin": boolean(), "instance_admin_source": nullable(str()),
		"suspended_at": nullable(str()), "suspended_reason": nullable(str()), "setup_link_expires_at": nullable(str()),
	}, "id", "email", "full_name", "avatar_url", "created_at", "workspace", "role", "memberships",
		"last_active_at", "active_sessions", "cli_tokens", "locked_until", "failed_login_count", "email_verified",
		"instance_admin", "instance_admin_source", "suspended_at", "suspended_reason", "setup_link_expires_at")
	adminWorkspace := object(map[string]any{
		"id": str(), "name": str(), "slug": str(), "logo_url": nullable(str()), "created_at": str(), "updated_at": str(),
		"_count_members": integer(), "_count_agents": integer(), "_count_crews": integer(),
		"preferred_language": nullable(str()), "run_retention_days": nullable(integer()),
		"allow_privileged_credentials": boolean(), "pending_invitations": integer(),
		"last_activity_at": nullable(str()), "runs_7d": integer(), "runs_by_day": array(integer()),
		"cost_30d_usd": number(), "current": boolean(),
		"owners": array(object(map[string]any{"id": str(), "email": str(), "full_name": nullable(str())}, "id", "email", "full_name")),
	}, "id", "name", "slug", "logo_url", "created_at", "updated_at", "_count_members", "_count_agents", "_count_crews",
		"preferred_language", "run_retention_days", "allow_privileged_credentials", "pending_invitations",
		"last_activity_at", "runs_7d", "runs_by_day", "cost_30d_usd", "current", "owners")
	// The three per-person POSTs carry no body; EmptyRequest is the shared
	// component the other body-less admin actions name.
	emptyRequest := ref("EmptyRequest")
	adminScopeHeader := map[string]any{"X-Admin-Scope": map[string]any{
		"description": "instance when the caller is an instance administrator and the list covers every workspace; workspace when it covers the caller's workspace only.",
		"schema":      map[string]any{"type": "string", "enum": []string{"instance", "workspace"}},
	}}
	instanceMembership := object(map[string]any{"workspace_id": str(), "role": str()}, "workspace_id", "role")
	adminUserSession := object(map[string]any{
		"id": str(), "created_at": str(), "last_used_at": str(), "expires_at": str(),
		"user_agent": str(), "ip": str(), "current": boolean(),
	}, "id", "created_at", "last_used_at", "expires_at", "user_agent", "ip", "current")
	adminUserCLIToken := object(map[string]any{
		"id": str(), "name": str(), "scopes": array(str()), "created_at": str(),
		"last_used_at": nullable(str()), "expires_at": nullable(str()),
	}, "id", "name", "scopes", "created_at", "last_used_at", "expires_at")

	approval := object(map[string]any{
		"id": str(), "workspace_id": str(), "crew_id": str(), "agent_id": str(), "mission_id": str(),
		"requested_by": str(), "kind": str(), "reason": str(), "payload": anyObject(), "status": str(),
		"decided_by": nullable(str()), "decided_at": nullable(str()), "decision_comment": str(),
		"timeout_at": nullable(str()), "created_at": str(),
		// routine_version (#2364): the authored routine version this approval
		// gated, present only when the request came from one — omitempty on
		// the struct, so it is not required.
		"routine_version": integer(),
	},
		// harbormaster.Request has no omitempty on any of these, so the server
		// emits all fifteen on every row. Naming them is what lets the contract
		// gate see a renamed field at all.
		"id", "workspace_id", "crew_id", "agent_id", "mission_id", "requested_by",
		"kind", "reason", "payload", "status", "decided_by", "decided_at",
		"decision_comment", "timeout_at", "created_at")
	checkpointState := object(map[string]any{
		"agent_memory":  stringMap(),
		"pending_tasks": stringArray(), "open_assignments": stringArray(),
		"crew_container_id": nullable(str()), "meta": anyObject(),
	})
	checkpoint := object(map[string]any{
		"id": str(), "workspace_id": str(), "crew_id": nullable(str()), "mission_id": str(),
		"label": nullable(str()), "journal_cursor": str(), "state": checkpointState,
		"fork_of": nullable(str()), "created_by": nullable(str()), "created_at": str(),
	})

	cacheImage := object(map[string]any{
		"tag": str(), "size": integer(), "created_at": integer(), "referenced_by": stringArray(),
	})
	memoryVersion := object(map[string]any{
		"id": str(), "path": str(), "tier": str(), "sha256": str(), "bytes": integer(),
		"written_at": str(), "written_by": str(), "parent_sha": nullable(str()),
	})
	memoryStats := object(map[string]any{
		"workspace_id": str(),
		"totals":       object(map[string]any{"versions": integer(), "bytes": integer(), "blobs": integer(), "oldest_at": str(), "newest_at": str()}),
		"by_tier":      array(object(map[string]any{"tier": str(), "versions": integer(), "bytes": integer()})),
		"by_agent":     array(object(map[string]any{"agent_slug": str(), "versions": integer(), "bytes": integer(), "newest_at": str()})),
	}, "workspace_id", "totals", "by_tier", "by_agent")
	memoryVersionList := object(map[string]any{
		"workspace_id": str(), "rows": array(memoryVersion), "next_cursor": nullable(str()),
		"limit": integer(), "filters_applied": stringMap(),
	}, "workspace_id", "rows", "next_cursor", "limit", "filters_applied")
	memoryConfig := object(map[string]any{
		"workspace_id": str(), "versions_retention_days": integer(), "is_default": boolean(), "raw_config": nullable(str()),
	}, "workspace_id", "versions_retention_days", "is_default", "raw_config")
	memoryHealth := object(map[string]any{
		"workspace_id": str(), "crew_id": nullable(str()), "computed_at": str(), "overall": number(),
		"metrics": object(map[string]any{"freshness": number(), "coverage": number(), "coherence": number(), "efficiency": number(), "reachability": number()}),
		"details": anyObject(),
	})

	// Manual runs of the two daily memory sweeps (#1702, admin_memory_sync.go).
	// One row per workspace swept, in the field names the worker's summary
	// log line uses; totals is the same shape without workspace_id.
	memorySyncWorkspace := object(map[string]any{
		"workspace_id": str(),
		"candidates":   integer(),
		"writes":       integer(),
		"skipped_threshold": map[string]any{"type": "integer",
			"description": "Candidates below the interaction threshold."},
		"skipped_empty": map[string]any{"type": "integer",
			"description": "Candidates the extractor had nothing to write for — with no model on the curator slot, every one of them."},
		"skipped_opt_out": integer(),
		"purged_opt_out":  integer(),
		"errors": map[string]any{"type": "integer",
			"description": "Candidates that failed inside a sweep that ran; each has a Warn line in the server log."},
		"error": map[string]any{"type": "string",
			"description": "Present when the sweep for this workspace could not run at all (the candidate query failed). The other workspaces still ran."},
	}, "candidates", "writes", "skipped_threshold", "skipped_empty", "skipped_opt_out", "purged_opt_out", "errors")
	memorySync := object(map[string]any{
		"sweep":       map[string]any{"type": "string", "enum": []string{"user_model", "peer_card"}},
		"dry_run":     boolean(),
		"workspaces":  array(memorySyncWorkspace),
		"totals":      memorySyncWorkspace,
		"duration_ms": integer(),
	}, "sweep", "dry_run", "workspaces", "totals", "duration_ms")

	skillAgent := object(map[string]any{
		"agent_id": str(), "agent_slug": str(), "agent_name": str(), "avatar_seed": nullable(str()),
		"avatar_style": nullable(str()), "avatar_url": nullable(str()), "crew_id": nullable(str()),
		"crew_slug": nullable(str()), "crew_name": nullable(str()), "crew_color": nullable(str()),
		"crew_icon": nullable(str()), "crew_avatar_style": nullable(str()), "missing_credentials": stringArray(),
	})
	skillUsage := object(map[string]any{"uses_7d": integer(), "errors_7d": integer(), "uses_total": integer(), "last_used_at": nullable(str())})
	skill := object(map[string]any{
		"id": str(), "name": str(), "slug": str(), "display_name": str(), "description": nullable(str()),
		"version": str(), "author": nullable(str()), "category": str(), "source": str(), "icon": nullable(str()),
		"verification": str(), "downloads": integer(), "rating_avg": nullable(number()), "rating_count": integer(),
		"tags": nullable(str()), "featured": boolean(), "pricing_tier": str(), "tool_count": nullable(integer()),
		"vendor": nullable(str()), "homepage": nullable(str()), "spdx_license": nullable(str()), "runtime": str(),
		"maturity": str(), "scan_status": str(), "description_quality": nullable(str()), "created_at": str(),
		"updated_at": str(), "installed_on": array(skillAgent), "lifecycle_state": str(), "needs_credentials": stringArray(), "usage": skillUsage,
	})
	skillDetail := object(map[string]any{
		"id": str(), "name": str(), "slug": str(), "display_name": str(), "description": nullable(str()),
		"version": str(), "author": nullable(str()), "category": str(), "source": str(), "icon": nullable(str()),
		"verification": str(), "downloads": integer(), "rating_avg": nullable(number()), "rating_count": integer(),
		"tags": nullable(str()), "featured": boolean(), "pricing_tier": str(), "tool_count": nullable(integer()),
		"vendor": nullable(str()), "homepage": nullable(str()), "spdx_license": nullable(str()), "runtime": str(),
		"maturity": str(), "scan_status": str(), "description_quality": nullable(str()), "created_at": str(),
		"updated_at": str(), "installed_on": array(skillAgent), "lifecycle_state": str(), "needs_credentials": stringArray(), "usage": skillUsage, "content": nullable(str()),
		"credential_requirements": nullable(str()), "mcp_server_command": nullable(str()), "mcp_server_image": nullable(str()),
		"mcp_transport": nullable(str()), "dependencies": nullable(str()), "license": nullable(str()), "agent_count": integer(),
		"security_score": nullable(integer()), "allowed_domains": nullable(str()), "changelog": nullable(str()),
	})
	recipeCredential := object(map[string]any{"env_var_name": str(), "provider": str(), "type": str(), "label": str(), "help_url": nullable(str())})
	recipeServer := object(map[string]any{
		"name": str(), "display_name": str(), "transport": str(), "command": nullable(str()), "args": stringArray(),
		"endpoint": nullable(str()), "env_mapping": stringMap(), "icon": nullable(str()),
	})
	recipe := object(map[string]any{
		"slug": str(), "name": str(), "description": str(), "icon": str(), "color": str(), "crew_slug": str(),
		"credentials": array(recipeCredential), "mcp_servers": array(recipeServer),
	})
	project := object(map[string]any{
		"id": str(), "workspace_id": str(), "name": str(), "slug": str(), "description": nullable(str()), "icon": nullable(str()),
		"color": str(), "status": str(), "priority": str(), "health": str(), "lead_type": nullable(str()), "lead_id": nullable(str()),
		"lead_name": nullable(str()), "start_date": nullable(str()), "target_date": nullable(str()), "created_at": str(), "updated_at": str(),
		"issue_count": integer(), "done_count": integer(), "progress": integer(),
	})
	resources := object(map[string]any{
		"datastores": array(object(map[string]any{"type": str(), "name": str(), "host": str(), "port": str()})),
		"tools":      array(object(map[string]any{"type": str(), "name": str()})),
	})
	capabilities := object(map[string]any{
		"crew_id": str(), "crew_slug": str(), "container": resources,
		"integrations": array(object(map[string]any{"id": str(), "name": str(), "display_name": nullable(str()), "tools": stringArray()})),
		"agents":       array(object(map[string]any{"slug": str(), "name": str()})),
		"runtimes":     anyObject(), "schema": anyObject(),
	})
	_ = skill
	_ = skillDetail

	memoryInventory := object(map[string]any{
		"source": str(), "peer_generation": str(),
		"scopes": map[string]any{"type": "object", "additionalProperties": str()},
		"documents": array(object(map[string]any{
			"id": str(), "name": str(), "scope": str(), "state": str(), "content": str(),
			"bytes":    map[string]any{"type": "integer", "nullable": true},
			"revision": str(), "updated_at": str(), "history_path": str(),
		}, "id", "name", "scope", "state", "bytes")),
	}, "source", "peer_generation", "scopes", "documents")
	// Admin › Security across workspaces (admin_instance_keeper.go).
	instanceWsRef := map[string]any{"workspace_id": str(), "workspace_name": str(), "workspace_slug": str()}
	instanceWsRequired := []string{"workspace_id", "workspace_name", "workspace_slug"}
	// Admin › Data retention (admin_instance_retention.go). Every window is a
	// number of days, or null for forever.
	retentionKeys := []string{"routine_runs_days", "approvals_days", "audit_days", "credential_audit_days",
		"memory_versions_days", "page_panel_data_days", "inbox_days", "chats_days", "keeper_decisions_days"}
	retentionProps := map[string]any{}
	for _, k := range retentionKeys {
		retentionProps[k] = nullable(integer())
	}
	retentionWindows := object(retentionProps, retentionKeys...)
	retentionPatch := object(retentionProps)
	instanceGovSettings := map[string]any{
		"enabled": boolean(), "security_contact_user_id": str(), "deny_notify_min_risk": integer(),
		"watch_spec": str(), "watch_presets": stringArray(), "require_second_approver": boolean(),
		"gov_model_provider": str(), "gov_model_id": str(), "gov_model_credential_id": str(),
		"auto_lease_seconds": integer(), "behavior_sample_every": integer(),
	}
	instanceGovPatch := map[string]any{
		"enabled": boolean(), "security_contact_user_id": str(), "deny_notify_min_risk": integer(),
		"watch_spec": str(), "watch_presets": stringArray(), "require_second_approver": boolean(),
		"gov_model_provider": str(), "gov_model_id": str(), "gov_model_credential_id": str(),
		"auto_lease_seconds": integer(), "behavior_sample_every": integer(),
	}
	instanceDefaults := object(map[string]any{
		"applied": boolean(), "configured": boolean(), "preview_id": str(),
		"defaults": object(instanceGovSettings, "enabled", "deny_notify_min_risk"),
		"changes":  array(object(map[string]any{"field": str(), "before": map[string]any{}, "after": map[string]any{}}, "field", "before", "after")),
	}, "applied", "configured", "defaults", "changes", "preview_id")
	withProps := func(maps ...map[string]any) map[string]any {
		out := map[string]any{}
		for _, m := range maps {
			for k, v := range m {
				out[k] = v
			}
		}
		return out
	}
	return map[string]DomainSchema{
		"GET /api/v1/admin/stats":      {Response: stats},
		"GET /api/v1/admin/users":      {Response: array(adminUser), SuccessHeaders: adminScopeHeader},
		"GET /api/v1/admin/workspaces": {Response: array(adminWorkspace), SuccessHeaders: adminScopeHeader},
		"GET /api/v1/admin/users/{userId}/sessions": {Response: object(map[string]any{
			"sessions": array(adminUserSession), "cli_tokens": array(adminUserCLIToken),
		}, "sessions", "cli_tokens")},
		"POST /api/v1/admin/users/{userId}/sessions/{sessionId}/revoke": {SuccessStatuses: []string{"204"}, Request: emptyRequest},
		"POST /api/v1/admin/users/{userId}/sessions/revoke-all":         {Response: object(map[string]any{"revoked": integer()}, "revoked"), Request: emptyRequest},
		"POST /api/v1/admin/users/{userId}/unlock":                      {SuccessStatuses: []string{"204"}, Request: emptyRequest},
		"POST /api/v1/admin/instance/people": {SuccessStatuses: []string{"201"},
			Request: object(map[string]any{"email": str(), "full_name": str(), "memberships": array(instanceMembership)}, "email"),
			Response: object(map[string]any{"user_id": str(), "email": str(), "memberships": array(instanceMembership), "setup_url": str(), "expires_at": str()},
				"user_id", "email", "memberships", "setup_url", "expires_at")},
		"POST /api/v1/admin/instance/people/{userId}/suspend": {Request: object(map[string]any{"reason": str()}),
			Response: object(map[string]any{"user_id": str(), "suspended_at": str(), "sessions_revoked": integer(), "cli_tokens_revoked": integer()},
				"user_id", "suspended_at", "sessions_revoked", "cli_tokens_revoked")},
		"POST /api/v1/admin/instance/people/{userId}/reactivate": {SuccessStatuses: []string{"204"}, Request: emptyRequest},
		"POST /api/v1/admin/instance/people/{userId}/setup-link": {Request: emptyRequest,
			Response: object(map[string]any{"user_id": str(), "setup_url": str(), "expires_at": str()}, "user_id", "setup_url", "expires_at")},
		"DELETE /api/v1/admin/instance/people/{userId}/setup-link": {SuccessStatuses: []string{"204"}},
		"PUT /api/v1/admin/instance/admins/{userId}":               {SuccessStatuses: []string{"204"}, Request: emptyRequest},
		"DELETE /api/v1/admin/instance/admins/{userId}":            {SuccessStatuses: []string{"204"}},
		"PUT /api/v1/admin/instance/workspaces/{workspaceId}/members/{userId}": {SuccessStatuses: []string{"200", "201"},
			Request: object(map[string]any{"role": str()}, "role"),
			Response: object(map[string]any{"workspace_id": str(), "user_id": str(), "role": str(), "created": boolean()},
				"workspace_id", "user_id", "role", "created")},
		"DELETE /api/v1/admin/instance/workspaces/{workspaceId}/members/{userId}": {SuccessStatuses: []string{"204"}},
		"POST /api/v1/admin/instance/workspaces": {SuccessStatuses: []string{"201"},
			Request: object(map[string]any{"name": str(), "slug": str(), "owner_user_id": str(), "preferred_language": nullable(str())}, "name", "slug", "owner_user_id"),
			Response: object(map[string]any{"id": str(), "name": str(), "slug": str(), "owner_user_id": str(), "created_at": str()},
				"id", "name", "slug", "owner_user_id", "created_at")},
		"POST /api/v1/admin/instance/workspaces/{workspaceId}/transfer-ownership": {
			Request: object(map[string]any{"user_id": str()}, "user_id"),
			Response: object(map[string]any{"workspace_id": str(), "owner_user_id": str(), "previous_owner_user_ids": array(str())},
				"workspace_id", "owner_user_id", "previous_owner_user_ids")},
		"DELETE /api/v1/admin/instance/workspaces/{workspaceId}": {SuccessStatuses: []string{"204"},
			Request: object(map[string]any{"confirm_slug": str()}, "confirm_slug")},
		"GET /api/v1/admin/instance/keeper/governance": {Response: object(map[string]any{
			"defaults":   object(withProps(instanceGovSettings, map[string]any{"configured": boolean(), "effective_second_approver": anyObject()}), "configured", "enabled"),
			"workspaces": array(object(withProps(instanceGovSettings, map[string]any{"configured": boolean(), "effective_second_approver": anyObject()}, instanceWsRef), append([]string{"configured", "enabled"}, instanceWsRequired...)...)),
		}, "defaults", "workspaces")},
		"PUT /api/v1/admin/instance/keeper/governance": {
			Request: object(map[string]any{
				"workspaces": stringArray(), "all": boolean(), "dry_run": boolean(), "expect_preview": str(),
				"set": object(instanceGovPatch),
			}, "set"),
			Response: object(map[string]any{
				"applied": boolean(), "changed": integer(), "preview_id": str(),
				"workspaces": array(object(withProps(instanceWsRef, map[string]any{
					"changes":  array(object(map[string]any{"field": str(), "before": map[string]any{}, "after": map[string]any{}}, "field", "before", "after")),
					"warnings": stringArray(),
				}), append([]string{"changes"}, instanceWsRequired...)...)),
			}, "applied", "changed", "workspaces", "preview_id")},
		"GET /api/v1/admin/instance/keeper/governance/defaults": {Response: instanceDefaults},
		"PUT /api/v1/admin/instance/keeper/governance/defaults": {
			Request:  object(map[string]any{"dry_run": boolean(), "expect_preview": str(), "set": object(instanceGovPatch)}, "set"),
			Response: instanceDefaults},
		"GET /api/v1/admin/instance/keeper/requests": {Response: object(map[string]any{
			"items": array(object(withProps(map[string]any{
				"id": str(), "agent_id": str(), "agent_name": str(), "crew_id": str(), "credential_id": str(), "credential_name": str(),
				"intent": str(), "request_type": str(), "command": nullable(str()), "decision": nullable(str()), "reason": nullable(str()),
				"risk_score": nullable(integer()), "exit_code": nullable(integer()), "ollama_prompt": nullable(str()), "ollama_raw_response": nullable(str()),
				"created_at": str(), "decided_at": nullable(str()), "judge_profile": nullable(str()),
			}, map[string]any{"workspace_id": str(), "workspace_name": str()}), "id", "agent_id", "request_type", "created_at", "workspace_id", "workspace_name")),
			"total":        integer(),
			"counts":       object(map[string]any{"allow": integer(), "deny": integer(), "escalate": integer(), "pending": integer()}, "allow", "deny", "escalate", "pending"),
			"by_workspace": array(object(withProps(instanceWsRef, map[string]any{"count": integer()}), append([]string{"count"}, instanceWsRequired...)...)),
			"by_type":      integerMap(),
		}, "items", "total", "counts", "by_workspace", "by_type")},
		"GET /api/v1/admin/instance/keeper/health": {Response: object(map[string]any{
			"workspaces": array(object(map[string]any{
				"workspace_id": str(), "workspace_name": str(), "workspace_slug": str(),
				"samples": integer(), "allow": integer(), "deny": integer(), "escalate": integer(), "judge_failures": integer(),
				"allow_rate": number(), "deny_rate": number(), "escalate_rate": number(), "progressed_rate": number(), "judge_failure_rate": number(),
				"p95_latency_ms": integer(), "min_samples": integer(), "alarm_progressed_rate": number(), "alarm_judge_failure_rate": number(),
				"alarm": nullable(object(map[string]any{"kind": str(), "summary": str(), "at": str()})), "oldest": str(), "newest": str(),
			}, "workspace_id", "workspace_name", "samples", "min_samples")),
		}, "workspaces")},
		"GET /api/v1/admin/instance/retention": {Response: object(map[string]any{
			"workspaces":          array(object(withProps(instanceWsRef, map[string]any{"windows": retentionWindows}), append([]string{"windows"}, instanceWsRequired...)...)),
			"defaults":            retentionWindows,
			"defaults_configured": stringArray(),
			"keys": array(object(map[string]any{
				"key": str(), "label": str(), "default_days": nullable(integer()), "forever_allowed": boolean(),
				"min_days": integer(), "max_days": integer(), "detail": str(),
			}, "key", "label", "default_days", "forever_allowed", "min_days", "max_days", "detail")),
			"housekeeping": array(object(map[string]any{"key": str(), "label": str(), "value": str(), "detail": str()},
				"key", "label", "value", "detail")),
		}, "workspaces", "defaults", "defaults_configured", "keys", "housekeeping")},
		"PUT /api/v1/admin/instance/retention": {
			Request: object(map[string]any{
				"workspace_ids": nullable(stringArray()), "windows": retentionPatch, "dry_run": boolean(), "expect_preview": str(),
			}, "workspace_ids", "windows"),
			Response: object(map[string]any{
				"applied": boolean(), "dry_run": boolean(), "changed": integer(), "rows_affected_next_sweep": integer(),
				"preview_id": str(),
				"workspaces": array(object(withProps(instanceWsRef, map[string]any{
					"changes": array(object(map[string]any{
						"key": str(), "from": nullable(integer()), "to": nullable(integer()), "rows_affected_next_sweep": integer(),
					}, "key", "from", "to", "rows_affected_next_sweep")),
				}), append([]string{"changes"}, instanceWsRequired...)...)),
			}, "applied", "dry_run", "changed", "rows_affected_next_sweep", "workspaces", "preview_id")},
		"GET /api/v1/admin/instance/retention/defaults": {Response: object(map[string]any{
			"defaults": retentionWindows, "configured": stringArray(),
		}, "defaults", "configured")},
		"PUT /api/v1/admin/instance/retention/defaults": {
			Request: object(map[string]any{"windows": retentionPatch, "dry_run": boolean(), "expect_preview": str()}, "windows"),
			Response: object(map[string]any{
				"applied": boolean(), "dry_run": boolean(), "affects_existing": boolean(),
				"changes":    array(object(map[string]any{"key": str(), "from": nullable(integer()), "to": nullable(integer())}, "key", "from", "to")),
				"defaults":   retentionWindows,
				"preview_id": str(),
			}, "applied", "dry_run", "affects_existing", "changes", "defaults", "preview_id")},
		// Admin › Backups across workspaces. incomplete is null when the
		// bundle's gaps were never recorded and [] when there are none;
		// proof_level is 1 checksum, 2 contents checked, 3 test restore.
		"GET /api/v1/admin/instance/backups/bundles": {Response: object(map[string]any{
			"bundles": array(object(map[string]any{
				"id": str(), "path": str(), "file_name": str(), "scope": str(), "scope_level": str(), "kind": str(), "slug": str(),
				"workspace_id": str(), "workspace_name": str(), "workspace_slug": str(), "created_at": str(), "created_by": str(),
				"size_bytes": integer(), "payload_sha256": str(), "encrypted": boolean(), "format_version": integer(),
				"plan_id": str(), "run_id": str(), "pinned": boolean(), "proof_level": integer(), "proof_checked_at": nullable(str()),
				"drill_result": str(), "drill_at": nullable(str()), "drill_report": nullable(map[string]any{}),
				"incomplete": nullable(array(object(map[string]any{"kind": str(), "detail": str(), "count": integer(), "workspace": str()}, "kind", "detail", "count"))),
			}, "id", "path", "file_name", "scope", "scope_level", "kind", "workspace_id", "created_at", "size_bytes", "payload_sha256",
				"encrypted", "format_version", "pinned", "proof_level", "proof_checked_at", "drill_result", "drill_at", "incomplete")),
		}, "bundles")},
		// One catalogued bundle's manifest (plaintext, never decrypted), any
		// scope; contents differs by scope (workspace, crews, instance).
		"GET /api/v1/admin/instance/backups/bundles/inspect": {Response: object(map[string]any{
			"format_version": integer(), "crewship_version_at_backup": str(), "scope": str(), "scope_level": str(),
			"created_at": str(),
			"created_by": object(map[string]any{"user_id": str(), "email": str(), "role": str()}),
			"encryption": object(map[string]any{"enabled": boolean(), "algorithm": str()}, "enabled"),
			"checksums":  object(map[string]any{"payload_sha256": str()}, "payload_sha256"),
			"contents":   map[string]any{"type": "object"},
		}, "format_version", "scope", "created_at", "encryption", "checksums", "contents")},
		"POST /api/v1/admin/instance/backups/bundles/pin": {
			Request:  object(map[string]any{"path": str()}, "path"),
			Response: object(map[string]any{"path": str(), "pinned": boolean()}, "path", "pinned")},
		"POST /api/v1/admin/instance/backups/bundles/unpin": {
			Request:  object(map[string]any{"path": str()}, "path"),
			Response: object(map[string]any{"path": str(), "pinned": boolean()}, "path", "pinned")},
		// kind restore | dry_run | drill; result ok | partial | failed; report
		// is the full restore report the server returned at the time.
		"GET /api/v1/admin/instance/backups/restores": {Response: object(map[string]any{
			"restores": array(object(map[string]any{
				"id": str(), "kind": str(), "actor_user_id": str(), "actor_email": str(), "bundle_path": str(),
				"target": str(), "result": str(), "report": map[string]any{}, "created_at": str(),
			}, "id", "kind", "actor_user_id", "actor_email", "bundle_path", "target", "result", "report", "created_at")),
		}, "restores")},
		// Whole-instance backup and recovery. Runs (POST …/run, GET
		// …/run/{runId}) are backup_runs rows: see backupPlanSchemaCatalog.
		// Counts only: no key material leaves the server.
		"GET /api/v1/admin/instance/backups/vault-keys": {Response: object(map[string]any{
			"versions": array(object(map[string]any{
				"version": str(), "env": str(), "active": boolean(), "envelopes": integer(), "present": boolean(),
			}, "version", "env", "active", "envelopes", "present")),
			"recovery_kit": object(map[string]any{"available": boolean(), "enabled": boolean()}, "available", "enabled"),
		}, "versions", "recovery_kit")},
		"PUT /api/v1/admin/instance/backups/settings/recovery-kit": {
			Request:  object(map[string]any{"enabled": boolean()}, "enabled"),
			Response: object(map[string]any{"enabled": boolean()}, "enabled")},
		// The key is used for this check only and never stored.
		"POST /api/v1/admin/instance/backups/bundles/check": {
			Request: object(map[string]any{"path": str(), "identity": str(), "passphrase": str()}, "path"),
			Response: object(map[string]any{
				"ok": boolean(), "proof_level": integer(), "detail": str(), "problems": stringArray(),
				"absent":  array(object(map[string]any{"kind": str(), "detail": str(), "count": integer(), "workspace": str()}, "kind", "detail", "count")),
				"entries": integer(), "tables": integer(), "files": integer(),
			}, "ok", "proof_level", "detail", "problems", "absent")},
		"POST /api/v1/admin/instance/backups/restore/checks": {
			Request: object(map[string]any{"path": str(), "target": str(), "identity": str(), "passphrase": str(), "as_workspace": str(), "as_crew": str()}, "path", "target"),
			Response: object(map[string]any{
				"space":   object(map[string]any{"ok": boolean(), "need_bytes": integer(), "free_bytes": integer()}, "ok", "need_bytes", "free_bytes"),
				"format":  object(map[string]any{"ok": boolean(), "version": integer(), "converter": boolean()}, "ok", "version", "converter"),
				"runtime": object(map[string]any{"ok": boolean(), "detail": str(), "warnings": stringArray()}, "ok", "detail", "warnings"),
				// One runtime check per complete container environment (Track E).
				"environments": array(object(map[string]any{
					"crew": str(), "action": str(), "platform": str(), "host_platform": str(),
					"missing_blobs": integer(), "need_bytes": integer(), "detail": str(),
				}, "crew", "action", "platform", "host_platform", "missing_blobs", "need_bytes", "detail")),
				"unsafe":    stringArray(),
				"conflicts": object(map[string]any{"ok": boolean(), "detail": str()}, "ok", "detail"),
			}, "space", "format", "runtime", "unsafe", "conflicts", "environments")},
		// Loads the complete container environments `crewship recover`
		// staged; each one restored, rebuilt or skipped with its reason.
		"POST /api/v1/admin/instance/backups/services/land": {
			Request:  object(map[string]any{"dry_run": boolean()}),
			Response: object(map[string]any{"images": integer(), "dry_run": boolean()}, "images", "dry_run")},
		"POST /api/v1/admin/instance/backups/environments/land": {
			Request: object(map[string]any{"dry_run": boolean()}),
			Response: object(map[string]any{
				"dir": str(), "staged": boolean(), "dry_run": boolean(),
				"environments": array(object(map[string]any{
					"workspace": str(), "crew": str(), "result": str(), "reason": str(), "image": str(), "container": str(),
					"volumes": stringArray(), "unsafe": stringArray(), "notes": stringArray(),
				}, "workspace", "crew", "result", "unsafe")),
			}, "dir", "staged", "dry_run", "environments")},
		"POST /api/v1/admin/instance/backups/drills": {
			Request: object(map[string]any{"path": str(), "sha256": str(), "result": str(), "report": map[string]any{}}, "path", "sha256", "result"),
			Response: object(map[string]any{
				"path": str(), "proof_level": integer(), "drill_result": str(), "drill_at": str(), "report_id": str(),
			}, "path", "proof_level", "drill_result", "drill_at", "report_id")},
		"GET /api/v1/admin/instance/backups/drills": {Response: array(object(map[string]any{
			"id": str(), "kind": str(), "actor": str(), "bundle_path": str(), "source_scope": str(), "source_name": str(),
			"source_date": str(), "target": str(), "result": str(), "warnings": integer(), "created_at": str(),
		}, "id", "kind", "actor", "bundle_path", "source_scope", "source_name", "source_date", "target", "result", "warnings", "created_at"))},
		"GET /api/v1/admin/instance/holds": {Response: array(object(map[string]any{
			"key": str(), "reason": str(), "count": integer(), "detail": str(), "created_at": str(),
		}, "key", "reason", "count", "detail", "created_at"))},
		"POST /api/v1/admin/instance/holds/resume": {
			Request:  object(map[string]any{"key": str()}, "key"),
			Response: object(map[string]any{"key": str(), "resumed": boolean()}, "key", "resumed")},
		"GET /api/v1/admin/instance/audit": {Response: array(object(map[string]any{
			"id": str(), "user_id": nullable(str()), "user_email": nullable(str()), "action": str(), "entity_type": str(),
			"entity_id": nullable(str()), "target_workspace_id": nullable(str()), "metadata": str(), "created_at": str(),
		}, "id", "user_id", "user_email", "action", "entity_type", "entity_id", "target_workspace_id", "metadata", "created_at"))},
		"GET /api/v1/admin/health": {Response: object(map[string]any{"uptime_seconds": integer(), "log_level": anyObject(), "encryption_key_source": str(), "db": anyObject(), "disk": anyObject()})},
		"GET /api/v1/admin/security-posture": {Response: object(map[string]any{"environment": str(), "encryption_key_configured": boolean(), "plaintext_secrets_allowed": boolean(), "private_endpoints_ceiling": boolean(), "signup_open": boolean(), "oauth_configured": boolean(), "email_configured": boolean(), "rate_limit_disabled": boolean(), "rate_limit_effectively_disabled": boolean(), "warnings": array(object(map[string]any{"key": str(), "severity": str(), "message": str()}, "key", "severity", "message"))},
			"environment", "encryption_key_configured", "plaintext_secrets_allowed", "private_endpoints_ceiling", "signup_open",
			"oauth_configured", "email_configured", "rate_limit_disabled", "rate_limit_effectively_disabled", "warnings")},
		"GET /api/v1/admin/log-level":         {Response: object(map[string]any{"level": str(), "baseline": str(), "expires_at": nullable(str())})},
		"PUT /api/v1/admin/log-level":         {Request: object(map[string]any{"level": str(), "ttl_seconds": integer()})},
		"GET /api/v1/admin/rate-limits":       {Response: object(map[string]any{"limiters": array(anyObject())})},
		"PUT /api/v1/admin/rate-limits/{key}": {Request: object(map[string]any{"value": integer()})},
		"GET /api/v1/approvals": {Response: object(map[string]any{"rows": array(approval), "status": str(), "count": integer(), "has_more": map[string]any{"type": "boolean"}},
			// The ENVELOPE needs its own required list, not just the row. Without
			// it `{}` validates: an empty object is a valid instance of a schema
			// whose every property is optional, so a handler that returned
			// nothing at all would satisfy the contract. ApprovalsHandler.List
			// writes this envelope as a map literal, so there is no struct to
			// derive it from — see the DTO note in
			// docs/specs/response-shape-contract.md.
			"rows", "status", "count", "has_more")},
		"GET /api/v1/approvals/{id}":                               {Response: approval},
		"POST /api/v1/approvals/{id}/decide":                       {Request: object(map[string]any{"status": str(), "comment": str()}), Response: object(map[string]any{"status": str(), "decided_by": str()})},
		"POST /api/v1/approvals/{id}/cancel":                       {Request: object(map[string]any{"reason": str()}), Response: object(map[string]any{"status": str(), "cancelled_by": str()})},
		"POST /api/v1/approvals/reset-auto-tuning":                 {Request: object(map[string]any{"tool": str()}), Response: object(map[string]any{"tool": str(), "rows_deleted": integer(), "workspace_id": str()})},
		"GET /api/v1/missions/{missionId}/checkpoints":             {Response: object(map[string]any{"checkpoints": array(checkpoint), "count": integer(), "mission_id": str()})},
		"POST /api/v1/missions/{missionId}/checkpoints":            {Request: object(map[string]any{"label": str()}), Response: checkpoint},
		"GET /api/v1/checkpoints/{id}":                             {Response: checkpoint},
		"POST /api/v1/checkpoints/{id}/restore":                    {Response: object(map[string]any{"checkpoint": checkpoint, "journal_cursor": str(), "warn_divergence": stringArray()})},
		"POST /api/v1/checkpoints/{id}/fork":                       {Request: object(map[string]any{"label": str()}), Response: object(map[string]any{"new_mission_id": str(), "new_checkpoint_id": str()})},
		"GET /api/v1/cache/images":                                 {Response: object(map[string]any{"images": array(cacheImage)})},
		"DELETE /api/v1/cache/images/{tag}":                        {Response: object(map[string]any{"tag": str(), "status": str()})},
		"GET /api/v1/admin/memory/stats":                           {Response: memoryStats},
		"GET /api/v1/admin/memory/versions":                        {Response: memoryVersionList},
		"GET /api/v1/admin/memory/config":                          {Response: memoryConfig},
		"PATCH /api/v1/admin/memory/config":                        {Request: object(map[string]any{"versions_retention_days": integer()}), Response: memoryConfig},
		"POST /api/v1/admin/memory/user-model-sync":                {Response: memorySync},
		"POST /api/v1/admin/memory/peer-card-sync":                 {Response: memorySync},
		"GET /api/v1/admin/memory/versions/{id}/content":           {Response: map[string]any{"type": "string", "format": "binary"}, ResponseMedia: []string{"text/markdown", "application/octet-stream"}},
		"GET /api/v1/memory/health":                                {Response: memoryHealth},
		"GET /api/v1/agents/{agentId}/memory":                      {Response: memoryInventory},
		"GET /api/v1/crews/{crewId}/memory":                        {Response: memoryInventory},
		"GET /api/v1/memory/versions":                              {Response: memoryVersionList},
		"GET /api/v1/memory/versions/{sha}":                        {Response: map[string]any{"type": "string", "format": "binary"}, ResponseMedia: []string{"application/octet-stream"}},
		"POST /api/v1/memory/versions/{sha}/restore":               {Request: object(map[string]any{"path": str(), "canonical_path": str(), "tier": str()})},
		"POST /api/v1/workspaces/{workspaceId}/skills/import":      {Request: object(map[string]any{"url": str(), "content": str(), "allow_unsafe_license": boolean()})},
		"POST /api/v1/workspaces/{workspaceId}/skills/generate":    {Request: object(map[string]any{"slug": str(), "prompt": str(), "model": str()}), Response: object(map[string]any{"skill_id": str(), "slug": str(), "content": str(), "scan_status": str(), "scan_reason": nullable(str()), "description_quality": nullable(str())})},
		"POST /api/v1/workspaces/{workspaceId}/skills/bulk-import": {Request: object(map[string]any{"git_url": str(), "git_ref": str(), "paths": stringArray(), "vendor": str(), "allow_unsafe_license": boolean(), "dry_run": boolean()})},
		"GET /api/v1/recipes":                                      {Response: array(recipe)},
		"GET /api/v1/recipes/{slug}":                               {Response: recipe},
		"GET /api/v1/recipes/{slug}/preview":                       {Response: object(map[string]any{"recipe": recipe, "needed_credentials": stringArray(), "existing_credentials": booleanMap(), "crew_slug_available": boolean(), "resolved_crew_slug": str()})},
		"POST /api/v1/recipes/{slug}/install":                      {Request: object(map[string]any{"credential_values": stringMap(), "account_labels": stringMap()}), Response: object(map[string]any{"crew_id": str(), "crew_slug": str(), "credentials_added": stringArray(), "credentials_reused": stringArray(), "mcp_servers_added": stringArray()})},
		"POST /api/v1/projects":                                    {Request: object(map[string]any{"name": str(), "description": nullable(str()), "icon": nullable(str()), "color": str(), "status": str(), "priority": str(), "lead_type": nullable(str()), "lead_id": nullable(str()), "start_date": nullable(str()), "target_date": nullable(str())}), Response: project},
		"PATCH /api/v1/projects/{projectId}":                       {Request: object(map[string]any{"name": nullable(str()), "description": nullable(str()), "icon": nullable(str()), "color": nullable(str()), "status": nullable(str()), "priority": nullable(str()), "health": nullable(str()), "lead_type": nullable(str()), "lead_id": nullable(str()), "start_date": nullable(str()), "target_date": nullable(str())}), Response: project},
		"GET /api/v1/projects/{projectId}/stats":                   {Response: object(map[string]any{"total_issues": integer(), "completed_issues": integer(), "by_status": integerMap(), "by_assignee": array(anyObject()), "by_label": array(anyObject()), "crews": stringArray()})},
		"GET /api/v1/crews/{crewId}/capabilities":                  {Response: capabilities},
	}
}
