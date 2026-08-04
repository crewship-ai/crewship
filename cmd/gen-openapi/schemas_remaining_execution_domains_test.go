package main

import "testing"

func TestRemainingExecutionDomainSchemaCatalogCoversConcreteContracts(t *testing.T) {
	catalog := remainingExecutionDomainSchemaCatalog()
	for _, route := range []string{
		"GET /api/v1/templates", "POST /api/v1/workflow-templates",
		"GET /api/v1/missions", "POST /api/v1/crews/{crewId}/missions/{missionId}/tasks",
		"GET /api/v1/memory/export", "POST /api/v1/memory/search/hybrid",
		"POST /api/v1/skills/proposed/approve", "POST /api/v1/workspaces/{workspaceId}/skills/bulk-import",
	} {
		if _, ok := catalog[route]; !ok {
			t.Errorf("catalog missing audited route %q", route)
		}
	}

	if got := catalog["GET /api/v1/templates"].Response["type"]; got != "array" {
		t.Fatalf("template list response type = %v, want array", got)
	}
	missionRequest := catalog["POST /api/v1/crews/{crewId}/missions"].Request["properties"].(map[string]any)
	for _, field := range []string{"title", "description", "lead_agent_id", "workflow_template"} {
		if _, ok := missionRequest[field]; !ok {
			t.Errorf("mission create request missing %q", field)
		}
	}
	searchResponse := catalog["POST /api/v1/memory/search/hybrid"].Response["properties"].(map[string]any)
	for _, field := range []string{"query", "count", "hits"} {
		if _, ok := searchResponse[field]; !ok {
			t.Errorf("memory search response missing %q", field)
		}
	}
}

func TestRemainingExecutionDomainSchemaCatalogWiresThroughDocument(t *testing.T) {
	doc := buildDocument([]route{
		{method: "POST", path: "/api/v1/workflow-templates"},
		{method: "GET", path: "/api/v1/missions"},
		{method: "POST", path: "/api/v1/memory/search/hybrid"},
	})
	paths := doc["paths"].(map[string]any)
	workflow := paths["/api/v1/workflow-templates"].(map[string]any)["post"].(map[string]any)
	request := workflow["requestBody"].(map[string]any)["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
	if request["type"] != "object" {
		t.Fatalf("workflow template request schema = %#v, want concrete object", request)
	}
	mission := paths["/api/v1/missions"].(map[string]any)["get"].(map[string]any)
	if got := responseSchemaFromOperation(t, mission)["type"]; got != "array" {
		t.Fatalf("mission list response type = %v, want array", got)
	}
	search := paths["/api/v1/memory/search/hybrid"].(map[string]any)["post"].(map[string]any)
	if got := responseSchemaFromOperation(t, search)["type"]; got != "object" {
		t.Fatalf("memory search response type = %v, want object", got)
	}
}

func TestRemainingExecutionDomainSchemaCatalogReturnsFreshMap(t *testing.T) {
	one := remainingExecutionDomainSchemaCatalog()
	delete(one, "GET /api/v1/templates")
	if _, ok := remainingExecutionDomainSchemaCatalog()["GET /api/v1/templates"]; !ok {
		t.Fatal("catalog returned shared mutable map")
	}
}
