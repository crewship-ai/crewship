package main

import "testing"

func TestWorkflowRequestSchemaAuditCoversNamedRoutes(t *testing.T) {
	routes, components := workflowRequestSchemaCatalog()
	for _, route := range []string{
		"POST /api/v1/workspaces/{workspaceId}/pipelines/save",
		"POST /api/v1/workspaces/{workspaceId}/pipelines/{slug}/run_batch",
		"POST /api/v1/crews/{crewId}/missions/{missionId}/tasks",
		"POST /api/v1/recurring-issues", "POST /api/v1/triage-rules",
		"POST /api/v1/projects/{projectId}/milestones", "PATCH /api/v1/escalations/{escalationId}/resolve",
		"POST /api/v1/missions/{missionId}/checkpoints",
	} {
		if _, ok := routes[route]; !ok {
			t.Errorf("missing audited request route %q", route)
		}
	}
	for _, name := range []string{"WorkflowPipelineSaveRequest", "WorkflowBatchRunRequest", "WorkflowMissionCreateRequest", "WorkflowRecurringIssueCreateRequest", "WorkflowTriageRuleCreateRequest", "WorkflowMilestoneCreateRequest", "WorkflowEscalationResolveRequest", "WorkflowCheckpointRequest"} {
		if _, ok := components[name]; !ok {
			t.Errorf("missing component %q", name)
		}
	}
}

func TestWorkflowRequestSchemasWireThroughDocument(t *testing.T) {
	doc := buildDocument([]route{{method: "POST", path: "/api/v1/workspaces/{workspaceId}/pipelines/save"}, {method: "PATCH", path: "/api/v1/escalations/{escalationId}/resolve"}})
	paths := doc["paths"].(map[string]any)
	for _, path := range []string{"/api/v1/workspaces/{workspaceId}/pipelines/save", "/api/v1/escalations/{escalationId}/resolve"} {
		op := paths[path].(map[string]any)
		method := "post"
		if path[8] == 'e' {
			method = "patch"
		}
		body := op[method].(map[string]any)["requestBody"].(map[string]any)
		schema := body["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
		if schema["$ref"] == nil {
			t.Fatalf("%s request schema is generic: %#v", path, schema)
		}
	}
}

func TestDraftPublicationDocumentsConflictAndProofErrors(t *testing.T) {
	operations := loadSpecOperations(t)
	operation := operations["/api/v1/workspaces/{workspaceId}/pipelines/{slug}/publish"]["post"]
	for _, code := range []string{"201", "400", "401", "403", "409", "422", "500"} {
		if len(operation.Responses[code]) == 0 {
			t.Errorf("publish response %s is undocumented", code)
		}
	}
}

func TestFixtureRequestBodyIsRequired(t *testing.T) {
	const path = "/api/v1/workspaces/{workspaceId}/pipelines/fixture_test"
	doc := buildDocument([]route{{method: "POST", path: path}})
	body := doc["paths"].(map[string]any)[path].(map[string]any)["post"].(map[string]any)["requestBody"].(map[string]any)
	if body["required"] != true {
		t.Fatalf("fixture request body is optional: %#v", body)
	}
}
