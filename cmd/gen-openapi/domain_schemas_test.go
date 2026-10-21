package main

import (
	"encoding/json"
	"testing"
)

func TestDomainSchemaMapCoversExecutionReadShapes(t *testing.T) {
	schemas := executionSchemaComponents()
	for _, name := range []string{
		"RunResult", "PipelineRun", "PipelineRunDetail", "AcceptedPipelineRun", "PipelineRunList", "RunRecordList", "Schedule", "RoutineState",
		"WaitpointList", "BulkReplayResult", "FailureGroupList", "RunLogList",
	} {
		if _, ok := schemas[name]; !ok {
			t.Errorf("missing schema %q", name)
		}
	}

	status := schemas["RunResult"].(map[string]any)["properties"].(map[string]any)["status"].(map[string]any)
	if len(status["enum"].([]string)) != 6 {
		t.Fatalf("RunResult status enum = %#v", status["enum"])
	}
	pipelineRun := schemas["PipelineRun"].(map[string]any)["properties"].(map[string]any)
	for _, field := range []string{"step_outputs", "inputs", "metadata", "warnings", "sub_spans", "is_replay"} {
		if _, ok := pipelineRun[field]; !ok {
			t.Errorf("PipelineRun missing %q", field)
		}
	}
}

func TestDomainResponseSchemasMatchReadRoutes(t *testing.T) {
	routes := executionResponseSchemas()
	want := map[string]string{
		"GET /api/v1/workspaces/{workspaceId}/pipeline-runs/{runId}":  "PipelineRunDetail",
		"GET /api/v1/workspaces/{workspaceId}/pipeline-schedules":     "ScheduleList",
		"GET /api/v1/workspaces/{workspaceId}/pipelines/{slug}/state": "RoutineState",
		"GET /api/v1/workspaces/{workspaceId}/pipelines/waitpoints":   "WaitpointList",
		// Mutations whose body is the execution result (#1849).
		"POST /api/v1/workspaces/{workspaceId}/pipelines/{slug}/dry_run":      "DryRunResult",
		"POST /api/v1/workspaces/{workspaceId}/pipelines/runs/{runId}/replay": "RunResult",
		"POST /api/v1/workspaces/{workspaceId}/pipelines/runs/bulk_replay":    "BulkReplayResult",
	}
	for route, schema := range want {
		if routes[route] != schema {
			t.Errorf("%s = %q, want %q", route, routes[route], schema)
		}
	}
}

func TestDomainRequestSchemasExposeHandlerBodies(t *testing.T) {
	schemas := executionSchemaComponents()
	routes := executionRequestSchemas()
	for route, schema := range map[string]string{
		"POST /api/v1/workspaces/{workspaceId}/pipelines/runs/bulk_replay":  "BulkReplayRequest",
		"PUT /api/v1/workspaces/{workspaceId}/pipelines/{slug}/state/{key}": "StateWriteRequest",
	} {
		if routes[route] != schema {
			t.Errorf("%s = %q, want %q", route, routes[route], schema)
		}
		if _, ok := schemas[schema]; !ok {
			t.Errorf("request schema %q is not defined", schema)
		}
	}
}

func TestDomainSchemaMapReturnsFreshMaps(t *testing.T) {
	one := executionSchemaComponents()
	one["RunResult"] = nil
	if executionSchemaComponents()["RunResult"] == nil {
		t.Fatal("DomainSchemaMap returned shared mutable state")
	}
}

func TestPipelineRunDetailSchemaSeparatesAcceptedStartsFromActualRuns(t *testing.T) {
	// Compile fresh generator components; exercise oneOf rather than just
	// checking that the newly named fields appear somewhere in the schema.
	raw, err := json.Marshal(map[string]any{"components": map[string]any{"schemas": executionSchemaComponents()}})
	if err != nil {
		t.Fatal(err)
	}
	schema := compileAt(t, raw, "/components/schemas/PipelineRunDetail")
	for _, test := range []struct {
		name, body string
		valid      bool
	}{
		{"queued", `{"id":"run1","workspace_id":"ws1","pipeline_id":"p1","pipeline_slug":"routine","status":"QUEUED","pending_id":"pending1","reason_code":"scheduled"}`, true},
		{"expired unstarted", `{"id":"run1","workspace_id":"ws1","pipeline_id":"p1","pipeline_slug":"routine","status":"EXPIRED","pending_id":"pending1","reason_code":"capacity_expired"}`, true},
		{"actual", `{"id":"run1","workspace_id":"ws1","pipeline_id":"p1","pipeline_slug":"routine","status":"completed","mode":"run","started_at":"2026-10-19T10:00:00Z"}`, true},
		{"missing pending reason", `{"id":"run1","workspace_id":"ws1","pipeline_id":"p1","pipeline_slug":"routine","status":"QUEUED","pending_id":"pending1"}`, false},
		{"missing pending identity", `{"id":"run1","workspace_id":"ws1","pipeline_id":"p1","pipeline_slug":"routine","status":"QUEUED","reason_code":"scheduled"}`, false},
		{"actual missing start", `{"id":"run1","workspace_id":"ws1","pipeline_id":"p1","pipeline_slug":"routine","status":"completed","mode":"run"}`, false},
		{"queued invents start", `{"id":"run1","workspace_id":"ws1","pipeline_id":"p1","pipeline_slug":"routine","status":"QUEUED","pending_id":"pending1","reason_code":"scheduled","started_at":"2026-10-19T10:00:00Z"}`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			var body any
			if err := json.Unmarshal([]byte(test.body), &body); err != nil {
				t.Fatal(err)
			}
			err := schema.Validate(body)
			if (err == nil) != test.valid {
				t.Fatalf("valid=%v want %v: %v", err == nil, test.valid, err)
			}
		})
	}
}
