package main

// adminSpecialRequestSchemaCatalog contains request contracts for the admin,
// memory, portability, evaluation and other small special-domain handlers.
// Keep this catalog isolated: request audits are merged by several agents and
// duplicate filenames/catalog functions make an otherwise valid merge unsafe.
func adminSpecialRequestSchemaCatalog() (map[string]DomainSchema, map[string]any) {
	str := func() map[string]any { return map[string]any{"type": "string"} }
	integer := func() map[string]any { return map[string]any{"type": "integer"} }
	boolean := func() map[string]any { return map[string]any{"type": "boolean"} }
	anyValue := func() map[string]any { return map[string]any{} }
	object := func(p map[string]any) map[string]any { return map[string]any{"type": "object", "properties": p} }
	array := func(i map[string]any) map[string]any { return map[string]any{"type": "array", "items": i} }
	nullable := func(s map[string]any) map[string]any {
		out := map[string]any{}
		for k, v := range s {
			out[k] = v
		}
		out["nullable"] = true
		return out
	}
	req := func(p map[string]any, fields ...string) map[string]any {
		out := object(p)
		if len(fields) > 0 {
			out["required"] = fields
		}
		return out
	}
	empty := func() map[string]any {
		return map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}
	}
	stringMap := func() map[string]any { return map[string]any{"type": "object", "additionalProperties": str()} }

	doc := object(map[string]any{"path": str(), "tier": str(), "scope": str(), "title": str(), "tags": array(str()), "sources": array(str()), "body": str()})
	config := object(map[string]any{
		"enabled": anyValue(), "judge_provider": str(), "judge_endpoint_url": str(), "judge_wire": str(), "judge_model": str(), "judge_timeout_ms": integer(), "judge_profile": str(),
		"judge_evidence": anyValue(), "judge_evidence_facts": str(), "judge_hard_gate": anyValue(), "judge_precedent": anyValue(), "judge_precedent_n": integer(), "judge_consistency_samples": integer(), "judge_escalate_from": integer(), "judge_prompt_budget_tokens": integer(),
	})
	governance := object(map[string]any{"enabled": boolean(), "security_contact_user_id": str(), "deny_notify_min_risk": integer(), "watch_spec": str(), "watch_presets": array(str()), "require_second_approver": boolean(), "auto_lease_seconds": integer(), "behavior_sample_every": integer(), "gov_model_provider": str(), "gov_model_id": str(), "gov_model_credential_id": str()})

	components := map[string]any{
		"AdminBackupCreateRequest":          req(map[string]any{"scope": map[string]any{"type": "string", "enum": []string{"crew", "workspace"}}, "scope_level": map[string]any{"type": "string", "enum": []string{"quick", "standard", "full"}}, "crew_id": str(), "passphrase": str(), "recipient": str(), "no_encrypt": boolean(), "output_dir": str()}, "scope"),
		"AdminBackupRestoreRequest":         req(map[string]any{"path": str(), "passphrase": str(), "identity": str(), "as_workspace": str(), "as_crew": str(), "replace": boolean(), "dry_run": boolean(), "files_only": boolean()}, "path"),
		"AdminBackupRotateRequest":          object(map[string]any{"keep_last": integer(), "keep_days": integer(), "dry_run": boolean()}),
		"AdminBackupSelfTestRequest":        req(map[string]any{"crew_id": str()}, "crew_id"),
		"AdminLogLevelRequest":              req(map[string]any{"level": str(), "ttl_seconds": integer()}, "level"),
		"AdminRateLimitRequest":             req(map[string]any{"value": integer()}, "value"),
		"AdminMemoryConfigRequest":          object(map[string]any{"versions_retention_days": integer()}),
		"AdminKeeperConfigRequest":          config,
		"AdminKeeperGovernanceRequest":      governance,
		"AdminKeeperAuxRequest":             object(map[string]any{"provider": str(), "model": str(), "timeout_ms": integer(), "credential_id": str()}),
		"AdminKeeperJudgeTestRequest":       object(map[string]any{"judge_endpoint_url": str(), "judge_model": str()}),
		"AdminKeeperHostedJudgeTestRequest": req(map[string]any{"provider": str(), "model": str(), "credential_id": str()}, "provider", "model", "credential_id"),
		"AdminKeeperResolveRequest":         req(map[string]any{"decision": map[string]any{"type": "string", "enum": []string{"approve", "deny"}}, "reason": str(), "adjudicator": str()}, "decision"),
		"AdminKeeperAskRequest":             object(map[string]any{"credential_id": str(), "credential_name": str(), "intent": str(), "requesting_agent_id": str(), "requesting_crew_id": str(), "task_id": str(), "workspace_id": str()}),
		"AdminKeeperFindingsTestRequest":    object(map[string]any{"crew_id": str(), "workspace_id": str(), "reason": str()}),
		"AdminKeeperReviewRequest":          object(map[string]any{"agent_id": str(), "crew_id": str(), "failure_snippet": str(), "prior_lesson": str(), "recent_tool_calls": array(str()), "skill_id": str(), "tool_args_snippet": str(), "tool_name": str(), "trigger": str(), "workspace_id": str()}),
		"MemoryImportRequest":               req(map[string]any{"crew_id": str(), "agent_slug": str(), "documents": array(doc)}, "crew_id", "agent_slug", "documents"),
		"MemoryHybridSearchRequest":         req(map[string]any{"query": str(), "limit": integer(), "scope": str(), "crew_id": str()}, "query"),
		"MemoryVersionRestoreRequest":       req(map[string]any{"path": str(), "canonical_path": str(), "tier": str()}, "path", "canonical_path", "tier"),
		"RecipeInstallRequest":              object(map[string]any{"credential_values": stringMap(), "account_labels": stringMap()}),
		"TemplateCreateRequest":             req(map[string]any{"name": str(), "description": nullable(str()), "template_json": anyValue(), "icon": nullable(str()), "color": nullable(str())}, "name", "template_json"),
		"TemplateUpdateRequest":             object(map[string]any{"name": nullable(str()), "description": nullable(str()), "template_json": anyValue(), "icon": nullable(str()), "color": nullable(str())}),
		"WorkflowTemplateCreateRequest":     req(map[string]any{"name": str(), "description": nullable(str()), "template_json": str(), "icon": nullable(str()), "color": nullable(str())}, "name", "template_json"),
		"WorkflowTemplateUpdateRequest":     object(map[string]any{"name": nullable(str()), "description": nullable(str()), "template_json": nullable(str()), "icon": nullable(str()), "color": nullable(str())}),
		"ConsolidateRunRequest":             object(map[string]any{"crew_id": str(), "since": str()}),
		"ConsolidateRejectRequest":          object(map[string]any{"reason": str()}),
		"EvalReplayRequest":                 req(map[string]any{"mission_id": str(), "seed": integer()}, "mission_id"),
		"EvalRegressionRequest":             req(map[string]any{"baseline_mission_id": str(), "candidate_mission_id": str()}, "baseline_mission_id", "candidate_mission_id"),
		"InstanceSettingRequest":            req(map[string]any{"value": str()}, "value"),
		"FeedbackCreateRequest":             req(map[string]any{"message_id": str(), "chat_id": str(), "trace_id": str(), "signal": map[string]any{"type": "string", "enum": []string{"helpful", "not_helpful", "inaccurate", "unsafe", "edit", "regenerate"}}, "reason": str()}, "message_id", "signal"),
		"FeatureFlagCreateRequest":          req(map[string]any{"key": str(), "description": nullable(str()), "enabled": boolean(), "percentage": integer()}, "key"),
		"FeatureFlagUpdateRequest":          object(map[string]any{"description": nullable(str()), "enabled": boolean(), "percentage": integer()}),
		"FeatureFlagOverrideRequest":        req(map[string]any{"enabled": boolean()}, "enabled"),
		"NotificationTemplateRequest":       req(map[string]any{"category": str(), "channel_id": str(), "title": str(), "body": str()}, "category", "channel_id", "title", "body"),
		"EmptyRequest":                      empty(),
	}

	routes := map[string]DomainSchema{
		"POST /api/v1/admin/backups": {Request: ref("AdminBackupCreateRequest")}, "POST /api/v1/admin/backups/restore": {Request: ref("AdminBackupRestoreRequest")}, "POST /api/v1/admin/backups/rotate": {Request: ref("AdminBackupRotateRequest")}, "POST /api/v1/admin/backups/self-test": {Request: ref("AdminBackupSelfTestRequest")},
		"PUT /api/v1/admin/log-level": {Request: ref("AdminLogLevelRequest")}, "PUT /api/v1/admin/rate-limits/{key}": {Request: ref("AdminRateLimitRequest")}, "PATCH /api/v1/admin/memory/config": {Request: ref("AdminMemoryConfigRequest")}, "POST /api/v1/admin/prune-crew-runtimes": {Request: ref("EmptyRequest")}, "POST /api/v1/admin/prune-legacy-resources": {Request: ref("EmptyRequest")}, "POST /api/v1/admin/reap-orphan-containers": {Request: ref("EmptyRequest")}, "POST /api/v1/admin/reencrypt": {Request: ref("EmptyRequest")},
		"PUT /api/v1/admin/keeper/config": {Request: ref("AdminKeeperConfigRequest")}, "PUT /api/v1/admin/keeper/governance": {Request: ref("AdminKeeperGovernanceRequest")}, "PUT /api/v1/admin/keeper/aux/{slot}": {Request: ref("AdminKeeperAuxRequest")}, "POST /api/v1/admin/keeper/aux/use-judge": {Request: ref("EmptyRequest")}, "POST /api/v1/admin/keeper/aux/{slot}/probe": {Request: ref("EmptyRequest")}, "POST /api/v1/admin/keeper/judge/test": {Request: ref("AdminKeeperJudgeTestRequest")}, "POST /api/v1/admin/keeper/judge/test-hosted": {Request: ref("AdminKeeperHostedJudgeTestRequest")}, "POST /api/v1/admin/keeper/ask": {Request: ref("AdminKeeperAskRequest")}, "POST /api/v1/admin/keeper/requests/{requestId}/resolve": {Request: ref("AdminKeeperResolveRequest")}, "POST /api/v1/admin/keeper/findings/test": {Request: ref("AdminKeeperFindingsTestRequest")}, "POST /api/v1/admin/keeper/review/{slot}/run": {Request: ref("AdminKeeperReviewRequest")},
		"POST /api/v1/memory/import": {Request: ref("MemoryImportRequest")}, "POST /api/v1/memory/search/hybrid": {Request: ref("MemoryHybridSearchRequest")}, "POST /api/v1/memory/versions/{sha}/restore": {Request: ref("MemoryVersionRestoreRequest")},
		"POST /api/v1/recipes/{slug}/install": {Request: ref("RecipeInstallRequest")}, "POST /api/v1/templates": {Request: ref("TemplateCreateRequest")}, "PATCH /api/v1/templates/{templateId}": {Request: ref("TemplateUpdateRequest")}, "POST /api/v1/workflow-templates": {Request: ref("WorkflowTemplateCreateRequest")}, "PATCH /api/v1/workflow-templates/{id}": {Request: ref("WorkflowTemplateUpdateRequest")},
		"POST /api/v1/consolidate/run": {Request: ref("ConsolidateRunRequest")}, "POST /api/v1/consolidate/proposed/{id}/approve": {Request: ref("EmptyRequest")}, "POST /api/v1/consolidate/proposed/{id}/reject": {Request: ref("ConsolidateRejectRequest")},
		"POST /api/v1/eval/replay": {Request: ref("EvalReplayRequest")}, "POST /api/v1/eval/regression": {Request: ref("EvalRegressionRequest")}, "POST /api/v1/mcp-registry/sync": {Request: ref("EmptyRequest")}, "POST /api/v1/hooks/{id}/enable": {Request: ref("EmptyRequest")}, "POST /api/v1/hooks/{id}/disable": {Request: ref("EmptyRequest")}, "PUT /api/v1/instance/settings/{key}": {Request: ref("InstanceSettingRequest")}, "POST /api/v1/feedback": {Request: ref("FeedbackCreateRequest")},
		"POST /api/v1/feature-flags": {Request: ref("FeatureFlagCreateRequest")}, "PATCH /api/v1/feature-flags/{key}": {Request: ref("FeatureFlagUpdateRequest")}, "PUT /api/v1/feature-flags/{key}/override": {Request: ref("FeatureFlagOverrideRequest")}, "PUT /api/v1/notification-templates": {Request: ref("NotificationTemplateRequest")},
	}
	return routes, components
}
