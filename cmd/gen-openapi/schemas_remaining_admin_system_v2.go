package main

// remainingAdminSystemSchemaCatalogV2 is the final audited contract catalog
// for the operational endpoints that were still using the generator fallback.
// It is intentionally isolated: domain schema work must not require edits to
// another domain's catalog.
func remainingAdminSystemSchemaCatalogV2() map[string]DomainSchema {
	str := func() map[string]any { return map[string]any{"type": "string"} }
	integer := func() map[string]any { return map[string]any{"type": "integer"} }
	number := func() map[string]any { return map[string]any{"type": "number"} }
	boolean := func() map[string]any { return map[string]any{"type": "boolean"} }
	nullableStr := func() map[string]any { return map[string]any{"type": "string", "nullable": true} }
	dateTime := func() map[string]any { return map[string]any{"type": "string", "format": "date-time"} }
	// Variadic `required`, matching schemas_core.go. Without it this file's
	// schemas cannot say which properties a response always carries, so a body
	// with every field renamed validates against them — see
	// docs/prd/response-shape-contract.md.
	obj := func(properties map[string]any, required ...string) map[string]any {
		s := map[string]any{"type": "object", "properties": properties}
		if len(required) > 0 {
			s["required"] = required
		}
		return s
	}
	free := func() map[string]any {
		return map[string]any{"type": "object", "additionalProperties": true}
	}
	array := func(item map[string]any) map[string]any {
		return map[string]any{"type": "array", "items": item}
	}
	json := func(properties map[string]any) map[string]any { return obj(properties) }
	noContent := func() map[string]any { return obj(map[string]any{}) }

	workspace := obj(map[string]any{"id": str(), "name": str(), "slug": str()})
	user := obj(map[string]any{
		"id": str(), "email": str(), "full_name": nullableStr(), "avatar_url": nullableStr(),
		"created_at": str(), "workspace": workspace, "workspaces": array(workspace), "role": nullableStr(),
	})
	adminHealth := obj(map[string]any{
		"uptime_seconds": integer(), "log_level": obj(map[string]any{"level": str(), "baseline": str(), "expires_at": nullableStr()}),
		"db":                    obj(map[string]any{"ok": boolean(), "error": nullableStr()}),
		"disk":                  obj(map[string]any{"path": str(), "total_bytes": integer(), "free_bytes": integer(), "used_bytes": integer(), "used_percent": number(), "error": nullableStr()}),
		"encryption_key_source": str(),
	})
	keeperField := func(value map[string]any) map[string]any {
		return obj(map[string]any{"value": value, "source": str(), "editable": boolean()})
	}
	keeperProfile := obj(map[string]any{
		"name": keeperField(str()), "evidence": keeperField(boolean()), "evidence_facts": keeperField(array(str())),
		"hard_gate": keeperField(boolean()), "escalate_from": keeperField(integer()), "precedent": keeperField(boolean()),
		"precedent_n": keeperField(integer()), "consistency_samples": keeperField(integer()), "prompt_budget_tokens": keeperField(integer()),
		"overridden": boolean(), "choices": array(str()), "available_facts": array(str()), "stamp": str(),
	})
	keeperConfig := obj(map[string]any{
		"enabled": keeperField(boolean()), "judge_provider": keeperField(str()), "judge_endpoint_url": keeperField(str()),
		"judge_wire": keeperField(str()), "judge_model": keeperField(str()), "judge_timeout_ms": keeperField(integer()),
		"judge_profile": keeperProfile, "overridden": boolean(), "updated_at": nullableStr(), "updated_by": nullableStr(),
		"judge_configured": boolean(),
	}, "enabled", "judge_provider", "judge_endpoint_url", "judge_wire", "judge_model",
		"judge_timeout_ms", "judge_profile", "overridden", "judge_configured")
	keeperSlot := obj(map[string]any{
		"slot": str(), "label": str(), "provider": keeperField(str()), "model": keeperField(str()),
		"timeout_ms": keeperField(integer()), "credential_id": keeperField(str()), "overridden": boolean(),
		"updated_at": nullableStr(), "updated_by": nullableStr(),
	})
	keeperAux := obj(map[string]any{
		"slots": array(keeperSlot), "providers": array(str()), "judge_provider": str(), "judge_model": str(), "any_overridden": boolean(),
	}, "slots", "providers", "judge_provider", "judge_model", "any_overridden")
	keeperStage := obj(map[string]any{"name": str(), "label": str(), "ok": boolean(), "skipped": boolean(), "detail": str(), "latency_ms": integer()})
	keeperTest := obj(map[string]any{
		"ok": boolean(), "endpoint": str(), "model": str(), "stages": array(keeperStage), "models": array(str()), "decision": str(),
	})
	keeperModels := obj(map[string]any{
		"endpoint": str(), "models": array(str()), "suggestions": array(obj(map[string]any{"url": str(), "label": str()})), "error": str(),
	}, "endpoint", "models")
	keeperHealth := obj(map[string]any{
		"workspace_id": str(), "samples": integer(), "allow": integer(), "deny": integer(), "escalate": integer(), "judge_failures": integer(),
		"allow_rate": number(), "deny_rate": number(), "escalate_rate": number(), "progressed_rate": number(), "judge_failure_rate": number(),
		"p95_latency_ms": integer(), "min_samples": integer(), "alarm_progressed_rate": number(), "alarm_judge_failure_rate": number(),
		"alarm": obj(map[string]any{"kind": str(), "summary": str(), "at": str()}), "oldest": str(), "newest": str(),
	}, "workspace_id", "samples", "allow", "deny", "escalate", "judge_failures",
		"allow_rate", "deny_rate", "escalate_rate", "progressed_rate", "judge_failure_rate",
		"p95_latency_ms", "min_samples", "alarm_progressed_rate", "alarm_judge_failure_rate")
	policy := obj(map[string]any{
		"crew_id": str(), "autonomy_level": str(), "behavior_mode": str(), "set_by_user_id": str(), "set_at": dateTime(), "reason": str(),
	})
	version := obj(map[string]any{
		"current": str(), "latest": nullableStr(), "newer": boolean(), "url": nullableStr(), "commit": str(), "build_time": str(),
		"dirty": boolean(), "go_version": str(), "os": str(), "arch": str(), "schema_version": integer(),
	})
	license := obj(map[string]any{
		"edition": str(), "license_id": str(), "licensee_org": str(), "max_crews": integer(), "max_agents_per_crew": integer(), "max_members": integer(), "features": array(str()),
	})
	auxStatus := obj(map[string]any{"subsystems": array(obj(map[string]any{
		"id": str(), "label": str(), "provider": str(), "model": str(), "timeout_ms": integer(), "source": str(), "healthy": boolean(),
		"detail": str(), "reachable": boolean(), "reach_detail": str(),
	}))})
	setup := obj(map[string]any{"needs_bootstrap": boolean(), "allow_signup": boolean()})
	telemetry := obj(map[string]any{"enabled": boolean(), "install_id": str()})
	integrity := obj(map[string]any{"intact": boolean(), "checked": integer(), "broken_at": nullableStr(), "error": nullableStr()})
	prune := obj(map[string]any{"removed": array(str()), "count": integer(), "error": str()})
	reap := obj(map[string]any{
		"orphans": array(obj(map[string]any{"crew_id": str(), "slug": str(), "container_id": str(), "reaped": boolean()})),
		"count":   integer(), "applied": boolean(), "inspected": integer(), "identified": integer(), "detector_inert": boolean(),
	})
	return map[string]DomainSchema{
		"GET /api/v1/admin/health":                    {Response: adminHealth},
		"GET /api/v1/admin/users":                     {Response: array(user)},
		"GET /api/v1/admin/users/{userId}/data":       {Response: obj(map[string]any{"user": user, "journal": array(free()), "credentials": array(free()), "counts": free()})},
		"DELETE /api/v1/admin/users/{userId}/data":    {Response: noContent()},
		"GET /api/v1/admin/keeper/health":             {Response: keeperHealth},
		"GET /api/v1/admin/keeper/config":             {Response: keeperConfig},
		"PUT /api/v1/admin/keeper/config":             {Request: json(map[string]any{"enabled": boolean(), "judge_provider": str(), "judge_endpoint_url": str(), "judge_wire": str(), "judge_model": str(), "judge_timeout_ms": integer(), "judge_profile": str(), "judge_evidence": boolean(), "judge_evidence_facts": str(), "judge_hard_gate": boolean(), "judge_precedent": boolean(), "judge_precedent_n": integer(), "judge_consistency_samples": integer(), "judge_escalate_from": integer(), "judge_prompt_budget_tokens": integer()}), Response: keeperConfig},
		"DELETE /api/v1/admin/keeper/config":          {Response: keeperConfig},
		"GET /api/v1/admin/keeper/aux":                {Response: keeperAux},
		"PUT /api/v1/admin/keeper/aux/{slot}":         {Request: json(map[string]any{"provider": str(), "model": str(), "timeout_ms": integer(), "credential_id": str()}), Response: keeperAux},
		"DELETE /api/v1/admin/keeper/aux/{slot}":      {Response: keeperAux},
		"DELETE /api/v1/admin/keeper/aux":             {Response: keeperAux},
		"POST /api/v1/admin/keeper/aux/use-judge":     {Response: keeperAux},
		"POST /api/v1/admin/keeper/aux/{slot}/probe":  {Response: keeperTest},
		"GET /api/v1/admin/keeper/judge/models":       {Response: keeperModels},
		"POST /api/v1/admin/keeper/judge/test":        {Request: json(map[string]any{"judge_endpoint_url": str(), "judge_model": str()}), Response: keeperTest},
		"POST /api/v1/admin/keeper/judge/test-hosted": {Request: json(map[string]any{"provider": str(), "model": str(), "credential_id": str()}), Response: keeperTest},
		"POST /api/v1/admin/keeper/findings/test":     {Request: json(map[string]any{"workspace_id": str(), "crew_id": str(), "reason": str()}), Response: obj(map[string]any{"inbox_item_id": str(), "recipients": array(obj(map[string]any{"user_id": str(), "email": str(), "name": str(), "role": str()}))})},
		"POST /api/v1/admin/keeper/review/{slot}/run": {Request: json(map[string]any{"workspace_id": str(), "crew_id": str(), "agent_id": str(), "skill_id": str(), "tool_name": str(), "tool_args_snippet": str(), "recent_tool_calls": array(str()), "trigger": str(), "failure_snippet": str(), "prior_lesson": str()}), Response: obj(map[string]any{"slot": str(), "decision": str(), "reason": str(), "lesson": str(), "error": str(), "data": free()})},
		"GET /api/v1/admin/journal/verify":            {Response: integrity},
		"GET /api/v1/admin/legacy-resources":          {Response: obj(map[string]any{"present": boolean()})},
		"POST /api/v1/admin/prune-legacy-resources":   {Response: prune},
		"POST /api/v1/admin/prune-crew-runtimes":      {Response: prune},
		"POST /api/v1/admin/reap-orphan-containers":   {Response: reap},
		"GET /api/v1/system/setup-status":             {Response: setup},
		"GET /api/v1/system/telemetry":                {Response: telemetry},
		// GET /api/v1/system/runtime was declared here too, with a `runtimes`
		// item shape that had drifted from the one in
		// schemas_observability_payments.go. routeSchemaCatalog merges
		// last-writer-wins and this file is applied first, so the copy here had
		// no effect on the generated document — editing it (adding the `gaps`
		// field for #1672) changed nothing at all, silently. Removed rather
		// than kept in sync: a schema nobody reads is the same defect this
		// endpoint's own issue was opened about.
		"GET /api/v1/system/version":                             {Response: version},
		"GET /api/v1/system/license":                             {Response: license},
		"GET /api/v1/system/keeper":                              {Response: obj(map[string]any{"enabled": boolean(), "ollama_url": str(), "model": str(), "ollama_online": boolean(), "ollama_probed": boolean(), "gatekeeper_configured": boolean(), "total_requests": integer(), "allow_count": integer(), "deny_count": integer(), "escalate_count": integer(), "secret_count": integer(), "enabled_source": str(), "ollama_url_source": str(), "model_source": str(), "gov_model_configured": boolean(), "gov_model_provider": str(), "gov_model": str(), "gov_model_degraded": boolean(), "gov_model_degrade_reason": str()})},
		"GET /api/v1/system/aux-status":                          {Response: auxStatus},
		"GET /api/health":                                        {Response: obj(map[string]any{"status": str()})},
		"GET /api/v1/policies":                                   {Response: array(policy)},
		"GET /api/v1/runtimes/catalog":                           {Response: obj(map[string]any{"runtimes": array(obj(map[string]any{"id": str(), "name": str(), "description": str(), "available": boolean()}))})},
		"DELETE /api/v1/feature-flags/{key}":                     {Response: noContent()},
		"DELETE /api/v1/feature-flags/{key}/override":            {Response: noContent()},
		"DELETE /api/v1/admin/backups":                           {Response: obj(map[string]any{"deleted": integer(), "dry_run": boolean()})},
		"DELETE /api/v1/admin/backups/status":                    {Response: noContent()},
		"DELETE /api/v1/admin/rate-limits/{key}":                 {Response: obj(map[string]any{"key": str(), "value": integer(), "source": str(), "default": integer()})},
		"DELETE /api/v1/admin/keeper/requests":                   {Response: obj(map[string]any{"deleted": integer()})},
		"GET /api/v1/admin/keeper/requests":                      {Response: obj(map[string]any{"requests": array(free()), "count": integer()})},
		"GET /api/v1/admin/keeper/requests/{requestId}/events":   {Response: array(free())},
		"POST /api/v1/admin/keeper/requests/{requestId}/resolve": {Request: json(map[string]any{"decision": str(), "reason": str()}), Response: obj(map[string]any{"request_id": str(), "decision": str(), "reason": str(), "resolved": boolean(), "error": str()})},
	}
}
