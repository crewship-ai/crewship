package main

import "testing"

func TestDomainSchemaMapCoversExecutionReadShapes(t *testing.T) {
	schemas := executionSchemaComponents()
	for _, name := range []string{
		"RunResult", "PipelineRun", "PipelineRunList", "RunRecordList", "Schedule", "RoutineState",
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
		"GET /api/v1/workspaces/{workspaceId}/pipeline-runs/{runId}":  "PipelineRun",
		"GET /api/v1/workspaces/{workspaceId}/pipeline-schedules":     "ScheduleList",
		"GET /api/v1/workspaces/{workspaceId}/pipelines/{slug}/state": "RoutineState",
		"GET /api/v1/workspaces/{workspaceId}/pipelines/waitpoints":   "WaitpointList",
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
		"POST /api/v1/workspaces/{workspaceId}/pipelines/{slug}/run":        "PipelineRunRequest",
		"POST /api/v1/workspaces/{workspaceId}/pipeline-schedules":          "ScheduleRequest",
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
