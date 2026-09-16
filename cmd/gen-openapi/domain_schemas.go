package main

// DomainSchemaMap returns the audited schemas for the execution-oriented API
// surface.  It is deliberately independent of route scanning: handlers often
// return maps or private DTOs, so deriving these shapes from Go reflection
// would document implementation details instead of the wire contract.
//
// The returned map is a fresh map and can be merged into an OpenAPI
// components.schemas map by callers which compose the generator.
func executionSchemaComponents() map[string]any {
	anyObject := map[string]any{"type": "object", "additionalProperties": true}
	stringMap := map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}}
	anyMap := map[string]any{"type": "object", "additionalProperties": true}
	refOrString := func(name string) map[string]any { return map[string]any{"$ref": "#/components/schemas/" + name} }
	arr := func(item map[string]any) map[string]any { return map[string]any{"type": "array", "items": item} }
	// Variadic `required`, matching schemas_core.go. Without it this file's
	// schemas cannot say which properties a response always carries, so a body
	// with every field renamed validates against them — see
	// docs/prd/response-shape-contract.md.
	obj := func(props map[string]any, required ...string) map[string]any {
		s := map[string]any{"type": "object", "properties": props}
		if len(required) > 0 {
			s["required"] = required
		}
		return s
	}
	str := func() map[string]any { return map[string]any{"type": "string"} }
	integer := func() map[string]any { return map[string]any{"type": "integer"} }
	number := func() map[string]any { return map[string]any{"type": "number"} }
	boolean := func() map[string]any { return map[string]any{"type": "boolean"} }
	timeString := func() map[string]any {
		return map[string]any{"type": "string", "format": "date-time"}
	}

	// pipeline.RunResult — what POST .../runs/{runId}/replay answers with, and
	// the body a dry run wraps. The required list is graded against the
	// struct's json tags by TestOpenAPIRequired_MatchesTheStructsOwnJSONTags.
	runResult := obj(map[string]any{
		"run_id": str(), "pipeline_id": str(), "pipeline_slug": str(),
		"status": map[string]any{"type": "string", "enum": []string{"COMPLETED", "FAILED", "CANCELLED", "DEDUPED", "WAITING", "DRY_RUN_OK"}},
		"output": str(), "step_outputs": stringMap, "would_execute": arr(refOrString("DryRunStep")),
		"duration_ms": integer(), "cost_usd": number(), "failed_at_step": str(),
		"error_message": str(), "deduped": boolean(), "waitpoint_token": str(), "current_step": str(),
	}, "run_id", "pipeline_id", "pipeline_slug", "status", "output", "step_outputs", "duration_ms", "cost_usd")
	// pipeline.DryRunStep: one planned step of a dry run.
	dryRunStep := obj(map[string]any{
		"step_id": str(), "step_type": str(), "would_call_agent": str(), "would_call_pipeline": str(),
		"would_pass": str(), "tier_adapter": str(), "tier_model": str(), "estimated_cost_usd": number(),
	}, "step_id", "step_type")
	// POST .../pipelines/{slug}/dry_run answers dryRunResponse: the RunResult
	// plus the planned manifest, null when the stored definition no longer
	// parses (pipelines_exec.go).
	dryRunResult := map[string]any{"allOf": []any{
		refOrString("RunResult"),
		obj(map[string]any{"manifest": map[string]any{"type": "object", "additionalProperties": true, "nullable": true}}, "manifest"),
	}}
	pipelineRun := obj(map[string]any{
		"id": str(), "workspace_id": str(), "pipeline_id": str(), "pipeline_slug": str(), "pipeline_name": str(),
		"status": str(), "mode": str(), "current_step_id": str(), "step_outputs": stringMap, "output": str(),
		"started_at": timeString(), "ended_at": timeString(), "error_message": str(), "failed_at_step": str(),
		"cost_usd": number(), "duration_ms": integer(), "triggered_via": str(), "triggered_by_id": str(),
		"idempotency_key": str(), "inputs": anyMap, "issue_identifier": str(), "metadata": anyMap,
		"is_replay": boolean(), "replay_of": str(), "tags": arr(str()), "warnings": arr(refOrString("RunWarning")),
		"sub_spans": map[string]any{"type": "object", "additionalProperties": arr(refOrString("AgentSubSpan"))},
		// outcome (§9.6, work package B6, #2349) — the routing decision, empty
		// for a non-terminal run or one that predates the column.
		"outcome": str(),
	})
	runRecord := obj(map[string]any{
		"id": str(), "pipeline_id": str(), "pipeline_slug": str(), "status": str(), "mode": str(),
		"started_at": timeString(), "ended_at": timeString(), "current_step_id": str(), "output": str(),
		"cost_usd": number(), "duration_ms": integer(), "error_message": str(), "failed_at_step": str(),
		"error_fingerprint": str(), "triggered_via": str(), "triggered_by_id": str(), "idempotency_key": str(),
		// Composition provenance. chain_depth is always present (0 = a run
		// somebody started); the rest appear only when they apply, and
		// automation_* is how a rule-fired run is told apart from a cron —
		// both arrive with triggered_via="schedule".
		"chain_depth": integer(), "chain_origin": str(),
		"automation_id": str(), "automation_name": str(), "trigger_event_type": str(),
		// outcome (§9.6, work package B6, #2349) — NO_CHANGE | SUCCEEDED |
		// WORK_CREATED | PARTIAL | NEEDS_HUMAN | FAILED | CANCELLED, empty
		// for a non-terminal run or one that predates the column.
		"outcome": str(),
	})
	schedule := obj(map[string]any{
		"id": str(), "workspace_id": str(), "name": str(), "target_pipeline_id": str(), "target_pipeline_slug": str(),
		"target_pipeline_version": integer(), "cron_expr": str(), "timezone": str(), "inputs": anyMap, "enabled": boolean(),
		"last_run_at": timeString(), "last_status": str(), "last_run_id": str(), "next_run_at": timeString(),
		"wake_pipeline_id": str(), "wake_pipeline_slug": str(), "wake_inputs": anyMap, "wake_fail_closed": boolean(),
		"wake_check_count": integer(), "wake_fire_count": integer(), "last_wake_at": timeString(), "last_wake_status": str(),
		"catchup_policy": str(), "last_missed_count": integer(), "consecutive_failures": integer(),
		"max_consecutive_failures": integer(), "disabled_reason": str(), "created_at": timeString(), "updated_at": timeString(),
	})
	warning := obj(map[string]any{"stage": str(), "message": str(), "at": timeString()})
	span := obj(map[string]any{"seq": integer(), "kind": str(), "name": str(), "started_at": timeString(), "duration_ms": integer(), "status": str(), "detail": anyObject, "input": anyObject, "output": anyObject, "input_truncated": boolean(), "output_truncated": boolean()})
	stateEntry := obj(map[string]any{"key": str(), "value": str(), "updated_at": timeString()})
	stateBucket := obj(map[string]any{"schedule_id": str(), "entries": arr(refOrString("StateEntry"))})
	waitpoint := obj(map[string]any{"decision_form": obj(map[string]any{"fields": arr(map[string]any{"type": "object", "additionalProperties": true}), "actions": arr(obj(map[string]any{"id": str(), "label": str(), "approved": map[string]any{"type": "boolean"}}))}), "token": str(), "pipeline_run_id": str(), "step_id": str(), "kind": str(), "prompt": str(), "invoking_crew_id": str(), "timeout_at": timeString(), "created_at": timeString(), "callback_url": str()})
	// pipeline_runs_replay.go's replayOutcome: only source_run_id is
	// unconditional; the other three carry omitempty.
	replayOutcome := obj(map[string]any{"source_run_id": str(), "new_run_id": str(), "status": str(), "error": str()}, "source_run_id")
	failureGroup := obj(map[string]any{"fingerprint": str(), "count": integer(), "pipeline_slug": str(), "failed_at_step": str(), "sample_error": str(), "run_ids": arr(str())})
	activeRun := obj(map[string]any{"run_id": str(), "workspace_id": str(), "pipeline_id": str(), "pipeline_slug": str(), "status": str(), "concurrency_key": str(), "started_at": timeString(), "cancel_requested": boolean()})
	// SchedulePreview — B9 (#2362), §13.2 "When". Stateless: no schedule id,
	// just the cron/timezone/count that produced the occurrences.
	schedulePreview := obj(map[string]any{
		"cron_expr": str(), "timezone": str(), "occurrences": arr(timeString()),
	})
	bulkReplayRequest := obj(map[string]any{"run_ids": arr(str()), "fingerprint": str(), "limit": integer()})
	stateWriteRequest := obj(map[string]any{"value": str(), "schedule_id": str()})
	// Appearance is a two-column write, so both fields are optional and an
	// explicit "" clears the stored value while an absent field keeps it —
	// the distinction the handler's pointer fields exist for.
	appearanceRequest := obj(map[string]any{
		"icon":  map[string]any{"type": "string", "maxLength": 64, "description": "Crew-icon name; \"\" clears it, omit to keep the stored value."},
		"color": map[string]any{"type": "string", "maxLength": 64, "description": "Gradient-palette id; \"\" clears it, omit to keep the stored value."},
	})
	// The success body is the re-read routine. The second branch is not
	// hypothetical: when the write lands but the re-read fails, the handler
	// deliberately answers 200 with just the two written values rather than
	// failing a change that already applied.
	appearanceRoutine := obj(map[string]any{
		"step_count": integer(),
		"id":         str(), "slug": str(), "name": str(), "description": str(), "dsl_version": str(),
		"definition_hash": str(), "ephemeral": boolean(), "workspace_visible": boolean(),
		"invocation_count": integer(), "last_invocation_status": str(), "last_invoked_at": timeString(),
		"icon": str(), "color": str(), "author_crew_id": str(), "author_agent_id": str(),
		"author_user_id": str(), "authored_via": str(), "status": str(),
		"integrations_required": arr(str()), "created_at": timeString(), "updated_at": timeString(),
	})
	appearanceResponse := map[string]any{"oneOf": []any{
		refOrString("PipelineAppearanceRoutine"),
		obj(map[string]any{"icon": str(), "color": str()}),
	}}

	return map[string]any{
		"RunResult": runResult, "DryRunStep": dryRunStep, "DryRunResult": dryRunResult, "PipelineRun": pipelineRun, "PipelineRunList": obj(map[string]any{"rows": arr(refOrString("PipelineRun")), "count": integer()}), "ActiveRunList": arr(activeRun),
		"RunRecord": runRecord, "RunRecordList": arr(refOrString("RunRecord")), "PipelineRunTree": arr(obj(map[string]any{"id": str(), "parent_id": str(), "pipeline_slug": str(), "status": str(), "triggered_via": str(), "cost_usd": number()})),
		"RunLogEntry": obj(map[string]any{"ts": timeString(), "level": str(), "message": str(), "type": str()}), "RunLogList": arr(refOrString("RunLogEntry")),
		"Schedule": schedule, "ScheduleList": arr(refOrString("Schedule")), "SchedulePreview": schedulePreview, "RoutineState": obj(map[string]any{"slug": str(), "buckets": arr(refOrString("StateBucket"))}),
		"StateEntry": stateEntry, "StateBucket": stateBucket, "Waitpoint": waitpoint, "WaitpointList": arr(refOrString("Waitpoint")),
		"ReplayOutcome": replayOutcome, "BulkReplayResult": obj(map[string]any{"requested": integer(), "replayed": integer(), "results": arr(refOrString("ReplayOutcome"))}, "requested", "replayed", "results"),
		"FailureGroup": failureGroup, "FailureGroupList": obj(map[string]any{"groups": arr(refOrString("FailureGroup"))}), "RunWarning": warning, "AgentSubSpan": span,
		// The run, schedule, replay and waitpoint-approval request bodies are
		// owned by schemas_workflow_request_audit.go (Workflow…Request).
		"BulkReplayRequest": bulkReplayRequest, "StateWriteRequest": stateWriteRequest,
		"PipelineAppearanceRequest": appearanceRequest, "PipelineAppearanceRoutine": appearanceRoutine,
		"PipelineAppearanceResponse": appearanceResponse,
	}
}

