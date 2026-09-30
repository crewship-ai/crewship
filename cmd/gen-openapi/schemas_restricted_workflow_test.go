package main

import "testing"

func TestRestrictedWorkflowSchemas(t *testing.T) {
	routes, components := restrictedWorkflowSchemaCatalog()
	for _, path := range []string{"restricted-routines", "restricted-pages", "restricted-routine-runs", "restricted-routine-runs/{runId}"} {
		route, ok := routes["GET /api/v1/workspaces/{workspaceId}/"+path]
		if !ok || route.Response == nil {
			t.Fatalf("missing typed private projection %s", path)
		}
	}
	request := components["RestrictedWorkflowRunRequest"].(map[string]any)
	if request["additionalProperties"] != false {
		t.Fatal("restricted request must reject authority fields")
	}
	props := request["properties"].(map[string]any)
	for _, field := range []string{"inputs", "expected_definition_hash", "expected_execution_hash", "delay_seconds"} {
		if props[field] == nil {
			t.Fatalf("missing request field %s", field)
		}
	}
	for _, field := range []string{"principal_id", "parent_handle", "model", "operation"} {
		if props[field] != nil {
			t.Fatalf("public authority selector %s", field)
		}
	}
	catalog := components["RestrictedRoutine"].(map[string]any)["properties"].(map[string]any)
	for _, field := range []string{"steps", "raw_yaml", "provider_key", "defaults"} {
		if catalog[field] != nil {
			t.Fatalf("catalog leaks %s", field)
		}
	}
	run := routes["POST /api/v1/workspaces/{workspaceId}/pipelines/{slug}/run"]
	if len(run.Request["anyOf"].([]any)) != 2 || len(run.Response["anyOf"].([]any)) != 2 {
		t.Fatal("ordinary run must retain the trusted contract beside the private contract")
	}
	page := routes["GET /api/v1/pages/{slug}/application/actions/{pendingId}"]
	if len(page.Response["anyOf"].([]any)) != 2 {
		t.Fatal("Page polling must retain both authorized execution projections")
	}
	action := components["RestrictedPageAction"].(map[string]any)["properties"].(map[string]any)
	input := components["RestrictedPageInput"].(map[string]any)["properties"].(map[string]any)
	for _, field := range []string{"params", "producer", "source", "recipe", "default"} {
		if action[field] != nil || input[field] != nil {
			t.Fatalf("private Page catalog exposes %s", field)
		}
	}
}
