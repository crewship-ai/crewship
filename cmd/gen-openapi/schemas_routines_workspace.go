package main

// routinesWorkspaceSchemaCatalog types the three read surfaces the routines
// workspace added: the calendar, a run's step executions and a run's
// artifacts. All three handlers build their rows from map literals with fixed
// keys, so these components are transcriptions of what the code already sends
// rather than a new contract.
//
// Executions and artifacts each answer in two shapes on the same path — a
// page of rows, or one row's payload when the caller names it with
// ?execution_id= / ?artifact_id= — so their 200 is a oneOf. The artifacts
// path also serves ?download= as raw bytes, which is not a JSON body and is
// described in docs/api-reference/routines.mdx instead.
func routinesWorkspaceSchemaCatalog() (map[string]DomainSchema, map[string]any) {
	str := func() map[string]any { return map[string]any{"type": "string"} }
	integer := func() map[string]any { return map[string]any{"type": "integer"} }
	boolean := func() map[string]any { return map[string]any{"type": "boolean"} }
	nullableString := func() map[string]any { return map[string]any{"type": "string", "nullable": true} }
	array := func(item map[string]any) map[string]any { return map[string]any{"type": "array", "items": item} }
	enum := func(values ...string) map[string]any { return map[string]any{"type": "string", "enum": values} }
	object := func(properties map[string]any, required ...string) map[string]any {
		schema := map[string]any{"type": "object", "properties": properties}
		if len(required) > 0 {
			schema["required"] = required
		}
		return schema
	}
	ref := func(name string) map[string]any {
		return map[string]any{"$ref": "#/components/schemas/" + name}
	}

	components := map[string]any{
		// schedule_id and timezone are carried by planned events only; status
		// and outcome by recorded runs only. Hence required covers the five
		// keys every kind writes.
		"RoutineCalendarEvent": object(map[string]any{
			"id": str(), "kind": enum("planned", "pending", "run"), "at": str(),
			"slug": str(), "name": str(), "schedule_id": str(), "timezone": str(),
			"status": str(), "outcome": str(),
		}, "id", "kind", "at", "slug", "name"),
		"RoutineCalendarResponse": object(map[string]any{
			"events": array(ref("RoutineCalendarEvent")), "truncated": boolean(),
		}, "events", "truncated"),

		"PipelineRunStepExecution": object(map[string]any{
			"id": str(), "parent_execution_id": str(), "step_id": str(),
			"execution_path": str(), "attempt": integer(), "kind": str(),
			"status": str(), "agent_slug": str(), "model": str(),
			"started_at": str(), "ended_at": str(), "error": str(),
			"output_bytes": integer(),
		}, "id", "parent_execution_id", "step_id", "execution_path", "attempt", "kind",
			"status", "agent_slug", "model", "started_at", "ended_at", "error", "output_bytes"),
		"PipelineRunStepExecutionList": object(map[string]any{
			"rows": array(ref("PipelineRunStepExecution")), "next_cursor": nullableString(),
		}, "rows", "next_cursor"),
		"PipelineRunStepExecutionOutput": object(map[string]any{
			"id": str(), "output": str(),
		}, "id", "output"),

		"PipelineRunArtifact": object(map[string]any{
			"id": str(), "step_execution_id": str(), "kind": str(), "label": str(),
			"state": str(), "media_type": str(), "sha256": str(),
			"content_bytes": integer(), "source": str(), "error": str(),
			"created_at": str(), "execution_path": str(), "attempt": integer(),
		}, "id", "step_execution_id", "kind", "label", "state", "media_type", "sha256",
			"content_bytes", "source", "error", "created_at", "execution_path", "attempt"),
		"PipelineRunArtifactList": object(map[string]any{
			"artifacts": array(ref("PipelineRunArtifact")), "truncated": boolean(),
			"next_cursor": nullableString(),
		}, "artifacts", "truncated", "next_cursor"),
		"PipelineRunArtifactContent": object(map[string]any{
			"id": str(), "content": str(),
		}, "id", "content"),
	}

	routes := map[string]DomainSchema{
		"GET /api/v1/workspaces/{workspaceId}/pipelines/calendar": {
			Response: ref("RoutineCalendarResponse"),
		},
		"GET /api/v1/workspaces/{workspaceId}/pipeline-runs/{runId}/executions": {
			Response: map[string]any{"oneOf": []any{
				ref("PipelineRunStepExecutionList"), ref("PipelineRunStepExecutionOutput"),
			}},
		},
		"GET /api/v1/workspaces/{workspaceId}/pipeline-runs/{runId}/artifacts": {
			Response: map[string]any{"oneOf": []any{
				ref("PipelineRunArtifactList"), ref("PipelineRunArtifactContent"),
			}},
		},
	}
	return routes, components
}