// DomainResponseSchemas maps endpoint paths to the named response schema used
// by the execution API.  The keys include the HTTP method to distinguish read
// operations from their mutation counterparts.
func executionResponseSchemas() map[string]string {
	return map[string]string{
		"GET /api/v1/workspaces/{workspaceId}/pipelines/{slug}/runs":        "RunRecordList",
		"GET /api/v1/workspaces/{workspaceId}/pipelines/{slug}/run-records": "RunRecordList",
		"GET /api/v1/workspaces/{workspaceId}/pipelines/waitpoints":         "WaitpointList",
		"GET /api/v1/workspaces/{workspaceId}/pipeline-schedules":           "ScheduleList",
		"GET /api/v1/workspaces/{workspaceId}/pipeline-schedules/preview":   "SchedulePreview",
		"GET /api/v1/workspaces/{workspaceId}/pipelines/runs/active":        "ActiveRunList",
		"GET /api/v1/workspaces/{workspaceId}/pipeline-runs":                "PipelineRunList",
		"GET /api/v1/workspaces/{workspaceId}/pipeline-runs/{runId}":        "PipelineRun",
		"GET /api/v1/workspaces/{workspaceId}/pipeline-runs/{runId}/tree":   "PipelineRunTree",
		"GET /api/v1/workspaces/{workspaceId}/pipeline-runs/{runId}/logs":   "RunLogList",
		"GET /api/v1/workspaces/{workspaceId}/pipelines/runs/errors":        "FailureGroupList",
		"GET /api/v1/workspaces/{workspaceId}/pipelines/{slug}/state":       "RoutineState",

		"PATCH /api/v1/workspaces/{workspaceId}/pipelines/{slug}/appearance": "PipelineAppearanceResponse",

		// Mutations that answer with an execution result rather than a
		// status envelope (pipelines_exec.go, pipeline_runs_replay.go).
		"POST /api/v1/workspaces/{workspaceId}/pipelines/{slug}/dry_run":      "DryRunResult",
		"POST /api/v1/workspaces/{workspaceId}/pipelines/runs/{runId}/replay": "RunResult",
		"POST /api/v1/workspaces/{workspaceId}/pipelines/runs/bulk_replay":    "BulkReplayResult",
	}
}

// DomainRequestSchemas maps mutation endpoints to their JSON request schema.
func executionRequestSchemas() map[string]string {
	return map[string]string{
		"POST /api/v1/workspaces/{workspaceId}/pipelines/runs/bulk_replay":   "BulkReplayRequest",
		"PUT /api/v1/workspaces/{workspaceId}/pipelines/{slug}/state/{key}":  "StateWriteRequest",
		"PATCH /api/v1/workspaces/{workspaceId}/pipelines/{slug}/appearance": "PipelineAppearanceRequest",
	}
}
